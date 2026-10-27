"use strict";

const { spawn, execSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");
const readline = require("readline");

const rootDir = path.join(__dirname, "..");
const exe = path.join(rootDir, "backend", "defhop.exe");

function b64(s) {
  return Buffer.from(String(s), "utf8").toString("base64");
}

function rssMB(pid) {
  const out = execSync(
    "powershell -NoProfile -Command \"(Get-Process -Id " + pid + ").WorkingSet64\"",
    { encoding: "utf8" },
  ).trim();
  return Math.round(Number(out) / 1024 / 1024);
}

function countJavaAndModules(projectRoot) {
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
      if (
        e.name === ".git" ||
        e.name === "node_modules" ||
        e.name === "target" ||
        e.name === "build" ||
        e.name === ".idea"
      ) {
        continue;
      }
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.name.endsWith(".java")) java++;
      else if (e.name === "pom.xml") poms++;
    }
  }
  walk(projectRoot);
  let topMods = 0;
  try {
    for (const e of fs.readdirSync(projectRoot, { withFileTypes: true })) {
      if (!e.isDirectory()) continue;
      const base = path.join(projectRoot, e.name);
      if (
        fs.existsSync(path.join(base, "pom.xml")) ||
        fs.existsSync(path.join(base, "build.gradle")) ||
        fs.existsSync(path.join(base, "build.gradle.kts"))
      ) {
        topMods++;
      }
    }
  } catch {
    /* ignore */
  }
  return { java, pomModules: poms, topLevelModules: topMods };
}

function findJavaWithClass(projectRoot) {
  let hit = null;
  function walk(dir, depth) {
    if (hit || depth > 8) return;
    let ents;
    try {
      ents = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of ents) {
      if (hit) return;
      if (
        e.name === ".git" ||
        e.name === "node_modules" ||
        e.name === "target" ||
        e.name === "build"
      ) {
        continue;
      }
      const p = path.join(dir, e.name);
      if (e.isDirectory()) walk(p, depth + 1);
      else if (e.name.endsWith(".java")) {
        const text = fs.readFileSync(p, "utf8");
        const m = text.match(/\b(class|interface|enum)\s+(\w+)/);
        if (m) {
          hit = { file: p, needle: m[2] };
          return;
        }
      }
    }
  }
  walk(projectRoot, 0);
  return hit;
}

function session(cwd) {
  const child = spawn(exe, [], { cwd, windowsHide: true });
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
  function req(cmd, args, timeoutMs = 600000) {
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
  return { child, req };
}

function percentile(sorted, p) {
  if (!sorted.length) return null;
  const i = Math.min(sorted.length - 1, Math.floor((p / 100) * sorted.length));
  return sorted[i];
}

async function run(label, projectRoot) {
  const scale = countJavaAndModules(projectRoot);
  const target = findJavaWithClass(projectRoot);
  if (!target) throw new Error("no java in " + projectRoot);

  const cache = path.join(os.tmpdir(), "defhop-bench-" + label);
  fs.rmSync(cache, { recursive: true, force: true });
  fs.mkdirSync(cache, { recursive: true });
  const { child, req } = session(projectRoot);
  try {
    const wall0 = Date.now();
    const init = await req("INIT", [
      projectRoot,
      "",
      "21",
      ".git,node_modules,target,.idea,logs,build",
      cache,
    ]);
    const coldWall = Date.now() - wall0;
    const rssAfterCold = rssMB(child.pid);

    const text = fs.readFileSync(target.file, "utf8");
    const lines = text.split(/\r?\n/);
    let line = 0;
    let col = 0;
    for (let i = 0; i < lines.length; i++) {
      const idx = lines[i].indexOf(target.needle);
      if (idx >= 0) {
        line = i;
        col = idx;
        break;
      }
    }
    const samples = [];
    for (let i = 0; i < 30; i++) {
      const t0 = Date.now();
      await req("DEF", [target.file, "", String(line), String(col)], 10000);
      samples.push(Date.now() - t0);
    }
    samples.sort((a, b) => a - b);

    const warm0 = Date.now();
    const warm = await req("REINDEX", [
      projectRoot,
      "",
      "21",
      ".git,node_modules,target,.idea,logs,build",
      cache,
    ]);
    const warmWall = Date.now() - warm0;
    const rssPeak = Math.max(rssAfterCold, rssMB(child.pid));

    return {
      label,
      scale,
      exeMB: +(fs.statSync(exe).size / 1024 / 1024).toFixed(2),
      cold: {
        wallMs: coldWall,
        engineMs: init.milliseconds,
        files: init.files,
        symbols: init.symbols,
        plugins: init.plugins,
        rssMB: rssAfterCold,
      },
      warm: { wallMs: warmWall, engineMs: warm.milliseconds, files: warm.files },
      defMs: {
        p50: percentile(samples, 50),
        p95: percentile(samples, 95),
        max: samples[samples.length - 1],
      },
      rssPeakMB: rssPeak,
    };
  } finally {
    child.kill();
  }
}

(async () => {
  const demo = await run("demo", path.join(rootDir, "testdata", "demo"));
  const mid = await run("mid", "D:/dev/nf-plat");
  const large = await run("large", "D:/dev/main");
  console.log(JSON.stringify({ demo, mid, large, when: new Date().toISOString() }, null, 2));
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
