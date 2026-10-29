"use strict";

/**
 * Defhop navigation smoke tests against testdata/samples.
 * Covers: Java lang (+ lambda), Spring, Spring Cloud OpenFeign,
 * MyBatis, MyBatis-Plus, Lombok, MapStruct.
 */

const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");
const readline = require("readline");

const root = path.resolve(__dirname, "..");
const exe =
  process.platform === "win32"
    ? path.join(root, "backend", "defhop.exe")
    : path.join(root, "backend", "defhop");
const samples = path.join(root, "testdata", "samples");
const cacheDir = path.join(samples, ".cache");
const javaRoot = path.join(samples, "src", "main", "java");

if (!fs.existsSync(exe)) {
  console.error("missing backend:", exe);
  process.exit(1);
}

function b64(s) {
  return Buffer.from(String(s), "utf8").toString("base64");
}

function j(...parts) {
  return path.join(javaRoot, ...parts);
}

function findPos(text, needle, pred) {
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const idx = lines[i].indexOf(needle);
    if (idx >= 0 && (!pred || pred(lines[i], i))) {
      return { line: i, col: idx };
    }
  }
  throw new Error("needle not found: " + needle);
}

function locStr(locs) {
  return (locs || []).map((l) => `${l.path}:${l.line}${l.label ? ":" + l.label : ""}`);
}

function assertSome(locs, pred, msg) {
  if (!(locs || []).some(pred)) {
    throw new Error(msg + " | got " + JSON.stringify(locStr(locs)));
  }
}

function run() {
  return new Promise((resolve, reject) => {
    if (fs.existsSync(cacheDir)) {
      fs.rmSync(cacheDir, { recursive: true, force: true });
    }
    const child = spawn(exe, [], { cwd: samples, windowsHide: true });
    const rl = readline.createInterface({ input: child.stdout });
    const pending = new Map();
    let nextId = 1;

    child.stderr.on("data", (c) => process.stderr.write(c));
    child.on("error", reject);

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

    function req(cmd, args, timeoutMs = 120000) {
      const id = String(nextId++);
      const line = [id, cmd, ...args.map(b64)].join("\t") + "\n";
      return new Promise((res, rej) => {
        const t = setTimeout(() => {
          pending.delete(id);
          rej(new Error("timeout " + cmd));
        }, timeoutMs);
        pending.set(id, {
          resolve: (v) => {
            clearTimeout(t);
            res(v);
          },
          reject: (e) => {
            clearTimeout(t);
            rej(e);
          },
        });
        child.stdin.write(line);
      });
    }

    async function defFile(relParts, needle, pred) {
      const file = j(...relParts);
      const text = fs.readFileSync(file, "utf8");
      const pos = findPos(text, needle, pred);
      const out = await req("DEF", [file, text, String(pos.line), String(pos.col)]);
      console.log(`DEF ${needle} @ ${relParts.join("/")} ->`, locStr(out.locations));
      return out.locations || [];
    }

    /** Retry DEF until pred matches (deps index runs after INIT returns). */
    async function waitDef(relParts, needle, linePred, locPred, msg, attempts = 40) {
      const file = j(...relParts);
      const text = fs.readFileSync(file, "utf8");
      const pos = findPos(text, needle, linePred);
      let last = [];
      for (let i = 0; i < attempts; i++) {
        const out = await req("DEF", [file, text, String(pos.line), String(pos.col)]);
        last = out.locations || [];
        if (last.some(locPred)) {
          console.log(`DEF ${needle} @ ${relParts.join("/")} ->`, locStr(last));
          return last;
        }
        await new Promise((r) => setTimeout(r, 50));
      }
      console.log(`DEF ${needle} @ ${relParts.join("/")} ->`, locStr(last));
      assertSome(last, locPred, msg);
      return last;
    }

    async function refsFile(relParts, needle, pred) {
      const file = j(...relParts);
      const text = fs.readFileSync(file, "utf8");
      const pos = findPos(text, needle, pred);
      const out = await req("REFS", [file, text, String(pos.line), String(pos.col), "true"]);
      console.log(`REFS ${needle} @ ${relParts.join("/")} ->`, locStr(out.locations));
      return out.locations || [];
    }

    async function routeFile(relParts, needle, pred) {
      const file = j(...relParts);
      const text = fs.readFileSync(file, "utf8");
      const pos = findPos(text, needle, pred);
      const out = await req("ROUTE", [file, text, String(pos.line), String(pos.col)]);
      console.log(`ROUTE ${needle} @ ${relParts.join("/")} ->`, locStr(out.locations));
      return out.locations || [];
    }

    (async () => {
      const init = await req("INIT", [
        samples,
        "",
        "21",
        ".git,node_modules,target,.idea,.cache",
        cacheDir,
      ]);
      console.log("INIT", init.files, "files", "plugins=", init.plugins, init.milliseconds, "ms");
      const plugins = init.plugins || [];
      for (const need of ["mybatis", "lombok", "spring", "mapstruct", "feign"]) {
        if (!plugins.includes(need)) throw new Error("missing plugin " + need);
      }
      if (!(init.files > 20)) throw new Error("expected samples to index many files, got " + init.files);

      // ---- Java language ----
      let locs = await defFile(["samples", "lang", "LangDemo.java"], "Dog dog", (l) =>
        l.includes("run("),
      );
      // click type Dog in parameter — needle "Dog" more precise
      locs = await defFile(["samples", "lang", "LangDemo.java"], "Dog", (l) => l.includes("run(Dog"));
      assertSome(locs, (l) => (l.path || "").includes("Dog.java"), "DEF Dog type");

      locs = await defFile(["samples", "lang", "LangDemo.java"], "speak()", (l) =>
        l.includes("animal.speak"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("Animal.java") || (l.path || "").includes("Dog.java"),
        "DEF animal.speak",
      );

      locs = await defFile(["samples", "lang", "Dog.java"], "speak()", (l) =>
        l.includes("public String speak"),
      );
      // IMPL / hierarchy optional — at least definition exists
      if (!locs.length) throw new Error("Dog.speak should resolve");

      locs = await defFile(["samples", "lang", "LangDemo.java"], "greet(", (l) =>
        l.includes("greeter.greet"),
      );
      assertSome(
        locs,
        (l) =>
          (l.path || "").includes("Greeter.java") || (l.path || "").includes("FriendlyGreeter.java"),
        "DEF greeter.greet",
      );

      locs = await defFile(["samples", "lang", "LangDemo.java"], "add(1, 2)");
      assertSome(locs, (l) => (l.path || "").includes("Overloads.java"), "DEF overloads.add");

      locs = await defFile(["samples", "lang", "LangDemo.java"], "Status.ACTIVE");
      assertSome(locs, (l) => (l.path || "").includes("Status.java"), "DEF Status.ACTIVE");

      locs = await defFile(["samples", "lang", "LangDemo.java"], "Point", (l) =>
        l.includes("new Point"),
      );
      assertSome(locs, (l) => (l.path || "").includes("Point.java"), "DEF record Point");
      locs = await defFile(["samples", "lang", "LangDemo.java"], "x()", (l) => l.includes("pt.x"));
      assertSome(
        locs,
        (l) => (l.path || "").includes("Point.java"),
        "DEF record accessor pt.x()",
      );
      locs = await defFile(["samples", "lang", "LangDemo.java"], "sum()", (l) =>
        l.includes("pt.sum"),
      );
      assertSome(locs, (l) => (l.path || "").includes("Point.java"), "DEF record body pt.sum()");

      // ---- Lambda / method references ----
      locs = await defFile(["samples", "lang", "LambdaPlayground.java"], "getId", (l) =>
        l.includes("Order::getId"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("Order.java"),
        "DEF Order::getId method reference",
      );

      locs = await defFile(["samples", "lang", "LambdaPlayground.java"], "getAmount", (l) =>
        l.includes("Order::getAmount") && l.includes("comparing"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("Order.java"),
        "DEF Comparator.comparing(Order::getAmount)",
      );

      locs = await defFile(["samples", "lang", "LambdaPlayground.java"], "getStatus", (l) =>
        l.includes("o.getStatus"),
      );
      assertSome(locs, (l) => (l.path || "").includes("Order.java"), "DEF lambda o.getStatus");

      // scoped local inside lambda (unique name avoids substring collisions)
      const lambdaFile = j("samples", "lang", "LambdaPlayground.java");
      const lambdaText = fs.readFileSync(lambdaFile, "utf8");
      const tagPos = findPos(lambdaText, "lambdaTag", (l) => l.includes("String lambdaTag"));
      await req("UPDATE", [lambdaFile, lambdaText]);
      const tagRefs = await req("REFS", [
        lambdaFile,
        "",
        String(tagPos.line),
        String(tagPos.col),
        "true",
      ]);
      console.log("REFS lambda local lambdaTag ->", locStr(tagRefs.locations));
      if ((tagRefs.locations || []).length < 2) {
        throw new Error("expected decl+use of lambdaTag inside lambda");
      }
      if ((tagRefs.locations || []).some((l) => (lambdaText.split(/\r?\n/)[l.line] || "").includes("lambdaLine"))) {
        throw new Error("lambdaTag REFS should not leak into forEachPrint lambdaLine");
      }

      // ---- Spring ----
      locs = await defFile(["samples", "spring", "OrderController.java"], "create(", (l) =>
        l.includes("orderService.create"),
      );
      assertSome(locs, (l) => (l.path || "").includes("OrderService.java"), "DEF orderService.create");

      locs = await defFile(
        ["samples", "spring", "SampleAppConfig.java"],
        "spring.application.name",
        (l) => l.includes("@Value") && !l.includes("greeting"),
      );
      assertSome(locs, (l) => (l.path || "").includes("application.yml"), "DEF @Value -> yml");

      locs = await defFile(
        ["samples", "spring", "SampleAppConfig.java"],
        "displayName",
        (l) => l.includes("private String displayName"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("application.yml"),
        "DEF @ConfigurationProperties field -> yml",
      );

      const yml = path.join(samples, "src", "main", "resources", "application.yml");
      const ymlText = fs.readFileSync(yml, "utf8");
      const ymlPos = findPos(ymlText, "name:", (l) => l.trim().startsWith("name"));
      const ymlDef = await req("DEF", [yml, ymlText, String(ymlPos.line), String(ymlPos.col)]);
      console.log("DEF yml name ->", locStr(ymlDef.locations));
      assertSome(
        ymlDef.locations || [],
        (l) => (l.path || "").includes("SampleAppConfig"),
        "yml key -> SampleAppConfig",
      );

      // ---- Spring Cloud OpenFeign ----
      locs = await defFile(["samples", "cloud", "OrderFeignClient.java"], "create(", (l) =>
        l.includes("Order create"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("OrderController.java"),
        "Feign create -> OrderController",
      );

      locs = await defFile(["samples", "cloud", "OrderFeignClient.java"], "ping(", (l) =>
        l.includes("String ping"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("OrderController.java"),
        "Feign ping -> OrderController",
      );

      locs = await refsFile(["samples", "spring", "OrderController.java"], "create(", (l) =>
        l.includes("public Order create"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("OrderFeignClient.java"),
        "Controller create REFS includes Feign",
      );

      // ---- MyBatis ----
      locs = await routeFile(["samples", "mybatis", "ProductMapper.java"], "selectById");
      assertSome(
        locs,
        (l) => (l.path || "").includes("ProductMapper.xml"),
        "ROUTE selectById -> XML",
      );

      locs = await defFile(["samples", "mybatis", "ProductRepository.java"], "selectById");
      assertSome(
        locs,
        (l) =>
          (l.path || "").includes("ProductMapper.java") ||
          (l.path || "").includes("ProductMapper.xml"),
        "DEF productMapper.selectById",
      );

      // ---- MyBatis-Plus ----
      locs = await defFile(["samples", "mybatisplus", "ItemServiceImpl.java"], "baseMapper", (l) =>
        l.includes("return"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("ItemMapper.java"),
        "DEF baseMapper -> ItemMapper",
      );

      locs = await defFile(
        ["samples", "mybatisplus", "ItemServiceImpl.java"],
        "selectByCode",
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("ItemMapper.java"),
        "DEF baseMapper.selectByCode",
      );

      // ---- Lombok ----
      locs = await defFile(["samples", "lombokdemo", "CustomerService.java"], "getName", (l) =>
        l.includes("Customer::getName"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("Customer.java"),
        "DEF Lombok Customer::getName",
      );
      locs = await defFile(["samples", "lombokdemo", "CustomerService.java"], "builder", (l) =>
        l.includes("Customer.builder"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("Customer.java"),
        "DEF Lombok Customer.builder()",
      );

      // ---- MapStruct ----
      locs = await defFile(["samples", "mapstructdemo", "CustomerMapStruct.java"], "toDto");
      assertSome(
        locs,
        (l) => (l.path || "").includes("CustomerMapStructImpl.java"),
        "MapStruct toDto -> Impl",
      );

      const msFile = j("samples", "mapstructdemo", "CustomerMapStruct.java");
      const msText = fs.readFileSync(msFile, "utf8");
      const mapPos = findPos(msText, '"displayName"', (l) => l.includes("target"));
      const propDef = await req("DEF", [
        msFile,
        msText,
        String(mapPos.line),
        String(mapPos.col + 1),
      ]);
      console.log("DEF @Mapping target displayName ->", locStr(propDef.locations));
      assertSome(
        propDef.locations || [],
        (l) => (l.path || "").includes("CustomerDto.java"),
        "@Mapping target -> CustomerDto.displayName",
      );

      // ---- Dependency sources (.defhop-deps/*-sources.jar; indexed async) ----
      locs = await waitDef(
        ["samples", "deps", "DepsUse.java"],
        "LibDemo",
        (l) => l.includes("new LibDemo"),
        (l) => (l.path || "").includes("LibDemo.java"),
        "DEF LibDemo -> dependency sources jar",
      );
      locs = await defFile(["samples", "deps", "DepsUse.java"], "hello(", (l) =>
        l.includes("demo.hello"),
      );
      assertSome(
        locs,
        (l) => (l.path || "").includes("LibDemo.java"),
        "DEF demo.hello -> LibDemo.hello in sources.jar",
      );

      const warm = await req("REINDEX", [
        samples,
        "",
        "21",
        ".git,node_modules,target,.idea,.cache",
        cacheDir,
      ]);
      console.log("REINDEX warm", warm.files, "files", warm.milliseconds, "ms");

      child.stdin.write(`${nextId++}\tSHUTDOWN\n`);
      console.log("SAMPLES SMOKE OK");
      resolve();
    })().catch((e) => {
      try {
        child.kill();
      } catch {
        /* ignore */
      }
      reject(e);
    });
  });
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});
