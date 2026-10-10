// UI end-to-end tests: the real server, the real page, a real browser.
// Run with `make e2e`. Every test works on its own copy of the example budget.

import { test, before, after, afterEach } from "node:test";
import assert from "node:assert/strict";
import { findChrome, startServer, startBrowser, waitFor } from "./harness.mjs";

const chrome = findChrome();
const skip = chrome ? false : "no Chromium found (set CHROME)";
let srv, page, n = 0;

before(async () => {
  if (skip) return;
  srv = await startServer();
  page = await startBrowser(chrome);
});
after(async () => {
  await page?.close();
  await srv?.stop();
});
afterEach(async () => {
  if (skip) return;
  assert.deepEqual(page.errors, [], "the page logged errors");
  await page.viewport(1280, 1000);
});

// fresh creates a budget from the example template and opens it on a view.
async function fresh(view, ops = []) {
  const id = `e2e-${++n}`;
  await srv.api("POST", "/api/budgets", { id, template: "example" });
  if (ops.length) await srv.api("POST", `/api/budgets/${id}/ops`, { ops });
  await page.goto(`${srv.base}/#${id}/${view}`);
  return id;
}
const indexOf = async (id, name) => (await srv.budget(id)).items.findIndex((l) => l.name === name);

test("every view opens without errors or third-party requests", { skip }, async () => {
  await fresh("overview");
  for (const v of ["overview", "items", "plan", "scenarios", "setup"]) {
    await page.click(`.tabs [data-view="${v}"]`);
    await waitFor(() => page.visible(`#view-${v}`), `${v} to show`);
    assert.equal(await page.eval(`[...document.querySelectorAll("section.view")].filter((s) => !s.hidden).length`), 1, `only ${v} shows`);
  }
  const foreign = page.requests.filter((u) => !u.startsWith(srv.base) && !u.startsWith("data:"));
  assert.deepEqual(foreign, [], "the UI stays on its own origin");
  assert.ok(await page.visible('#view-setup a[href*="github.com"]'), "the source link required by the AGPL is shown");
});

test("an amount edited in place is saved and updates the summary", { skip }, async () => {
  const id = await fresh("items");
  const i = await indexOf(id, "Rent");
  const before = await page.text("#railSummary");
  await page.fill(`[data-path="items.${i}.amount"]`, "1000");
  await waitFor(async () => (await srv.budget(id)).items[i].amount === "1000", "the new rent to be saved");
  await waitFor(async () => (await page.text("#railSummary")) !== before, "the summary to change");
});

test("a new item opens in the editor and saves its name", { skip }, async () => {
  const id = await fresh("items");
  await page.click('#view-items button.add[data-action="add-item"]');
  await waitFor(() => page.eval(`document.querySelector("#sheet").open`), "the editor to open");
  const path = await waitFor(() => page.eval(`document.querySelector('#sheet [data-path$=".name"]')?.dataset.path`), "the name field");
  const i = Number(path.split(".")[1]);
  await page.fill(`[data-path="items.${i}.name"]`, "Gym");
  await page.fill(`[data-path="items.${i}.amount"]`, "35.50");
  await waitFor(async () => {
    const l = (await srv.budget(id)).items[i];
    return l?.name === "Gym" && l.amount === "35.5"; // the same exact amount, trailing zero dropped
  }, "the new item to be saved");
});

test("a deleted item comes back with undo", { skip }, async () => {
  const id = await fresh("items");
  const i = await indexOf(id, "Utilities");
  await page.click(`#items [data-action="edit"][data-kind="items"][data-i="${i}"]`);
  await page.click('#sheet [data-action="delete-item"]');
  await waitFor(() => page.visible("#toast"), "the undo toast");
  await waitFor(async () => (await indexOf(id, "Utilities")) < 0, "the delete to be saved");
  await page.click("#toastUndo");
  await waitFor(async () => (await indexOf(id, "Utilities")) === i, "undo to restore it in place");
});

test("a short month is covered from a pot only when asked", { skip }, async () => {
  const id = await fresh("overview", [
    { op: "set_pot", target: "savings", value: { balance: "3000" } },
    { op: "add_item", value: { name: "Boiler", amount: "2500", category: "home", schedule: { start: 0, every: 0 } } },
  ]);
  await waitFor(() => page.visible("#shortfall"), "the short-month card");
  const { report } = await srv.api("GET", `/api/budgets/${id}`);
  const short = report.months[0].short.amount;
  assert.ok(Number(short) > 0, "October is short");
  assert.equal(report.months[0].from_savings.amount, "0", "savings are not used by themselves");

  await page.click('#shortfall button[data-action="cover"][data-pot="savings"]');
  await waitFor(async () => (await srv.budget(id)).withdrawals.length === 1, "the transfer to be saved");
  const w = (await srv.budget(id)).withdrawals[0];
  assert.equal(w.pot, "savings");
  assert.equal(w.amount, short);
  await waitFor(async () => !(await page.visible("#shortfall")), "the card to go once covered");
});

test("income sent to a pot is saved whole", { skip }, async () => {
  const id = await fresh("items", [
    { op: "add_item", value: { name: "Bike sale", amount: "800", category: "income", schedule: { start: 1, every: 0 } } },
  ]);
  const i = await indexOf(id, "Bike sale");
  await page.click(`#items [data-action="edit"][data-kind="items"][data-i="${i}"]`);
  await page.fill(`[data-path="items.${i}.pot"]`, "savings");
  await waitFor(async () => (await srv.budget(id)).items[i].pot === "savings", "the route to be saved");
  const { report } = await srv.api("GET", `/api/budgets/${id}`);
  assert.equal(report.months[1].income_to_pots.amount, "800");
});

test("a debt is paid off in full from a pot, and the payoff can be removed", { skip }, async () => {
  const id = await fresh("plan", [
    { op: "add_item", value: { name: "Bike sale", amount: "3000", category: "income", schedule: { start: 2, every: 0 }, pot: "savings" } },
    { op: "add_debt", value: { name: "Store card", balance: "2500", apr_pct: "24", min_payment: "80" } },
  ]);
  await page.click('#debts [data-kind="debts"][data-i="0"]');
  assert.equal(await page.exists('[data-path="debts.0.payoff_from"]'), false, "no pot choice before a month");
  const key = (await srv.api("GET", `/api/budgets/${id}`)).report.months[2].month;
  await page.fill('[data-path="debts.0.payoff_month"]', key);
  await page.fill('[data-path="debts.0.payoff_from"]', "savings");
  await waitFor(async () => {
    const d = (await srv.budget(id)).debts[0];
    return d.payoff_month === 2 && d.payoff_from === "savings";
  }, "the payoff to be saved");

  const { report } = await srv.api("GET", `/api/budgets/${id}`);
  assert.equal(report.months[2].debts[0].balance.amount, "0", "cleared in month 2");
  assert.equal(report.months[2].payoff_from_pots.amount, report.months[2].payoff.amount, "paid from the pot");
  await page.click('[data-action="close-sheet"]');
  await waitFor(async () => /pay off in full .* from General savings/.test(await page.text('#debts [data-kind="debts"][data-i="0"] .meta')), "the row to show the plan");
  assert.ok(report.insights.some((x) => x.id === "debt_payoff_store-card" && x.tone === "good"));

  await page.click('#debts [data-kind="debts"][data-i="0"]');
  await page.fill('[data-path="debts.0.payoff_month"]', "");
  await waitFor(async () => {
    const d = (await srv.budget(id)).debts[0];
    return d.payoff_month === undefined && d.payoff_from === undefined;
  }, "the payoff and its pot to be removed together");
});

test("what-if previews without saving", { skip }, async () => {
  const id = await fresh("scenarios");
  const rev = (await srv.api("GET", `/api/budgets/${id}`)).revision;
  assert.equal(await page.eval(`document.querySelector("#previewBtn").disabled`), true, "nothing to preview yet");
  await page.click('[data-action="add-wi"]');
  await page.fill('[data-wi="0"][data-k="value"]', "100");
  await waitFor(() => page.eval(`!document.querySelector("#previewBtn").disabled`), "Preview to be enabled");
  await page.click("#previewBtn");
  await waitFor(async () => (await page.text("#wiResult"))?.trim(), "the preview");
  assert.equal((await srv.api("GET", `/api/budgets/${id}`)).revision, rev, "the budget is unchanged");
});

test("phones get the bottom tab bar, pinned summary and an add button", { skip }, async () => {
  await page.viewport(390, 844, true);
  const id = await fresh("items");
  assert.equal(await page.eval(`getComputedStyle(document.querySelector(".tabs")).position`), "fixed");
  assert.ok(await page.eval(`document.querySelector(".tabs").getBoundingClientRect().bottom >= 800`), "tab bar at the bottom");
  assert.equal(await page.visible("#railSummary"), false, "no side rail");
  await waitFor(() => page.visible("#minibar"), "the pinned summary");
  const count = (await srv.budget(id)).items.length;
  await page.click("#appAdd");
  await waitFor(() => page.eval(`document.querySelector("#sheet").open`), "the editor sheet");
  await waitFor(async () => (await srv.budget(id)).items.length === count + 1, "the new item to be saved");
});

test("app settings save from their page", { skip }, async () => {
  await page.goto(`${srv.base}/settings.html`, "#form:not([hidden])");
  await page.eval(`(() => { const f = document.querySelector("#form"); f.fetch_rates.checked = true; f.fetch_rates.dispatchEvent(new Event("change")); f.requestSubmit(); })()`);
  await waitFor(async () => (await srv.api("GET", "/api/settings")).fetch_rates === true, "the setting to be saved");
  await srv.api("PUT", "/api/settings", { fetch_rates: false }); // keep the rest offline
});

test("in Russian the page, the insights and decimal commas all work", { skip }, async () => {
  await srv.api("PUT", "/api/settings", { locale: "ru" });
  try {
    const id = await fresh("items");
    await waitFor(() => page.eval(`document.documentElement.lang === "ru"`), "the page to switch language");
    assert.equal(await page.text('.tabs [data-view="overview"] span'), "Обзор");
    // A decimal comma is a decimal point here, never a thousands separator.
    const i = await indexOf(id, "Rent");
    await page.fill(`[data-path="items.${i}.amount"]`, "1000,50");
    await waitFor(async () => (await srv.budget(id)).items[i].amount === "1000.5", "1000,50 to be saved as 1000.5");
    for (const v of ["overview", "plan", "scenarios", "setup"]) {
      await page.click(`.tabs [data-view="${v}"]`);
      await waitFor(() => page.visible(`#view-${v}`), `${v} to show`);
      const text = await page.text(`#view-${v}`);
      // The example budget's own names (Rent, Salary) stay as typed; the UI's words must not.
      for (const en of ["Savings", "Left in", "Add a", "What the numbers say", "Recent changes", "months"]) {
        assert.ok(!text.includes(en), `${v} still says "${en}"`);
      }
    }
    assert.match(await page.text("#insights"), /[а-я]{4}/, "insights are rendered in Russian too");
  } finally {
    await srv.api("PUT", "/api/settings", { locale: "auto" });
  }
});
