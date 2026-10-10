// End-to-end harness: runs the real bbbudget binary against a throwaway data
// directory and drives headless Chromium over the DevTools protocol. Node's
// built-in WebSocket and test runner only: no npm packages.
//
// BBBUDGET_BIN overrides the binary (default bin/bbbudget, see `make e2e`).
// CHROME overrides the browser; without one the tests are skipped.

import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

const ROOT = resolve(import.meta.dirname, "..");

export function findChrome() {
  if (process.env.CHROME) return process.env.CHROME;
  for (const name of ["chromium", "chromium-browser", "google-chrome-stable", "google-chrome"]) {
    try {
      return execFileSync("which", [name], { encoding: "utf8" }).trim();
    } catch {}
  }
  return "";
}

// waitFor polls fn until it returns a truthy value, or fails with what it was waiting for.
export async function waitFor(fn, what, timeout = 5000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${what}${last ? `: ${last.message}` : ""}`);
}

// startServer runs `bbbudget serve` on a free port with its own data,
// templates and settings, so nothing touches the user's home.
export async function startServer() {
  const bin = process.env.BBBUDGET_BIN || join(ROOT, "bin", "bbbudget");
  if (!existsSync(bin)) throw new Error(`${bin} not found: run make build (or make e2e)`);
  const dir = mkdtempSync(join(tmpdir(), "bbbudget-e2e-"));
  const env = { ...process.env, BBBUDGET_SETTINGS: join(dir, "settings.json"), BBBUDGET_TEMPLATES: join(dir, "templates"), BBBUDGET_DIR: join(dir, "data") };
  const proc = spawn(bin, ["serve", "-addr", "127.0.0.1:0"], { env, stdio: ["ignore", "pipe", "pipe"] });
  let out = "";
  proc.stdout.on("data", (b) => (out += b));
  proc.stderr.on("data", (b) => (out += b));
  const base = await waitFor(() => out.match(/on (http:\/\/127\.0\.0\.1:\d+)/)?.[1], `the server to start (${out})`);
  const api = async (method, path, body) => {
    const res = await fetch(base + path, {
      method,
      headers: { "Content-Type": "application/json", "X-Requested-With": "bbbudget" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${data?.error || ""}`);
    return data;
  };
  return {
    base,
    api,
    budget: async (id) => (await api("GET", `/api/budgets/${id}`)).budget,
    async stop() {
      proc.kill();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

// startBrowser launches headless Chromium and returns a page driver.
export async function startBrowser(chrome) {
  const profile = mkdtempSync(join(tmpdir(), "bbbudget-e2e-chrome-"));
  const proc = spawn(chrome, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
    `--user-data-dir=${profile}`, "--remote-debugging-port=0", "--window-size=1280,1000", "about:blank",
  ], { stdio: "ignore" });
  const port = await waitFor(() => readFileSync(join(profile, "DevToolsActivePort"), "utf8").split("\n")[0], "Chromium to start", 15000);
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  const ws = new WebSocket(targets.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((ok, fail) => {
    ws.addEventListener("open", ok, { once: true });
    ws.addEventListener("error", fail, { once: true });
  });

  let seq = 0;
  const pending = new Map();
  const page = { errors: [], requests: [] };
  ws.addEventListener("message", (e) => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) {
      const { ok, fail } = pending.get(m.id);
      pending.delete(m.id);
      m.error ? fail(new Error(m.error.message)) : ok(m.result);
    } else if (m.method === "Runtime.exceptionThrown") {
      const d = m.params.exceptionDetails;
      page.errors.push(d.exception?.description || d.text);
    } else if (m.method === "Runtime.consoleAPICalled" && m.params.type === "error") {
      page.errors.push(m.params.args.map((a) => a.value ?? a.description).join(" "));
    } else if (m.method === "Network.requestWillBeSent") {
      page.requests.push(m.params.request.url);
    }
  });
  const send = (method, params = {}) =>
    new Promise((ok, fail) => {
      const id = ++seq;
      pending.set(id, { ok, fail });
      ws.send(JSON.stringify({ id, method, params }));
    });
  await send("Runtime.enable");
  await send("Page.enable");
  await send("Network.enable");

  // eval runs an expression in the page and returns its JSON value.
  page.eval = async (expr) => {
    const r = await send("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  };
  const q = (sel) => JSON.stringify(sel);
  // goto loads a page and waits until `ready` matches: by default a budget's
  // summary, which renders only once the budget has loaded (the workspace
  // itself shows earlier, while the budget is still being fetched).
  page.goto = async (url, ready = "#railSummary h2, #empty:not([hidden])") => {
    page.errors.length = 0;
    page.requests.length = 0;
    // A new hash alone wouldn't reload the page: start each visit from scratch.
    await send("Page.navigate", { url: "about:blank" });
    await send("Page.navigate", { url });
    await waitFor(() => page.eval(`document.readyState === "complete" && !!document.querySelector(${q(ready)})`), `${url} to load`);
  };
  page.exists = (sel) => page.eval(`!!document.querySelector(${q(sel)})`);
  page.visible = (sel) => page.eval(`(() => { const el = document.querySelector(${q(sel)}); if (!el) return false; const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden"; })()`);
  page.text = (sel) => page.eval(`document.querySelector(${q(sel)})?.textContent ?? null`);
  page.click = async (sel) => {
    await waitFor(() => page.exists(sel), `${sel} to exist`);
    await page.eval(`document.querySelector(${q(sel)}).click()`);
  };
  // fill sets a field the way typing would, then commits it (input + change).
  page.fill = async (sel, value) => {
    await waitFor(() => page.exists(sel), `${sel} to exist`);
    await page.eval(`(() => { const el = document.querySelector(${q(sel)}); el.focus(); el.value = ${q(value)};
      el.dispatchEvent(new Event("input", { bubbles: true })); el.dispatchEvent(new Event("change", { bubbles: true })); })()`);
  };
  page.viewport = (width, height, mobile = false) =>
    send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
  page.close = async () => {
    ws.close();
    proc.kill();
    await sleep(100);
    rmSync(profile, { recursive: true, force: true });
  };
  return page;
}
