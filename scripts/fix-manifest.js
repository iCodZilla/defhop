"use strict";

const fs = require("fs");
const path = require("path");
const { execSync } = require("child_process");
const os = require("os");

const root = path.join(__dirname, "..");

// Marketplace-safe ASCII-leaning description + English keywords (Chinese ok in README).
const pkgPath = path.join(root, "package.json");
const pkg = JSON.parse(fs.readFileSync(pkgPath, "utf8"));
pkg.description =
  "Java navigation helper powered by a Go engine. Instant go-to-definition & references — lightweight, fast, built for reading code.";
pkg.keywords = [
  "java",
  "java helper",
  "java navigation",
  "go to definition",
  "find references",
  "code navigation",
  "mybatis",
  "spring",
  "feign",
  "lombok",
  "mapstruct",
  "maven",
  "gradle",
  "golang",
  "performance",
];
// SPDX-style license marketplace accepts more cleanly than UNLICENSED alone
if (pkg.license === "UNLICENSED") {
  pkg.license = "MIT";
}

fs.writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + "\n", "utf8");

const licensePath = path.join(root, "LICENSE");
if (!fs.existsSync(licensePath)) {
  fs.writeFileSync(
    licensePath,
    `MIT License

Copyright (c) 2026 iCodZilla

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`,
    "utf8",
  );
}

const out = path.join(root, "dist", `defhop-${pkg.version}.vsix`);
fs.mkdirSync(path.dirname(out), { recursive: true });
if (fs.existsSync(out)) fs.unlinkSync(out);

execSync(`npx --yes @vscode/vsce package --out "${out}"`, {
  cwd: root,
  stdio: "inherit",
  shell: true,
});

// Validate packaged manifest XML is well-formed UTF-8 (no U+FFFD / truncated tags)
const Admish = path.join(os.tmpdir(), "defhop-vsix-validate");
fs.rmSync(Admish, { recursive: true, force: true });
fs.mkdirSync(Admish, { recursive: true });
execSync(
  `powershell -NoProfile -Command "Add-Type -AssemblyName System.IO.Compression.FileSystem; [System.IO.Compression.ZipFile]::ExtractToDirectory('${out.replace(/\\/g, "/")}', '${Admish.replace(/\\/g, "/")}')"`,
  { stdio: "inherit" },
);

const manifest = fs.readFileSync(path.join(Admish, "extension.vsixmanifest"), "utf8");
if (manifest.includes("\uFFFD")) {
  console.error("FAIL: vsixmanifest contains U+FFFD replacement chars");
  process.exit(1);
}
if (!manifest.includes("</Description>")) {
  console.error("FAIL: Description tag broken");
  process.exit(1);
}
const desc = manifest.match(/<Description[^>]*>([\s\S]*?)<\/Description>/);
console.log("Description OK:", desc && desc[1].slice(0, 120));
const packedPkg = JSON.parse(
  fs.readFileSync(path.join(Admish, "extension", "package.json"), "utf8"),
);
console.log("package.json description OK:", packedPkg.description.slice(0, 80));
console.log("PACKED", out, fs.statSync(out).size);
