"use strict";

const { spawn } = require("child_process");
const fs = require("fs");
const path = require("path");
const readline = require("readline");

const root = path.resolve(__dirname, "..");
const exe =
  process.platform === "win32"
    ? path.join(root, "backend", "defhop.exe")
    : path.join(root, "backend", "defhop");
const demo = path.join(root, "testdata", "demo");
const cacheDir = path.join(demo, ".cache");

if (!fs.existsSync(exe)) {
  console.error("missing backend:", exe);
  process.exit(1);
}

// Fresh cache each run — avoid stale packed index / deps skewing smoke.
fs.rmSync(cacheDir, { recursive: true, force: true });

function b64(s) {
  return Buffer.from(String(s), "utf8").toString("base64");
}

function run() {
  return new Promise((resolve, reject) => {
    const child = spawn(exe, [], { cwd: demo, windowsHide: true });
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

    function req(cmd, args, timeoutMs = 60000) {
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

    function findPos(text, needle, pred) {
      const lines = text.split(/\r?\n/);
      for (let i = 0; i < lines.length; i++) {
        const idx = lines[i].indexOf(needle);
        if (idx >= 0 && (!pred || pred(lines[i]))) {
          return { line: i, col: idx };
        }
      }
      return { line: 0, col: 0 };
    }

    (async () => {
      // cold INIT — exclude cache so Detect samples workspace sources, not deps jars
      const excludes = ".git,node_modules,target,.idea,.cache,.defhop";
      const init = await req("INIT", [demo, "", "21", excludes, cacheDir]);
      console.log("INIT", init.files, "files", "plugins=", init.plugins);
      const plugins = init.plugins || [];
      for (const need of ["mybatis", "lombok", "spring", "mapstruct", "feign"]) {
        if (!plugins.includes(need)) throw new Error("missing plugin " + need);
      }

      const controller = path.join(demo, "src/main/java/demo/UserController.java");
      const text = fs.readFileSync(controller, "utf8");
      const pos = findPos(text, "UserApi", (l) => l.includes("private"));
      // Empty text = production path (backend uses index/overlay, no IPC payload)
      const def = await req("DEF", [controller, "", String(pos.line), String(pos.col)]);
      console.log("DEF UserApi ->", (def.locations || []).map((l) => l.path + ":" + l.line));
      if (!(def.locations || []).length) throw new Error("DEF UserApi failed");
      if (!(def.locations[0].path || "").includes("UserService")) {
        throw new Error("expected jump to UserService impl, got " + def.locations[0].path);
      }

      // Instant overlay: UPDATE returns after overlay store; DEF uses empty text
      const live = text + "\n// live UserApi\n";
      const t0 = Date.now();
      await req("UPDATE", [controller, live]);
      const updateMs = Date.now() - t0;
      const livePos = findPos(live, "UserApi", (l) => l.includes("live"));
      const t1 = Date.now();
      const def2 = await req("DEF", [
        controller,
        "",
        String(livePos.line),
        String(livePos.col),
      ]);
      const defMs = Date.now() - t1;
      console.log("timing UPDATE", updateMs, "ms DEF", defMs, "ms");
      if (updateMs > 200) throw new Error("UPDATE should return instantly, took " + updateMs + "ms");
      if (defMs > 500) throw new Error("DEF too slow: " + defMs + "ms");
      console.log(
        "DEF live overlay UserApi ->",
        (def2.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(def2.locations || []).length) throw new Error("live overlay DEF failed");

      // MyBatis ROUTE
      const mapper = path.join(demo, "src/main/java/demo/UserMapper.java");
      const mtext = fs.readFileSync(mapper, "utf8");
      const mpos = findPos(mtext, "selectById");
      const route = await req("ROUTE", [mapper, mtext, String(mpos.line), String(mpos.col)]);
      console.log(
        "ROUTE selectById ->",
        (route.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(route.locations || []).length) throw new Error("ROUTE mybatis failed");

      // MapStruct DEF toDto -> Impl
      const ms = path.join(demo, "src/main/java/demo/UserMapperMS.java");
      const mst = fs.readFileSync(ms, "utf8");
      const msPos = findPos(mst, "toDto");
      const msDef = await req("DEF", [ms, mst, String(msPos.line), String(msPos.col)]);
      console.log(
        "DEF MapStruct toDto ->",
        (msDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(msDef.locations || []).some((l) => (l.path || "").includes("UserMapperMSImpl"))) {
        throw new Error("MapStruct DEF should land on Impl");
      }

      // MapStruct @Mapping target property
      const mapPos = findPos(mst, '"name"', (l) => l.includes("target"));
      const propDef = await req("DEF", [ms, mst, String(mapPos.line), String(mapPos.col + 1)]);
      console.log(
        "DEF @Mapping target ->",
        (propDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );

      // Method reference PricingProposal::getCInquiryCode -> field cInquiryCode
      const pref = path.join(demo, "src/main/java/demo/ProposalRef.java");
      const prefText = fs.readFileSync(pref, "utf8");
      const prefPos = findPos(prefText, "getCInquiryCode");
      const prefDef = await req("DEF", [
        pref,
        prefText,
        String(prefPos.line),
        String(prefPos.col),
      ]);
      console.log(
        "DEF ::getCInquiryCode ->",
        (prefDef.locations || []).map((l) => `${l.path}:${l.line}:${l.label || ""}`),
      );
      if (
        !(prefDef.locations || []).some(
          (l) => (l.path || "").includes("PricingProposal") && (l.line === 6 || l.label === "cInquiryCode"),
        )
      ) {
        const hit = (prefDef.locations || [])[0];
        if (!hit || !(hit.path || "").includes("PricingProposal")) {
          throw new Error("method ref getCInquiryCode should jump to PricingProposal field");
        }
      }

      // Scoped locals: defaultConfig in second() must not include first()
      const scopeFile = path.join(demo, "src/main/java/demo/ScopeLocals.java");
      const scopeText = fs.readFileSync(scopeFile, "utf8");
      const scopePos = findPos(scopeText, "defaultConfig", (l) => l.includes("b-"));
      await req("UPDATE", [scopeFile, scopeText]);
      const scopeRefs = await req("REFS", [
        scopeFile,
        "",
        String(scopePos.line),
        String(scopePos.col),
        "true",
      ]);
      const scopeLocs = scopeRefs.locations || [];
      console.log(
        "REFS scoped defaultConfig ->",
        scopeLocs.map((l) => `${l.path}:${l.line}`),
      );
      if (scopeLocs.length < 2) {
        throw new Error("expected decl+use of defaultConfig in lambda");
      }
      if (scopeLocs.some((l) => l.line < scopePos.line - 5)) {
        // first() method's defaultConfig is earlier in the file
        const firstPos = findPos(scopeText, "defaultConfig", (l) => l.includes("a-"));
        if (scopeLocs.some((l) => l.line === firstPos.line)) {
          throw new Error("scoped REFS leaked into first() method");
        }
      }

      // MyBatis-Plus ServiceImpl: baseMapper -> PubVouchRelationMapper
      const svc = path.join(demo, "src/main/java/demo/PubVouchRelationServiceImpl.java");
      const svcText = fs.readFileSync(svc, "utf8");
      const bmPos = findPos(svcText, "baseMapper", (l) => l.includes("return"));
      const bmDef = await req("DEF", [svc, svcText, String(bmPos.line), String(bmPos.col)]);
      console.log(
        "DEF baseMapper ->",
        (bmDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(bmDef.locations || []).some((l) => (l.path || "").includes("PubVouchRelationMapper"))) {
        throw new Error("baseMapper should jump to PubVouchRelationMapper");
      }
      // baseMapper.selectByBiz -> mapper method
      const selPos = findPos(svcText, "selectByBiz");
      const selDef = await req("DEF", [svc, svcText, String(selPos.line), String(selPos.col)]);
      console.log(
        "DEF baseMapper.selectByBiz ->",
        (selDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(selDef.locations || []).some((l) => (l.path || "").includes("PubVouchRelationMapper"))) {
        throw new Error("baseMapper.selectByBiz should jump to mapper method");
      }

      // Spring @Value -> application.yml
      const appCfg = path.join(demo, "src/main/java/demo/AppConfig.java");
      const appCfgText = fs.readFileSync(appCfg, "utf8");
      const valPos = findPos(appCfgText, "spring.application.name");
      const valDef = await req("DEF", [
        appCfg,
        appCfgText,
        String(valPos.line),
        String(valPos.col),
      ]);
      console.log(
        "DEF @Value spring.application.name ->",
        (valDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(valDef.locations || []).some((l) => (l.path || "").includes("application.yml"))) {
        throw new Error("@Value should jump to application.yml");
      }

      // Nested placeholder in default: spring.application.name inside greeting
      const nestPos = findPos(appCfgText, "spring.application.name", (l) => l.includes("greeting"));
      const nestDef = await req("DEF", [
        appCfg,
        appCfgText,
        String(nestPos.line),
        String(nestPos.col),
      ]);
      console.log(
        "DEF nested placeholder ->",
        (nestDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(nestDef.locations || []).some((l) => (l.path || "").includes("application.yml"))) {
        throw new Error("nested ${spring.application.name} should jump to yml");
      }

      // application.yml name -> AppConfig @Value
      const yml = path.join(demo, "src/main/resources/application.yml");
      const ymlText = fs.readFileSync(yml, "utf8");
      const ymlPos = findPos(ymlText, "name:", (l) => l.trim().startsWith("name"));
      // click on "name" key
      const ymlDef = await req("DEF", [yml, ymlText, String(ymlPos.line), String(ymlPos.col)]);
      console.log(
        "DEF yml spring.application.name ->",
        (ymlDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(ymlDef.locations || []).some((l) => (l.path || "").includes("AppConfig"))) {
        throw new Error("yml key should jump to AppConfig @Value");
      }

      // @ConfigurationProperties field displayName -> display-name in yml
      const featPos = findPos(appCfgText, "displayName", (l) => l.includes("private"));
      const featDef = await req("DEF", [
        appCfg,
        appCfgText,
        String(featPos.line),
        String(featPos.col),
      ]);
      console.log(
        "DEF FeatureProps.displayName ->",
        (featDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(featDef.locations || []).some((l) => (l.path || "").includes("application.yml"))) {
        throw new Error("@ConfigurationProperties field should jump to yml");
      }

      // bank-import.enabled: click key segment AND field name
      const biKeyPos = findPos(appCfgText, "bank-import.enabled");
      const biKeyDef = await req("DEF", [
        appCfg,
        appCfgText,
        String(biKeyPos.line),
        String(biKeyPos.col + "bank-import.".length),
      ]);
      console.log(
        "DEF @Value bank-import.enabled ->",
        (biKeyDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(biKeyDef.locations || []).some((l) => (l.path || "").includes("application.yml"))) {
        throw new Error("bank-import.enabled in @Value should jump to yml");
      }
      const biFieldPos = findPos(appCfgText, "enabled;", (l) => l.includes("boolean"));
      const biFieldDef = await req("DEF", [
        appCfg,
        appCfgText,
        String(biFieldPos.line),
        String(biFieldPos.col),
      ]);
      console.log(
        "DEF field enabled ->",
        (biFieldDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(biFieldDef.locations || []).some((l) => (l.path || "").includes("application.yml"))) {
        throw new Error("field enabled with @Value should jump to yml");
      }

      // Feign method → Controller (direct, no picker)
      const feignFile = path.join(demo, "src/main/java/demo/StockFeignClient.java");
      const feignText = fs.readFileSync(feignFile, "utf8");
      const feignPos = findPos(feignText, "add(", (l) => l.includes("String add"));
      const feignDef = await req("DEF", [
        feignFile,
        feignText,
        String(feignPos.line),
        String(feignPos.col),
      ]);
      console.log(
        "DEF Feign add ->",
        (feignDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(feignDef.locations || []).some((l) => (l.path || "").includes("StockOutController"))) {
        throw new Error("Feign add should jump to StockOutController");
      }

      // Controller → Feign refs
      const ctrlFile = path.join(demo, "src/main/java/demo/StockOutController.java");
      const ctrlText = fs.readFileSync(ctrlFile, "utf8");
      const ctrlPos = findPos(ctrlText, "add(", (l) => l.includes("public String add"));
      const ctrlRefs = await req("REFS", [
        ctrlFile,
        ctrlText,
        String(ctrlPos.line),
        String(ctrlPos.col),
        "true",
      ]);
      console.log(
        "REFS Controller add ->",
        (ctrlRefs.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(ctrlRefs.locations || []).some((l) => (l.path || "").includes("StockFeignClient"))) {
        throw new Error("Controller add REFS should include StockFeignClient");
      }

      // Path variable pattern
      const syncPos = findPos(feignText, "syncItem");
      const syncDef = await req("DEF", [
        feignFile,
        feignText,
        String(syncPos.line),
        String(syncPos.col),
      ]);
      console.log(
        "DEF Feign syncItem ->",
        (syncDef.locations || []).map((l) => `${l.path}:${l.line}`),
      );
      if (!(syncDef.locations || []).some((l) => (l.path || "").includes("StockOutController"))) {
        throw new Error("Feign syncItem should jump to Controller");
      }

      // Warm cache INIT (REINDEX)
      const warm = await req("REINDEX", [demo, "", "21", excludes, cacheDir]);
      console.log("REINDEX warm", warm.files, "files", warm.milliseconds, "ms");

      if (!(init.files > 0)) throw new Error("no files indexed");

      child.stdin.write(`${nextId++}\tSHUTDOWN\n`);
      console.log("SMOKE OK");
      resolve();
    })().catch((e) => {
      try {
        child.kill();
      } catch {}
      reject(e);
    });
  });
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});
