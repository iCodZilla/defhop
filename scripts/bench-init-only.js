"use strict";

const { spawn, execSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");
const readline = require("readline");

const exe = path.join(__dirname, "..", "backend", "defhop.exe");

function b64(s) {
  return Buffer.from(String(s), "utf8").toString("base64");
}

function rssMB(pid) {
  const script = `(Get-Process -Id ${pid}).WorkingSet64`;
  const out = execSync(`powershell -NoProfile -Command ${JSON.stringify(script)}`, {
    encoding: "utf8",
  }).trim();
  return Math.round(Number(out) / 1024 / 1024);
}

function count(root) {
  let java = 0;
  let poms = 0;
  function walk(dir) {
    let ents;
    try {
      ents = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of ents) {
      if ([".git", "node_modules", "target", "build", ".idea"].includes(e.name)) continue;
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith(".java")) java++;
      else if (e.name === "pom.xml") poms++;
    }
  }
  walk(root);
  let top = 0;
  for (const e of fs.readdirSync(root, { withFileTypes: true })) {
    if (!e.isDirectory()) continue;
    if (fs.existsSync(path.join(root, e.name, "pom.xml"))) top++;
  }
  return { javaFiles: java, mavenModules: poms, topLevelModules: top };
}

function findSymbol(root) {
  function walk(dir, depth) {
    if (depth > 8) return null;
    let ents;
    try {
      ents = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return null;
    }
    for (const e of ents) {
      if ([".git", "node_modules", "target", "build"].includes(e.name)) continue;
      const p = path.join(dir, e.name);
      if (e.isDirectory()) {
        const hit = walk(p, depth + 1);
        if (hit) return hit;
      } else if (e.name.endsWith(".java")) {
        const text = fs.readFileSync(p, "utf8");
        const m = text.match(/\b(class|interface|enum)\s+(\w+)/);
        if (m) return { file: p, needle: m[2], text };
      }
    }
    return null;
  }
  return walk(root, 0);
}

async function initOnly(label, root) {
  const scale = count(root);
  const cache = path.join(os.tmpdir(), "defhop-init-" + label);
  fs.rmSync(cache, { recursive: true, force: true });
  fs.mkdirSync(cache, { recursive: true });

  const child = spawn(exe, [], { cwd: root, windowsHide: true });
  const rl = readline.createInterface({ input: child.stdout });
  const pending = new Map();
  let nextId = 1;
  rl.on("line", (line) => {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return;
    }
    const p = pending.get(String(msg.id));
    if (!p) return;
    pending.delete(String(msg.id));
    if (msg.ok === false) p.reject(new Error(msg.error));
    else p.resolve(msg);
  });

  function req(cmd, args, timeoutMs = 900000) {
    const id = String(nextId++);
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        pending.delete(id);
        reject(new Error("timeout " + cmd));
      }, timeoutMs);
      pending.set(id, {
        resolve: (v) => {
          clearTimeout(t);
          resolve(v);
        },
        reject: (e) => {
          clearTimeout(t);
          reject(e);
        },
      });
      child.stdin.write([id, cmd, ...args.map(b64)].join("\t") + "\n");
    });
  }

  try {
    process.stderr.write(`[${label}] INIT starting scale=${JSON.stringify(scale)}\n`);
    const t0 = Date.now();
    const init = await req("INIT", [
      root,
      "",
      "21",
      ".git,node_modules,target,.idea,logs,build",
      cache,
    ]);
    const wallMs = Date.now() - t0;
    const mem = rssMB(child.pid);

    const hit = findSymbol(root);
    const lines = hit.text.split(/\r?\n/);
    let line = 0;
    let col = 0;
    for (let i = 0; i < lines.length; i++) {
      const idx = lines[i].indexOf(hit.needle);
      if (idx >= 0) {
        line = i;
        col = idx;
        break;
      }
    }
    const samples = [];
    for (let i = 0; i < 15; i++) {
      const t = Date.now();
      await req("DEF", [hit.file, "", String(line), String(col)], 20000);
      samples.push(Date.now() - t);
    }
    samples.sort((a, b) => a - b);

    const result = {
      label,
      scale,
      wallMs,
      engineMs: init.milliseconds,
      files: init.files,
      symbols: init.symbols,
      plugins: init.plugins,
      rssMB: mem,
      defP50: samples[7],
      defP95: samples[13],
      exeMB: +(fs.statSync(exe).size / 1024 / 1024).toFixed(2),
    };
    console.log(JSON.stringify(result, null, 2));
    return result;
  } finally {
    try {
      child.kill();
    } catch {
      /* ignore */
    }
  }
}

(async () => {
  await initOnly("mid", "D:/dev/nf-plat");
  await initOnly("large", "D:/dev/main");
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
