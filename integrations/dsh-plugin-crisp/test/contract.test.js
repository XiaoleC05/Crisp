import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const PACKAGE_DIR = fileURLToPath(new URL("..", import.meta.url));
const REPO_ROOT = fileURLToPath(new URL("../../..", import.meta.url));
const packageJson = JSON.parse(readFileSync(path.join(PACKAGE_DIR, "package.json"), "utf8"));

test("manifest is a private ESM bundle declaring its Cordis patch", () => {
  assert.equal(packageJson.private, true);
  assert.equal(packageJson.type, "module");
  assert.equal(typeof packageJson.name, "string");
  assert.ok(packageJson.name.startsWith("@local/"));
  assert.equal(packageJson.exports["."], "./index.js");
  assert.deepEqual(packageJson.dsh, { bundle: { patch: "./cordis.patch.yml" } });
  assert.equal(existsSync(path.join(PACKAGE_DIR, "index.js")), true);
  assert.equal(existsSync(path.join(PACKAGE_DIR, "cordis.patch.yml")), true);
});

test("display resources are exported and declared in files", () => {
  assert.equal(packageJson.exports["./icon"], "./icon.svg");
  assert.equal(packageJson.exports["./locale/*.json"], "./locale/*.json");
  for (const entry of ["index.js", "lib/", "cordis.patch.yml", "locale/*.json", "icon.svg"]) {
    assert.ok(packageJson.files.includes(entry), `files must include ${entry}`);
  }
});

test("the patch inserts one uniquely named row matching the package", () => {
  const patch = readFileSync(path.join(PACKAGE_DIR, "cordis.patch.yml"), "utf8");
  const rows = [...patch.matchAll(/^\s*-\s*id:\s*(\S+)\s*$/gm)].map((match) => match[1]);
  assert.deepEqual(rows, ["crisp-quality-gate"]);
  const names = [...patch.matchAll(/^\s*name:\s*'([^']+)'\s*$/gm)].map((match) => match[1]);
  assert.ok(names.includes(packageJson.name), "row name must be this package");
  assert.match(patch, /config:\s*\n\s*mode:\s*observe/);
});

test("locale resources carry meaningful display metadata in both languages", () => {
  for (const locale of ["en", "zh"]) {
    const file = path.join(PACKAGE_DIR, "locale", `${locale}.json`);
    assert.equal(existsSync(file), true, `missing locale ${locale}`);
    const parsed = JSON.parse(readFileSync(file, "utf8"));
    assert.equal(typeof parsed.meta?.title, "string");
    assert.ok(parsed.meta.title.trim().length >= 3, `${locale} title too short`);
    assert.equal(typeof parsed.meta?.description, "string");
    assert.ok(parsed.meta.description.trim().length >= 10, `${locale} description too short`);
  }
  const en = JSON.parse(readFileSync(path.join(PACKAGE_DIR, "locale", "en.json"), "utf8"));
  const zh = JSON.parse(readFileSync(path.join(PACKAGE_DIR, "locale", "zh.json"), "utf8"));
  assert.notEqual(en.meta.title, zh.meta.title);
});

test("the icon is a contained original SVG within the size limit", () => {
  const iconPath = path.join(PACKAGE_DIR, "icon.svg");
  const buffer = readFileSync(iconPath);
  assert.ok(buffer.length > 100);
  assert.ok(buffer.length < 256 * 1024);
  const svg = buffer.toString("utf8");
  assert.match(svg, /^<svg\b/);
  assert.match(svg, /xmlns="http:\/\/www\.w3\.org\/2000\/svg"/);
  assert.match(svg, /viewBox="/);
  assert.doesNotMatch(svg, /<script/i);
  assert.doesNotMatch(svg, /\b(?:href|src)\s*=\s*"https?:/i);
});

function filesPatternMatches(pattern, relPath) {
  if (pattern.endsWith("/")) return relPath.startsWith(pattern);
  if (!pattern.includes("*")) return relPath === pattern;
  const escaped = pattern
    .split("*")
    .map((part) => part.replace(/[.+?^${}()|[\]\\]/g, "\\$&"))
    .join("[^/]*");
  return new RegExp("^" + escaped + "$").test(relPath);
}

function runtimeFiles(dir = PACKAGE_DIR, prefix = "") {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const rel = prefix === "" ? entry.name : `${prefix}/${entry.name}`;
    if (rel === "test" || rel === "node_modules" || rel === "package.json") continue;
    if (entry.isDirectory()) out.push(...runtimeFiles(path.join(dir, entry.name), rel));
    else if (entry.isFile()) out.push(rel);
  }
  return out;
}

test("every runtime file is covered by the files list", () => {
  for (const rel of runtimeFiles()) {
    assert.ok(
      packageJson.files.some((pattern) => filesPatternMatches(pattern, rel)),
      `runtime file ${rel} is not covered by package.json files`,
    );
  }
});

test("the host module exports the apply entry point and no static DSH dependency", async () => {
  const moduleUrl = pathToFileURL(path.join(PACKAGE_DIR, "index.js")).href;
  const module = await import(moduleUrl);
  assert.equal(typeof module.apply, "function");
  assert.equal(typeof module.applyWithDeps, "function");
  assert.equal(module.name, "crisp-quality-gate");
  assert.equal(module.default, undefined, "must use the apply export form");

  const sources = ["index.js", ...runtimeFiles(path.join(PACKAGE_DIR, "lib"), "lib")];
  for (const rel of sources) {
    const source = readFileSync(path.join(PACKAGE_DIR, rel), "utf8");
    assert.doesNotMatch(
      source,
      /(?:^|\n)\s*import\s[^;]*?from\s+["']@deepseek-ai\//,
      `${rel} must not statically import installation packages`,
    );
    assert.doesNotMatch(source, /require\(\s*["']@deepseek-ai\//, `${rel} must not require installation packages`);
  }
  const gateSource = readFileSync(path.join(PACKAGE_DIR, "lib", "gate.js"), "utf8");
  assert.match(gateSource, /import\(\s*["']@deepseek-ai\/dsh-llm["']\s*\)/);
});

test("the guard rewrite instruction matches internal/policy/decision.go", (t) => {
  const goFile = path.join(REPO_ROOT, "internal", "policy", "decision.go");
  if (!existsSync(goFile) || !statSync(goFile).isFile()) {
    t.skip("internal/policy/decision.go is absent from this installation");
    return;
  }
  const source = readFileSync(goFile, "utf8");
  const match = /RewriteReason\s*=\s*"([^"]+)"/.exec(source);
  assert.ok(match, "policy.RewriteReason not found in decision.go");
  const expected = match[1];
  const configSource = readFileSync(path.join(PACKAGE_DIR, "lib", "config.js"), "utf8");
  const configMatch = /DEFAULT_REWRITE_INSTRUCTION\s*=\s*"([^"]+)"/.exec(configSource);
  assert.ok(configMatch, "DEFAULT_REWRITE_INSTRUCTION not found in lib/config.js");
  assert.equal(configMatch[1], expected);
});