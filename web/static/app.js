// Budget planner UI. The server owns every number: this file edits the budget
// document (amounts stay exact decimal strings) and renders the server's
// report. The only floats here are pixel positions on the chart.

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const bold = (s) => esc(s).replace(/\*\*(.+?)\*\*/g, "<b>$1</b>");
const show = (m) => (m ? m.display : "—");
const DEC_RE = /^[+-]?\d+(\.\d+)?$/;
const SAVE_DELAY = 600;
const POLL_MS = 3000;

const S = {
  meta: null,
  prefs: { fetch_rates: false, default_rate_source: "auto" }, // app settings (settings.html); offline until loaded
  list: [],
  id: null,
  revision: 0,
  budget: null,
  report: null,
  saving: false,
  pending: false, // local edits not yet sent
  fetching: 0, // rate fetches in flight: saving waits so a placeholder rate is never stored
  notice: null, // an error to keep showing after the next save, instead of "Saved"
  timer: null,
  wi: [], // what-if draft ops
  history: [], // recent changes, newest first (Activity)
};

// ---------- API ----------

async function api(method, path, body) {
  const headers = { "X-Requested-With": "bbbudget" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) {
    const err = new Error(data?.error || res.statusText);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

function status(text, isErr = false) {
  const el = $("#status");
  el.textContent = text;
  el.classList.toggle("err", isErr);
}

// ---------- months ----------

function monthKey(index) {
  const [y, m] = S.budget.start_month.split("-").map(Number);
  const t = y * 12 + (m - 1) + index;
  return `${Math.floor(t / 12)}-${String((t % 12) + 1).padStart(2, "0")}`;
}
function monthIndex(key) {
  if (!key) return null;
  const [y, m] = S.budget.start_month.split("-").map(Number);
  const [ky, km] = key.split("-").map(Number);
  return Math.max(0, ky * 12 + km - (y * 12 + m));
}

// ---------- ids ----------

function slug(name) {
  return (String(name).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40) || "item");
}
function newId(name, list) {
  const taken = new Set(list.map((x) => x.id));
  const base = slug(name);
  let id = base;
  for (let n = 2; taken.has(id); n++) id = `${base}-${n}`;
  return id;
}

// ---------- loading ----------

async function loadList() {
  S.list = await api("GET", "/api/budgets");
  const picker = $("#picker");
  picker.innerHTML = S.list.map((b) => `<option value="${esc(b.id)}">${esc(b.name)} (${esc(b.id)})</option>`).join("");
  $("#empty").hidden = S.list.length > 0;
  $("#workspace").hidden = S.list.length === 0;
  picker.hidden = S.list.length === 0;
}

async function open(id) {
  const v = await api("GET", `/api/budgets/${encodeURIComponent(id)}`);
  S.id = v.id;
  adopt(v, true);
  $("#picker").value = id;
  try { localStorage.setItem("bbbudget.last", id); } catch {}
  history.replaceState(null, "", `#${encodeURIComponent(id)}`);
  status(`Saved · revision ${S.revision}`);
  loadCompare();
  loadActivity();
}

function adopt(view, rerenderEditors) {
  S.revision = view.revision;
  S.budget = view.budget;
  S.report = view.report;
  showIssues([]);
  if (rerenderEditors) renderEditors();
  renderOutputs();
}

// ---------- saving ----------

function edited({ structural = false } = {}) {
  S.pending = true;
  if (structural) renderEditors();
  status("Unsaved changes…");
  clearTimeout(S.timer);
  S.timer = setTimeout(save, SAVE_DELAY);
}

async function save() {
  S.timer = null;
  if (S.fetching) { S.timer = setTimeout(save, SAVE_DELAY); return; }
  if (S.saving) return; // the in-flight save re-checks S.pending when it ends
  S.saving = true;
  S.pending = false;
  const sent = JSON.stringify(S.budget);
  try {
    const v = await api("PUT", `/api/budgets/${encodeURIComponent(S.id)}`, { revision: S.revision, budget: S.budget });
    S.revision = v.revision;
    S.report = v.report;
    showIssues([]);
    renderOutputs();
    if (S.notice) status(S.notice, true);
    else status(`Saved · revision ${S.revision}`);
    S.notice = null;
    loadCompare();
    loadActivity();
  } catch (e) {
    if (e.status === 422) {
      showIssues(e.data?.issues || [{ path: "", message: e.message }]);
      status("Not saved: fix the highlighted fields.", true);
    } else if (e.status === 409) {
      status("This budget was changed elsewhere (e.g. by Claude). Reloaded the latest version.", true);
      await open(S.id);
    } else {
      S.pending = true;
      status(`Not saved: ${e.message}. Retrying…`, true);
      S.timer = setTimeout(save, 3000);
    }
  } finally {
    S.saving = false;
    if (S.pending && JSON.stringify(S.budget) !== sent && !S.timer) S.timer = setTimeout(save, SAVE_DELAY);
  }
}

// Pick up changes made elsewhere (MCP, another tab) when idle.
async function poll() {
  if (!S.id || S.pending || S.saving || document.visibilityState !== "visible") return;
  try {
    const list = await api("GET", "/api/budgets");
    const me = list.find((b) => b.id === S.id);
    if (me && me.revision > S.revision && !S.pending && !S.saving) {
      const v = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}`);
      adopt(v, true);
      status(`Updated just now (changed elsewhere) · revision ${S.revision}`);
      loadCompare();
      loadActivity();
    }
  } catch {}
}

function showIssues(issues) {
  document.querySelectorAll(".invalid").forEach((el) => el.classList.remove("invalid"));
  const box = $("#issues");
  box.hidden = issues.length === 0;
  if (!issues.length) return;
  box.innerHTML = `<b>These need fixing before the budget can be saved:</b><ul>${issues.map((i) => `<li>${esc(i.path)}: ${esc(i.message)}</li>`).join("")}</ul>`;
  for (const i of issues) {
    const path = i.path.replace(/\[(\d+)\]/g, ".$1");
    document.querySelectorAll(`[data-path="${CSS.escape(path)}"]`).forEach((el) => el.classList.add("invalid"));
  }
}

// ---------- editors ----------

function getPath(obj, path) {
  return path.split(".").reduce((o, k) => (o == null ? undefined : o[k]), obj);
}
function setPath(obj, path, value) {
  const keys = path.split(".");
  const last = keys.pop();
  const target = keys.reduce((o, k) => o[k], obj);
  if (value === undefined) delete target[last];
  else target[last] = value;
}

const field = (label, path, { type = "text", attrs = "", cls = "" } = {}) => {
  const v = getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<input data-path="${esc(path)}" data-kind="${type === "decimal" ? "decimal" : type}" ${
    type === "decimal" ? 'inputmode="decimal" autocomplete="off"' : type === "int" ? 'type="number" step="1"' : `type="${type}"`
  } value="${esc(v ?? "")}" ${attrs}></label>`;
};
const select = (label, path, options, { structural = false, cls = "" } = {}) => {
  const v = getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<select data-path="${esc(path)}" data-kind="text" ${structural ? "data-structural" : ""}>${options
    .map(([val, text]) => `<option value="${esc(val)}" ${String(val) === String(v) ? "selected" : ""}>${esc(text)}</option>`)
    .join("")}</select></label>`;
};
const del = (action, i, what) => `<button type="button" class="del" data-action="${action}" data-i="${i}" aria-label="Remove ${esc(what)}">✕</button>`;

function currencyOptions() {
  return [S.budget.base_currency, ...S.budget.currencies.map((c) => c.code)].map((c) => [c, c]);
}

const RATE_SOURCES = [
  ["manual", "Typed by hand"],
  ["auto", "Auto: ECB, else market"],
  ["ecb", "ECB (official)"],
  ["currency-api", "currency-api (fiat + crypto)"],
  ["coingecko", "CoinGecko (crypto)"],
];
const isManual = (c) => !c.source || c.source === "manual";

const EVERY = [[0, "One-off"], [1, "Monthly"], [2, "Every 2 months"], [3, "Quarterly"], [6, "Every 6 months"], [12, "Yearly"]];

function renderEditors() {
  const b = S.budget;
  $("#title").textContent = b.name;
  const crypto = new Set(S.meta?.crypto || []);
  const strategies = (S.meta?.strategies || ["avalanche", "snowball", "listed"]).map((s) => [s, s[0].toUpperCase() + s.slice(1)]);
  $("#settings").innerHTML = [
    field("Name", "name"),
    select("Base currency", "base_currency", (S.meta?.currencies || [b.base_currency]).filter((c) => !crypto.has(c)).map((c) => [c, c]), { structural: true }),
    field("First month", "start_month", { type: "month" }),
    select("Months to project", "months", [[6, "6"], [12, "12"], [24, "24"], [36, "36"], [60, "60"]].concat(
      [6, 12, 24, 36, 60].includes(b.months) ? [] : [[b.months, String(b.months)]])),
    field(`Savings now (${b.base_currency})`, "savings_start", { type: "decimal" }),
    field("Share of leftover to debts (%)", "extra_to_debt_pct", { type: "decimal" }),
    select("Debt strategy", "debt_strategy", strategies),
  ].join("");

  const allCodes = (S.meta?.currencies || []).filter((c) => c !== b.base_currency);
  const codeOpts = allCodes.map((x) => [x, crypto.has(x) ? `${x} (crypto)` : x]);
  $("#currencies").innerHTML = b.currencies.map((c, i) => `<div class="row row-cur">
      ${select("Code", `currencies.${i}.code`, codeOpts, { structural: true })}
      ${select("Rate from", `currencies.${i}.source`, RATE_SOURCES, { structural: true })}
      ${field(`per 1 ${b.base_currency}`, `currencies.${i}.per_base`, { type: "decimal" })}
      ${field("Payment loss %", `currencies.${i}.transfer_fee_pct`, { type: "decimal" })}
      ${field("Cash-out loss %", `currencies.${i}.cash_out_fee_pct`, { type: "decimal", attrs: 'placeholder="0"' })}
      ${del("del-currency", i, c.code)}
      <p class="rate-note muted" data-rate="${esc(c.code)}"></p>
    </div>`).join("") || `<p class="muted">Only ${esc(b.base_currency)} so far.</p>`;
  $("#refreshRates").hidden = !b.currencies.length || !S.prefs.fetch_rates;

  const cats = b.categories.map((c) => [c.id, `${c.label}${c.kind === "income" ? " (income)" : ""}`]);
  $("#items").innerHTML = b.items.map((l, i) => {
    const color = b.categories.find((c) => c.id === l.category)?.color || "var(--line)";
    const sch = l.schedule;
    return `<div class="row row-item" style="box-shadow:inset 3px 0 0 ${esc(color)}">
      ${field("Name", `items.${i}.name`, { cls: "name" })}
      ${field("Amount", `items.${i}.amount`, { type: "decimal" })}
      ${select("Currency", `items.${i}.currency`, currencyOptions())}
      ${select("Category", `items.${i}.category`, cats, { structural: true })}
      ${select("Repeats", `items.${i}.schedule.every`, EVERY, { structural: true })}
      <label class="f">${sch.every === 0 ? "In" : "From"}<input type="month" data-path="items.${i}.schedule.start" data-kind="monthidx" value="${monthKey(sch.start)}"></label>
      ${sch.every === 0 ? "<span></span>" :
        `<label class="f">Until<input type="month" data-path="items.${i}.schedule.end" data-kind="monthidx-opt" value="${sch.end == null ? "" : monthKey(sch.end)}"></label>`}
      <span class="base" data-base="${esc(l.id)}"></span>
      ${del("del-item", i, l.name)}
    </div>`;
  }).join("") || `<p class="muted">No items yet.</p>`;

  $("#debts").innerHTML = b.debts.map((d, i) => `<div class="row row-debt" style="box-shadow:inset 3px 0 0 var(--debt)">
      ${field("Name", `debts.${i}.name`)}
      ${select("Currency", `debts.${i}.currency`, currencyOptions())}
      ${field("Balance", `debts.${i}.balance`, { type: "decimal" })}
      ${field("APR %", `debts.${i}.apr_pct`, { type: "decimal" })}
      ${field("Minimum / month", `debts.${i}.min_payment`, { type: "decimal" })}
      ${del("del-debt", i, d.name)}
    </div>`).join("") || `<p class="muted">No debts. Add credit cards or loans to see interest and payoff dates.</p>`;

  $("#goals").innerHTML = b.goals.map((g, i) => `<div class="row row-goal" style="box-shadow:inset 3px 0 0 var(--save)">
      ${field("Goal", `goals.${i}.name`)}
      ${field(`Target (${b.base_currency})`, `goals.${i}.target`, { type: "decimal" })}
      ${del("del-goal", i, g.name)}
    </div>`).join("") || `<p class="muted">No goals yet.</p>`;

  $("#categories").innerHTML = b.categories.map((c, i) => `<div class="row row-cat">
      ${field("Label", `categories.${i}.label`)}
      ${select("Kind", `categories.${i}.kind`, [["expense", "Expense"], ["income", "Income"]], { structural: true })}
      <label class="f">Colour<input type="color" data-path="categories.${i}.color" data-kind="text" value="${esc(c.color || "#888888")}"></label>
      ${del("del-category", i, c.label)}
    </div>`).join("");

  renderWhatIf();
  renderItemBases();
  renderRateNotes();
}

function onEditorInput(e) {
  const el = e.target;
  const path = el.dataset.path;
  if (!path || !S.budget) return;
  let v = el.value;
  switch (el.dataset.kind) {
    case "decimal":
      v = v.trim().replace(/,/g, "");
      el.classList.toggle("invalid", !DEC_RE.test(v));
      if (!DEC_RE.test(v)) return; // keep editing; never send a malformed amount
      break;
    case "int":
      v = parseInt(v, 10);
      if (!Number.isFinite(v)) return;
      break;
    case "month":
      if (!/^\d{4}-\d{2}$/.test(v)) return;
      break;
    case "monthidx":
      if (!v) return;
      v = monthIndex(v);
      break;
    case "monthidx-opt":
      v = v ? monthIndex(v) : undefined;
      break;
  }
  if (path === "months" || path.endsWith(".every")) v = parseInt(v, 10);
  const cur = path.match(/^currencies\.(\d+)\.(\w+)$/);
  if (cur && cur[2] === "cash_out_fee_pct" && v === "") v = undefined;
  if (cur && (cur[2] === "code" || cur[2] === "source")) {
    // Selects fire input then change: act once, on change.
    if (e.type !== "change") return;
    const c = S.budget.currencies[+cur[1]];
    const field = cur[2], prev = c[field];
    c[field] = v;
    if (!isManual(c)) {
      // A new code or source needs its rate before anything is saved: the old
      // rate would be wrong for it. If the fetch fails, the change is undone.
      S.pending = true;
      renderEditors();
      fetchRate(+cur[1], () => { c[field] = prev; });
      return;
    }
    edited({ structural: true });
    return;
  }
  if (cur && cur[2] === "per_base") {
    // A typed rate is a manual rate; otherwise the next refresh would overwrite it.
    const c = S.budget.currencies[+cur[1]];
    if (!isManual(c)) {
      c.source = "manual";
      delete c.rate_date;
      delete c.rate_from;
      const sel = document.querySelector(`[data-path="currencies.${cur[1]}.source"]`);
      if (sel) sel.value = "manual";
      renderRateNotes();
    }
  }
  if (path.endsWith(".every") && v === 0) delete getPath(S.budget, path.replace(/\.every$/, "")).end;
  setPath(S.budget, path, v);
  edited({ structural: el.hasAttribute("data-structural") && e.type === "change" });
}

// fetchRate quotes one currency from its source and puts the rate into the
// document, which then saves as any other edit. Saving waits meanwhile. If no
// rate comes back, undo() reverts the change that needed it, so a currency is
// never stored with a placeholder or another currency's rate.
async function fetchRate(i, undo) {
  const c = S.budget.currencies[i];
  const src = isManual(c) ? "auto" : c.source;
  status(`Fetching the ${c.code} rate…`);
  S.fetching++;
  try {
    const [q] = await api("GET", `/api/rates?base=${encodeURIComponent(S.budget.base_currency)}&codes=${encodeURIComponent(c.code)}&source=${encodeURIComponent(src)}`);
    if (S.budget.currencies[i] !== c) { // the row changed meanwhile
      if (S.pending && !S.timer && !S.saving) edited(); // never leave pending stuck
      return;
    }
    if (q.error) throw new Error(q.error);
    Object.assign(c, { per_base: q.new, source: src, rate_date: q.date, rate_from: q.from });
    edited({ structural: true });
  } catch (e) {
    const code = c.code;
    undo();
    S.notice = `Couldn't get the ${code} rate (${e.message}), so nothing changed. To use ${code} anyway, set “Rate from” to “Typed by hand” and type its rate.`;
    edited({ structural: true });
    status(S.notice, true);
  } finally {
    S.fetching--;
  }
}

// refreshAll fetches every non-manual rate on the server and saves them.
async function refreshAll() {
  if (!S.prefs.fetch_rates) {
    status("Fetching rates is turned off in Settings.", true);
    return;
  }
  if (!S.budget.currencies.some((c) => !isManual(c))) {
    status("Every rate is typed by hand. Pick a “Rate from” source for the ones to keep current.", true);
    return;
  }
  let res;
  await serverChange(async () => {
    try {
      res = await api("POST", `/api/budgets/${encodeURIComponent(S.id)}/rates/refresh`, {});
    } catch (e) {
      res = e.data;
      throw e;
    }
  });
  const ups = res?.rate_updates || [];
  const changed = ups.filter((u) => !u.error).map((u) => (u.old === u.new ? `${u.code} unchanged` : `${u.code} ${u.old} → ${u.new}`));
  const failed = ups.filter((u) => u.error).map((u) => u.code);
  if (changed.length) status(`Rates updated: ${changed.join(", ")}${failed.length ? ` · failed: ${failed.join(", ")}` : ""}`, failed.length > 0);
}

function renderRateNotes() {
  if (!S.report || !S.budget) return;
  const unit = Object.fromEntries((S.report.currencies || []).map((c) => [c.code, c]));
  const fmt = new Intl.NumberFormat("en-GB", { style: "currency", currency: S.report.base_currency, maximumFractionDigits: 2 });
  document.querySelectorAll("[data-rate]").forEach((el) => {
    const c = S.budget.currencies.find((x) => x.code === el.dataset.rate);
    const u = unit[el.dataset.rate];
    if (!c) return;
    const n = u ? new Intl.NumberFormat("en-GB").format(Number(u.units)) : "";
    const worth = u ? `${n} ${c.code} = ${fmt.format(Number(u.base_value.amount))}` : ""; // display only
    const from = isManual(c) ? "typed by hand" : c.rate_date ? `${({ ecb: "ECB", coingecko: "CoinGecko" })[c.rate_from] || c.rate_from} rate of ${c.rate_date}` : "not fetched yet";
    el.textContent = [worth, from].filter(Boolean).join(" · ");
  });
}

const ACTIONS = {
  "add-item"() {
    const b = S.budget;
    const cat = b.categories.find((c) => c.kind === "expense") || b.categories[0];
    b.items.push({ id: newId("New item", b.items), name: "New item", amount: "0", currency: b.base_currency, category: cat.id, schedule: { start: 0, every: 1 } });
    edited({ structural: true });
    focusLast("#items input[data-path$='.name']");
  },
  "del-item"(i) { S.budget.items.splice(i, 1); edited({ structural: true }); },
  "add-currency"() {
    const b = S.budget;
    const used = new Set([b.base_currency, ...b.currencies.map((c) => c.code)]);
    const crypto = new Set(S.meta?.crypto || []);
    const code = (S.meta?.currencies || []).find((c) => !used.has(c) && !crypto.has(c));
    if (!code) return;
    const fetch = S.prefs.fetch_rates && S.prefs.default_rate_source !== "manual";
    // The rate starts at 1 for a moment, until fetchRate fills in the real one.
    b.currencies.push({ code, per_base: "1", transfer_fee_pct: "0", source: fetch ? S.prefs.default_rate_source : "manual" });
    if (!fetch) { edited({ structural: true }); return; }
    // Pending now, so poll() can't replace the budget (and drop this row) while
    // the rate loads; fetchRate's edited() then saves it.
    S.pending = true;
    renderEditors();
    const row = b.currencies[b.currencies.length - 1];
    fetchRate(b.currencies.length - 1, () => {
      const at = S.budget.currencies.indexOf(row);
      if (at >= 0) S.budget.currencies.splice(at, 1);
    });
  },
  "refresh-rates"() { refreshAll(); },
  "del-currency"(i) { S.budget.currencies.splice(i, 1); edited({ structural: true }); },
  "add-debt"() {
    const b = S.budget;
    b.debts.push({ id: newId("Debt", b.debts), name: "Debt", currency: b.base_currency, balance: "0", apr_pct: "0", min_payment: "0" });
    edited({ structural: true });
    focusLast("#debts input[data-path$='.name']");
  },
  "del-debt"(i) { S.budget.debts.splice(i, 1); edited({ structural: true }); },
  "add-goal"() {
    const b = S.budget;
    b.goals.push({ id: newId("Goal", b.goals), name: "Goal", target: "1000" });
    edited({ structural: true });
    focusLast("#goals input[data-path$='.name']");
  },
  "del-goal"(i) { S.budget.goals.splice(i, 1); edited({ structural: true }); },
  "add-category"() {
    const b = S.budget;
    b.categories.push({ id: newId("Category", b.categories), label: "Category", kind: "expense", color: "#7a6ff0" });
    edited({ structural: true });
  },
  "del-category"(i) {
    const c = S.budget.categories[i];
    if (S.budget.items.some((l) => l.category === c.id)) { status(`“${c.label}” is still used by an item.`, true); return; }
    S.budget.categories.splice(i, 1);
    edited({ structural: true });
  },
  new() { openNewDialog(); },
  "add-wi"() { S.wi.push({ kind: "item", target: S.budget.items[0]?.id || "", value: "" }); renderWhatIf(); },
  "del-wi"(i) { S.wi.splice(i, 1); renderWhatIf(); },
  "preview-wi"() { previewWhatIf(); },
  "save-wi"() { saveWhatIf(); },
  undo(i, el) { undoChange(i, el); },
  async "adopt-scenario"(i, el) {
    if (!confirm(`Apply “${el.dataset.name}” to the real budget?`)) return;
    await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/scenarios/${encodeURIComponent(el.dataset.sid)}/adopt`, {}));
  },
  async "del-scenario"(i, el) {
    await serverChange(() => api("DELETE", `/api/budgets/${encodeURIComponent(S.id)}/scenarios/${encodeURIComponent(el.dataset.sid)}`));
  },
};

// serverChange runs an action that changes the budget on the server: local
// edits are saved first so they are not lost, then the result is reloaded.
async function serverChange(action) {
  if (S.timer) { clearTimeout(S.timer); S.timer = null; }
  if (S.pending) await save();
  try {
    await action();
    await open(S.id);
  } catch (e) {
    status(e.message, true);
  }
}

function focusLast(sel) {
  const all = document.querySelectorAll(sel);
  all[all.length - 1]?.select();
}

// ---------- outputs ----------

function renderItemBases() {
  if (!S.report) return;
  const byId = Object.fromEntries(S.report.items.map((l) => [l.id, l]));
  document.querySelectorAll("[data-base]").forEach((el) => {
    const l = byId[el.dataset.base];
    el.textContent = l && l.amount.currency !== S.report.base_currency ? `≈ ${l.per_occurrence_base.display}` : "";
  });
}

function renderOutputs() {
  const r = S.report;
  if (!r) return;
  renderRateNotes();
  const m0 = r.months[0];
  const cats = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#monthName").textContent = m0.label.split(" ")[0];
  $("#incomeBig").textContent = m0.income.display;
  $("#monthsOut").textContent = r.months.length;

  const num = (m) => Math.max(parseFloat(m.amount), 0); // pixels only
  const segs = Object.entries(m0.by_category)
    .filter(([, v]) => parseFloat(v.amount) > 0)
    .map(([id, v]) => [cats[id]?.label || id, v, cats[id]?.color || "var(--accent)"]);
  if (num(m0.debt_payments) > 0) segs.push(["Debt payments", m0.debt_payments, "var(--debt)"]);
  if (num(m0.saved) > 0) segs.push(["To savings", m0.saved, "var(--save)"]);
  const tot = segs.reduce((a, s) => a + num(s[1]), 0) || 1;
  const inc = parseFloat(m0.income.amount) || 0;
  $("#bar").innerHTML = segs.map((s) => `<div style="flex-basis:${(num(s[1]) / tot) * 100}%;background:${esc(s[2])}" title="${esc(s[0])}: ${esc(s[1].display)}"></div>`).join("");
  $("#legend").innerHTML = segs.map((s) => `<div><span class="sw" style="background:${esc(s[2])}"></span>${esc(s[0])}<b>${esc(s[1].display)}${inc > 0 ? `<small>${Math.round((num(s[1]) / inc) * 100)}%</small>` : ""}</b></div>`).join("");
  const warn = $("#warn");
  warn.hidden = parseFloat(m0.left.amount) >= 0;
  if (!warn.hidden) warn.textContent = `Over budget by ${m0.left.display.replace("−", "")} this month.`;

  $("#insights").innerHTML = r.insights.map((x) => `<div class="ins ${esc(x.tone)}">${bold(x.text)}</div>`).join("") || `<p class="muted">Add income and costs to see insights.</p>`;

  const last = r.months[r.months.length - 1];
  const goal = r.goals[0];
  const debt = r.debts.find((d) => parseFloat(d.start_balance.amount) > 0);
  $("#stats").innerHTML = `
    <div><span class="muted">Savings after ${r.months.length} months</span><b>${esc(last.savings.display)}</b></div>
    <div><span class="muted">Interest paid</span><b>${esc(r.totals.total_interest_base.display)}</b></div>
    ${goal ? `<div><span class="muted">${esc(goal.name)} (${esc(goal.target.display)})</span><b>${goal.already_met ? "Already met" : goal.reached_label ? "Reached " + esc(goal.reached_label) : "Not in this window"}</b></div>` : ""}
    ${debt ? `<div><span class="muted">${esc(debt.name)} paid off</span><b>${debt.paid_off_label ? esc(debt.paid_off_label) : "Not in this window"}</b></div>` : ""}`;
  drawChart(r);

  const hasDebt = r.debts.length > 0;
  let h = `<tr><th>Month</th><th>Income</th><th>Spent</th><th>To debts</th><th>Left</th><th>Saved</th><th>Savings</th>${hasDebt ? "<th>Debt</th>" : ""}</tr>`;
  for (const m of r.months) {
    const neg = (x) => (parseFloat(x.amount) < 0 ? "neg" : "");
    h += `<tr><td>${esc(m.label)}</td><td>${esc(m.income.display)}</td><td>${esc(m.expenses.display)}</td><td>${esc(m.debt_payments.display)}</td><td class="${neg(m.left)}">${esc(m.left.display)}</td><td class="${neg(m.saved)}">${esc(m.saved.display)}</td><td class="${neg(m.savings)}">${esc(m.savings.display)}</td>${hasDebt ? `<td>${esc(m.debt_balance_base.display)}</td>` : ""}</tr>`;
  }
  $("#table").innerHTML = h;
  renderItemBases();
}

function drawChart(r) {
  const svg = $("#chart");
  const W = 640, H = 260, L = 64, R = 12, T = 14, B = 30;
  const sav = r.months.map((m) => parseFloat(m.savings.amount));
  const debt = r.months.map((m) => parseFloat(m.debt_balance_base.amount));
  const hasDebt = debt.some((v) => v > 0);
  const goals = r.goals.map((g) => parseFloat(g.target.amount));
  const all = sav.concat(hasDebt ? debt : [], goals);
  let lo = Math.min(0, ...all), hi = Math.max(100, ...all);
  const pad = (hi - lo) * 0.08; hi += pad; if (lo < 0) lo -= pad;
  const n = Math.max(r.months.length - 1, 1);
  const x = (i) => L + (i * (W - L - R)) / n;
  const y = (v) => T + ((hi - v) / (hi - lo)) * (H - T - B);
  const cur = r.base_currency;
  const fmt = (v) => new Intl.NumberFormat("en-GB", { style: "currency", currency: cur, maximumFractionDigits: 0 }).format(v);
  let g = "";
  for (let k = 0; k <= 4; k++) {
    const v = lo + ((hi - lo) * k) / 4, yy = y(v);
    g += `<line x1="${L}" x2="${W - R}" y1="${yy}" y2="${yy}" stroke="var(--line)"/><text x="${L - 6}" y="${yy + 4}" text-anchor="end">${esc(fmt(v))}</text>`;
  }
  if (lo < 0) g += `<line x1="${L}" x2="${W - R}" y1="${y(0)}" y2="${y(0)}" stroke="var(--muted)" stroke-dasharray="3 3"/>`;
  const step = Math.ceil(r.months.length / 8);
  r.months.forEach((m, i) => { if (i % step === 0) g += `<text x="${x(i)}" y="${H - 8}" text-anchor="middle">${esc(m.label.replace(/ (\d\d)(\d\d)$/, " $2"))}</text>`; });
  r.goals.forEach((gl, i) => {
    g += `<line x1="${L}" x2="${W - R}" y1="${y(goals[i])}" y2="${y(goals[i])}" stroke="var(--save)" stroke-dasharray="6 4" opacity=".55"/><text x="${W - R}" y="${y(goals[i]) - 5}" text-anchor="end">${esc(gl.name)}</text>`;
  });
  const line = (arr, c, labels) => `<polyline fill="none" stroke="${c}" stroke-width="2.5" stroke-linejoin="round" points="${arr.map((v, i) => x(i) + "," + y(v)).join(" ")}"/>` +
    arr.map((v, i) => `<circle cx="${x(i)}" cy="${y(v)}" r="3" fill="${c}"><title>${esc(r.months[i].label)}: ${esc(labels[i])}</title></circle>`).join("");
  g += line(sav, "var(--save)", r.months.map((m) => m.savings.display));
  if (hasDebt) g += line(debt, "var(--debt)", r.months.map((m) => m.debt_balance_base.display));
  svg.innerHTML = g;
}

// ---------- scenarios ----------

const WI_KINDS = [["item", "Change an item's amount"], ["rate", "Change an exchange rate"], ["extra", "Change share to debts (%)"], ["remove", "Remove an item"]];

function renderWhatIf() {
  const b = S.budget;
  const itemOpts = b.items.map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join("");
  const curOpts = b.currencies.map((c) => `<option value="${esc(c.code)}">${esc(c.code)} (now ${esc(c.per_base)})</option>`).join("");
  $("#wiOps").innerHTML = S.wi.map((o, i) => `<div class="row row-wi">
      <label class="f">Change<select data-wi="${i}" data-k="kind">${WI_KINDS.map(([k, t]) => `<option value="${k}" ${k === o.kind ? "selected" : ""}>${t}</option>`).join("")}</select></label>
      ${o.kind === "extra" ? "<span></span>" : `<label class="f">${o.kind === "rate" ? "Currency" : "Item"}<select data-wi="${i}" data-k="target">${o.kind === "rate" ? curOpts : itemOpts}</select></label>`}
      ${o.kind === "remove" ? "<span></span>" : `<label class="f">New value<input data-wi="${i}" data-k="value" inputmode="decimal" value="${esc(o.value)}"></label>`}
      ${del("del-wi", i, "change")}
    </div>`).join("") || `<p class="muted">Add a change to see what it would do.</p>`;
  S.wi.forEach((o, i) => { const t = document.querySelector(`[data-wi="${i}"][data-k="target"]`); if (t) { if (o.target) t.value = o.target; o.target = t.value; } });
}

function wiOps() {
  return S.wi.map((o) => {
    if (o.kind !== "remove" && !DEC_RE.test(String(o.value).trim())) throw new Error("Each new value must be a plain number like 800 or 92.5");
    const value = String(o.value).trim();
    switch (o.kind) {
      case "item": return { op: "set_item", target: o.target, value: { amount: value } };
      case "rate": return { op: "set_currency", target: o.target, value: { per_base: value } };
      case "extra": return { op: "set_settings", value: { extra_to_debt_pct: value } };
      case "remove": return { op: "remove_item", target: o.target };
    }
  });
}

async function previewWhatIf() {
  const out = $("#wiResult");
  try {
    const ops = wiOps();
    if (!ops.length) return;
    const res = await api("POST", `/api/budgets/${encodeURIComponent(S.id)}/what-if`, { ops });
    const before = S.report, after = res.report;
    const endB = parseFloat(before.totals.end_savings.amount), endA = parseFloat(after.totals.end_savings.amount);
    const goalTxt = after.goals.map((g) => `${esc(g.name)}: ${g.already_met ? "already met" : g.reached_label ? esc(g.reached_label) : "not reached"}`).join(" · ");
    out.innerHTML = `<div class="ins ${endA >= endB ? "good" : "warn"}">After ${after.months.length} months: <b>${esc(after.totals.end_savings.display)}</b> instead of ${esc(before.totals.end_savings.display)}. This month saves ${esc(after.months[0].saved.display)}.${goalTxt ? "<br>" + goalTxt : ""}</div>
      <div class="row-actions"><input id="wiName" placeholder="Scenario name" style="flex:1"><button type="button" data-action="save-wi">Save as scenario</button></div>`;
  } catch (e) {
    out.innerHTML = `<div class="ins warn">${esc(e.message)}</div>`;
  }
}

async function saveWhatIf() {
  const name = $("#wiName")?.value.trim();
  if (!name) { $("#wiName")?.focus(); return; }
  let ops;
  try { ops = wiOps(); } catch (e) { status(e.message, true); return; }
  S.wi = [];
  $("#wiResult").innerHTML = "";
  await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/scenarios`, { name, ops }));
}

// ---------- activity ----------

// Who made a change, as the history records it (see store.Change).
const SOURCES = { web: "You (browser)", mcp: "AI assistant", assistant: "AI assistant", cli: "Command line", "auto-refresh": "Rate refresh", earlier: "Earlier" };

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return new Date(iso).toLocaleDateString();
}

async function loadActivity() {
  if (!S.id) return;
  const box = $("#activity");
  try {
    S.history = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/history?limit=15`);
  } catch {
    return; // keep what is shown; the next refresh retries
  }
  const h = S.history;
  box.innerHTML = h.map((e, i) => `<li class="act ${esc(e.source)}">
      <span class="who">${esc(SOURCES[e.source] || e.source || "Unknown")}</span>
      <span class="what">${esc(e.summary)}</span>
      <span class="when muted" title="${esc(new Date(e.at).toLocaleString())}">r${e.revision} · ${esc(ago(e.at))}</span>
      ${i < h.length - 1 ? `<button type="button" class="linkish" data-action="undo" data-rev="${e.revision}" data-i="${i}">Undo</button>` : "<span></span>"}
    </li>`).join("") || `<li class="muted">No changes yet.</li>`;
}

async function undoChange(i, el) {
  const rev = Number(el.dataset.rev);
  const later = S.history.slice(0, i);
  if (later.length && !confirm(`This also undoes ${later.length} later change${later.length === 1 ? "" : "s"}:\n\n${later.map((e) => `• ${e.summary}`).join("\n")}\n\nUndo them all?`)) return;
  await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/undo`, { change: rev, force: later.length > 0 }));
}

async function loadCompare() {
  const table = $("#compare");
  if (!S.id) return;
  try {
    const rows = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/compare`);
    if (rows.length < 2) { table.innerHTML = ""; return; }
    let h = `<tr><th>Scenario</th><th>This month</th><th>End savings</th><th>Δ</th><th>Interest</th><th>Goals</th><th></th></tr>`;
    for (const o of rows) {
      if (o.error) { h += `<tr><td>${esc(o.name)}</td><td colspan="5" class="neg">${esc(o.error)}</td><td>${scenarioButtons(o)}</td></tr>`; continue; }
      const d = o.delta_end_savings;
      const goals = (o.goals || []).map((g) => g.already_met ? "met" : g.reached_label || "—").join(", ");
      h += `<tr><td>${esc(o.name)}</td><td>${esc(o.first_month_saved.display)}</td><td>${esc(o.end_savings.display)}</td><td class="${d ? (parseFloat(d.amount) < 0 ? "neg" : "pos") : ""}">${d ? esc(d.display) : ""}</td><td>${esc(o.total_interest_base.display)}</td><td>${esc(goals)}</td><td>${o.id === "baseline" ? "" : scenarioButtons(o)}</td></tr>`;
    }
    table.innerHTML = h;
  } catch { table.innerHTML = ""; }
}
const scenarioButtons = (o) => `<button type="button" class="linkish" data-action="adopt-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}">Adopt</button> <button type="button" class="linkish" data-action="del-scenario" data-sid="${esc(o.id)}" aria-label="Delete ${esc(o.name)}">✕</button>`;

// ---------- new / import / export / delete ----------

function openNewDialog() {
  const sel = $("#templateSel");
  const list = S.meta?.templates || [{ name: "blank" }];
  sel.innerHTML = list.map((t) => `<option value="${esc(t.name)}" ${t.name === "blank" ? "selected" : ""}>${esc(t.name)}${t.description ? " — " + esc(t.description) : ""}</option>`).join("");
  $("#newForm").reset();
  $("#newDialog").showModal();
}

async function createFromDialog() {
  const f = new FormData($("#newForm"));
  try {
    const v = await api("POST", "/api/budgets", { id: f.get("id"), name: f.get("name") || "", template: f.get("template") });
    await loadList();
    await open(v.id);
  } catch (e) { status(e.message, true); }
}

function exportJSON() {
  const blob = new Blob([JSON.stringify(S.budget, null, 2)], { type: "application/json" });
  const a = Object.assign(document.createElement("a"), { href: URL.createObjectURL(blob), download: `${S.id}.json` });
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

async function importJSON(file) {
  try {
    const budget = JSON.parse(await file.text());
    const id = prompt("Id for the imported budget:", slug(budget.name || file.name.replace(/\.json$/, "")));
    if (!id) return;
    await api("POST", "/api/budgets", { id, budget });
    await loadList();
    await open(id);
  } catch (e) {
    status(`Import failed: ${e.data?.issues ? e.data.issues.map((i) => `${i.path}: ${i.message}`).join("; ") : e.message}`, true);
  }
}

async function deleteBudget() {
  if (!S.id || !confirm(`Delete “${S.budget.name}” permanently?`)) return;
  try {
    await api("DELETE", `/api/budgets/${encodeURIComponent(S.id)}`);
    S.id = null;
    await loadList();
    if (S.list.length) await open(S.list[0].id);
    else status("No budgets.");
  } catch (e) { status(e.message, true); }
}

// ---------- wiring ----------

document.addEventListener("input", (e) => {
  if (e.target.dataset.path) onEditorInput(e);
  if (e.target.dataset.wi !== undefined && e.target.dataset.k === "value") S.wi[+e.target.dataset.wi].value = e.target.value;
});
document.addEventListener("change", (e) => {
  if (e.target.dataset.path && (e.target.tagName === "SELECT" || e.target.type === "month" || e.target.type === "color")) onEditorInput(e);
  if (e.target.dataset.wi !== undefined && e.target.tagName === "SELECT") {
    const o = S.wi[+e.target.dataset.wi];
    o[e.target.dataset.k] = e.target.value;
    if (e.target.dataset.k === "kind") { o.target = ""; renderWhatIf(); }
  }
});
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-action]");
  if (!btn) return;
  ACTIONS[btn.dataset.action]?.(Number(btn.dataset.i), btn);
});
$("#picker").addEventListener("change", (e) => open(e.target.value));
$("#newBtn").addEventListener("click", openNewDialog);
$("#newDialog").addEventListener("close", () => { if ($("#newDialog").returnValue === "ok") createFromDialog(); });
$("#exportBtn").addEventListener("click", exportJSON);
$("#importFile").addEventListener("change", (e) => { if (e.target.files[0]) importJSON(e.target.files[0]); e.target.value = ""; });
$("#deleteBtn").addEventListener("click", deleteBudget);
window.addEventListener("beforeunload", (e) => { if (S.pending || S.saving) e.preventDefault(); });

(async function init() {
  try {
    S.meta = await api("GET", "/api/meta");
    S.prefs = await api("GET", "/api/settings").catch(() => S.prefs);
    await loadList();
    if (!S.list.length) { status("No budgets yet."); return; }
    let want = decodeURIComponent(location.hash.slice(1));
    if (!want) try { want = localStorage.getItem("bbbudget.last") || ""; } catch {}
    await open(S.list.some((b) => b.id === want) ? want : S.list[0].id);
  } catch (e) {
    status(`Could not reach the server: ${e.message}`, true);
  }
  setInterval(poll, POLL_MS);
})();
