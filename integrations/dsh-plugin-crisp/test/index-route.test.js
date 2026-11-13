import assert from "node:assert/strict";
import { test } from "node:test";

import { applyWithDeps } from "../index.js";

/** Minimal fake Node response for the status handler. */
function fakeResponse() {
  return {
    statusCode: 200,
    headers: {},
    body: "",
    setHeader(name, value) {
      this.headers[String(name).toLowerCase()] = value;
    },
    end(chunk) {
      this.body += chunk === undefined || chunk === null ? "" : String(chunk);
      this.ended = true;
    },
  };
}

/** Fake context that records listeners/effects and captures optional injections. */
function fakeContext() {
  const effects = [];
  const injections = [];
  const ctx = {
    on() {},
    get() {
      return undefined;
    },
    effect(callback) {
      const dispose = callback();
      effects.push(dispose);
      return dispose;
    },
    inject(dependencies, callback) {
      injections.push({ dependencies, callback });
      return { dispose() {} };
    },
    logger: { warn() {} },
  };
  return { ctx, effects, injections };
}

const BASE_CONFIG = {
  enabled: true,
  mode: "observe",
  logFile: false,
  binaryPath: "D:\\\\no-such-dir\\\\crisp.exe",
};

test("the status route is registered when webServer becomes available later", () => {
  const { ctx, injections } = fakeContext();
  const applyDeps = {
    platform: "win32",
    env: {},
    logLine: () => {},
    runner: async () => {
      throw new Error("runner must not run in this test");
    },
  };
  applyWithDeps(ctx, BASE_CONFIG, applyDeps);

  assert.equal(injections.length, 1, "webServer is resolved with optional injection");
  assert.deepEqual(injections[0].dependencies, ["webServer"]);

  const routes = [];
  const webServer = {
    register(route) {
      routes.push(route);
      return () => {};
    },
  };
  const childEffects = [];
  const childCtx = {
    webServer,
    effect(callback) {
      const dispose = callback();
      childEffects.push(dispose);
      return dispose;
    },
  };
  injections[0].callback(childCtx);

  assert.equal(routes.length, 1);
  assert.equal(routes[0].kind, "exact");
  assert.equal(routes[0].path, "/crisp/status");
  assert.equal(typeof routes[0].handler, "function");

  const response = fakeResponse();
  routes[0].handler({ method: "GET" }, response);
  assert.equal(response.statusCode, 200);
  assert.match(response.headers["content-type"], /application\/json/);
  assert.equal(JSON.parse(response.body).ok, true);
});

test("the status route falls back to one-shot registration for older hosts", () => {
  const routes = [];
  const webServer = {
    register(route) {
      routes.push(route);
      return () => {};
    },
  };
  const ctx = {
    on() {},
    effect(callback) {
      callback();
    },
    get(name) {
      return name === "webServer" ? webServer : undefined;
    },
  };
  applyWithDeps(ctx, BASE_CONFIG, {
    platform: "win32",
    env: {},
    logLine: () => {},
    runner: async () => {
      throw new Error("runner must not run in this test");
    },
  });
  assert.equal(routes.length, 1);
  assert.equal(routes[0].path, "/crisp/status");
});
