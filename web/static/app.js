// Budget planner UI. The server owns every number: this file edits the budget
// document (amounts stay exact decimal strings) and renders the server's
// report. The only floats here are pixel positions on the chart.
//
// Every visible string goes through t() (see i18n.js and i18n/*.json), and
// money through money(), which formats the server's exact amounts.

import { t, loadLocale, money, moneyAxis, number, toInput, parseDecimal, monthName, translatePage, locale } from "./i18n.js";

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const bold = (s) => esc(s).replace(/\*\*(.+?)\*\*/g, "<b>$1</b>");
const SAVE_DELAY = 600;
const POLL_MS = 3000;

const S = {
  meta: null,
  prefs: { fetch_rates: true, default_rate_source: "auto" }, // app settings (settings.html)
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
  if (S.chatBudget !== id) { S.chatSession = ""; S.chatBudget = id; $("#chatLog").replaceChildren(); }
  status(t("status.saved", { revision: S.revision }));
  loadCompare();
  loadActivity();
  loadNow();
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
  status(t("status.unsaved"));
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
    else status(t("status.saved", { revision: S.revision }));
    S.notice = null;
    loadCompare();
    loadActivity();
  loadNow();
  } catch (e) {
    if (e.status === 422) {
      showIssues(e.data?.issues || [{ path: "", message: e.message }]);
      status(t("status.fix_fields"), true);
    } else if (e.status === 409) {
      status(t("status.conflict"), true);
      await open(S.id);
    } else {
      S.pending = true;
      status(t("status.retrying", { error: e.message }), true);
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
      status(t("status.updated_elsewhere", { revision: S.revision }));
      loadCompare();
      loadActivity();
  loadNow();
    }
  } catch {}
}

function showIssues(issues) {
  document.querySelectorAll(".invalid").forEach((el) => el.classList.remove("invalid"));
  const box = $("#issues");
  box.hidden = issues.length === 0;
  if (!issues.length) return;
  box.innerHTML = `<b>${esc(t("issues.title"))}</b><ul>${issues.map((i) => `<li>${esc(i.path)}: ${esc(i.message)}</li>`).join("")}</ul>`;
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
  } value="${esc(type === "decimal" ? toInput(v) : v ?? "")}" ${attrs}></label>`;
};
const select = (label, path, options, { structural = false, cls = "" } = {}) => {
  const v = getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<select data-path="${esc(path)}" data-kind="text" ${structural ? "data-structural" : ""}>${options
    .map(([val, text]) => `<option value="${esc(val)}" ${String(val) === String(v) ? "selected" : ""}>${esc(text)}</option>`)
    .join("")}</select></label>`;
};
const del = (action, i, what) => `<button type="button" class="del" data-action="${action}" data-i="${i}" aria-label="${esc(t("remove", { what }))}">✕</button>`;

function currencyOptions() {
  return [S.budget.base_currency, ...S.budget.currencies.map((c) => c.code)].map((c) => [c, c]);
}

const RATE_SOURCES = () => ["manual", "auto", "ecb", "currency-api", "coingecko"].map((k) => [k, t(`source_opt.${k}`)]);
const isManual = (c) => !c.source || c.source === "manual";

const EVERY = () => [0, 1, 2, 3, 6, 12].map((n) => [n, t(`every.${n}`)]);

function renderEditors() {
  const b = S.budget;
  $("#title").textContent = b.name;
  const crypto = new Set(S.meta?.crypto || []);
  const strategies = (S.meta?.strategies || ["avalanche", "snowball", "listed"]).map((s) => [s, t(`strategy.${s}`)]);
  $("#settings").innerHTML = [
    field(t("field.name"), "name"),
    select(t("field.base_currency"), "base_currency", (S.meta?.currencies || [b.base_currency]).filter((c) => !crypto.has(c)).map((c) => [c, c]), { structural: true }),
    field(t("field.first_month"), "start_month", { type: "month" }),
    select(t("field.months"), "months", [[6, "6"], [12, "12"], [24, "24"], [36, "36"], [60, "60"]].concat(
      [6, 12, 24, 36, 60].includes(b.months) ? [] : [[b.months, String(b.months)]])),
    field(t("field.savings_now", { currency: b.base_currency }), "savings_start", { type: "decimal" }),
    field(t("field.extra_pct"), "extra_to_debt_pct", { type: "decimal" }),
    select(t("field.strategy"), "debt_strategy", strategies),
  ].join("");

  const allCodes = (S.meta?.currencies || []).filter((c) => c !== b.base_currency);
  const codeOpts = allCodes.map((x) => [x, crypto.has(x) ? t("currencies.crypto", { code: x }) : x]);
  $("#currencies").innerHTML = b.currencies.map((c, i) => `<div class="row row-cur">
      ${select(t("field.code"), `currencies.${i}.code`, codeOpts, { structural: true })}
      ${select(t("field.rate_from"), `currencies.${i}.source`, RATE_SOURCES(), { structural: true })}
      ${field(t("field.per_base", { currency: b.base_currency }), `currencies.${i}.per_base`, { type: "decimal" })}
      ${field(t("field.payment_loss"), `currencies.${i}.transfer_fee_pct`, { type: "decimal" })}
      ${field(t("field.cashout_loss"), `currencies.${i}.cash_out_fee_pct`, { type: "decimal", attrs: 'placeholder="0"' })}
      ${del("del-currency", i, c.code)}
      <p class="rate-note muted" data-rate="${esc(c.code)}"></p>
    </div>`).join("") || `<p class="muted">${esc(t("currencies.only_base", { currency: b.base_currency }))}</p>`;
  $("#refreshRates").hidden = !b.currencies.length || !S.prefs.fetch_rates;

  const cats = b.categories.map((c) => [c.id, c.kind === "income" ? t("category.income_suffix", { label: c.label }) : c.label]);
  $("#lines").innerHTML = b.lines.map((l, i) => {
    const color = b.categories.find((c) => c.id === l.category)?.color || "var(--line)";
    const sch = l.schedule;
    return `<div class="row row-line" style="box-shadow:inset 3px 0 0 ${esc(color)}">
      ${field(t("field.name"), `lines.${i}.name`, { cls: "name" })}
      ${field(t("field.amount"), `lines.${i}.amount`, { type: "decimal" })}
      ${select(t("field.currency"), `lines.${i}.currency`, currencyOptions())}
      ${select(t("field.category"), `lines.${i}.category`, cats, { structural: true })}
      ${select(t("field.repeats"), `lines.${i}.schedule.every`, EVERY(), { structural: true })}
      <label class="f">${esc(t(sch.every === 0 ? "field.in" : "field.from"))}<input type="month" data-path="lines.${i}.schedule.start" data-kind="monthidx" value="${monthKey(sch.start)}"></label>
      ${sch.every === 0 ? "<span></span>" :
        `<label class="f">${esc(t("field.until"))}<input type="month" data-path="lines.${i}.schedule.end" data-kind="monthidx-opt" value="${sch.end == null ? "" : monthKey(sch.end)}"></label>`}
      <span class="base" data-base="${esc(l.id)}"></span>
      ${del("del-line", i, l.name)}
    </div>`;
  }).join("") || `<p class="muted">${esc(t("lines.empty"))}</p>`;

  $("#debts").innerHTML = b.debts.map((d, i) => `<div class="row row-debt" style="box-shadow:inset 3px 0 0 var(--debt)">
      ${field(t("field.name"), `debts.${i}.name`)}
      ${select(t("field.currency"), `debts.${i}.currency`, currencyOptions())}
      ${field(t("field.balance"), `debts.${i}.balance`, { type: "decimal" })}
      ${field(t("field.apr"), `debts.${i}.apr_pct`, { type: "decimal" })}
      ${field(t("field.min_payment"), `debts.${i}.min_payment`, { type: "decimal" })}
      ${del("del-debt", i, d.name)}
    </div>`).join("") || `<p class="muted">${esc(t("debts.empty"))}</p>`;

  $("#goals").innerHTML = b.goals.map((g, i) => `<div class="row row-goal" style="box-shadow:inset 3px 0 0 var(--save)">
      ${field(t("field.goal"), `goals.${i}.name`)}
      ${field(t("field.target", { currency: b.base_currency }), `goals.${i}.target`, { type: "decimal" })}
      ${del("del-goal", i, g.name)}
    </div>`).join("") || `<p class="muted">${esc(t("goals.empty"))}</p>`;

  $("#categories").innerHTML = b.categories.map((c, i) => `<div class="row row-cat">
      ${field(t("field.label"), `categories.${i}.label`)}
      ${select(t("field.kind"), `categories.${i}.kind`, [["expense", t("kind.expense")], ["income", t("kind.income")]], { structural: true })}
      <label class="f">${esc(t("field.colour"))}<input type="color" data-path="categories.${i}.color" data-kind="text" value="${esc(c.color || "#888888")}"></label>
      ${del("del-category", i, c.label)}
    </div>`).join("");

  renderWhatIf();
  renderLineBases();
  renderRateNotes();
}

function onEditorInput(e) {
  const el = e.target;
  const path = el.dataset.path;
  if (!path || !S.budget) return;
  let v = el.value;
  switch (el.dataset.kind) {
    case "decimal":
      v = parseDecimal(v); // "12,50" in a decimal-comma locale is 12.50, never 1250
      el.classList.toggle("invalid", v === null);
      if (v === null) return; // keep editing; never send a malformed amount
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
  status(t("rate.fetching", { code: c.code }));
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
    S.notice = t("rate.fetch_failed", { code, error: e.message });
    edited({ structural: true });
    status(S.notice, true);
  } finally {
    S.fetching--;
  }
}

// refreshAll fetches every non-manual rate on the server and saves them.
async function refreshAll() {
  if (!S.prefs.fetch_rates) {
    status(t("rates.off"), true);
    return;
  }
  if (!S.budget.currencies.some((c) => !isManual(c))) {
    status(t("rates.all_manual"), true);
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
  const changed = ups.filter((u) => !u.error).map((u) => (u.old === u.new ? t("rates.unchanged", { code: u.code }) : t("rates.changed", { code: u.code, old: number(u.old), new: number(u.new) })));
  const failed = ups.filter((u) => u.error).map((u) => u.code);
  if (!changed.length) return;
  const text = t("rates.updated", { list: changed.join(", ") });
  status(failed.length ? t("rates.failed", { text, list: failed.join(", ") }) : text, failed.length > 0);
}

function renderRateNotes() {
  if (!S.report || !S.budget) return;
  const unit = Object.fromEntries((S.report.currencies || []).map((c) => [c.code, c]));
  const exact = (m) => new Intl.NumberFormat(locale(), { style: "currency", currency: m.currency, minimumFractionDigits: 2 }).format(m.amount);
  document.querySelectorAll("[data-rate]").forEach((el) => {
    const c = S.budget.currencies.find((x) => x.code === el.dataset.rate);
    const u = unit[el.dataset.rate];
    if (!c) return;
    const worth = u ? `${number(u.units)} ${c.code} = ${exact(u.base_value)}` : "";
    const src = ({ ecb: "ECB", coingecko: "CoinGecko" })[c.rate_from] || c.rate_from;
    const from = isManual(c) ? t("rate.typed") : c.rate_date ? t("rate.of", { source: src, date: c.rate_date }) : t("rate.not_fetched");
    el.textContent = [worth, from].filter(Boolean).join(" · ");
  });
}

const ACTIONS = {
  "add-line"() {
    const b = S.budget;
    const cat = b.categories.find((c) => c.kind === "expense") || b.categories[0];
    const name = t("lines.new");
    b.lines.push({ id: newId(name, b.lines), name, amount: "0", currency: b.base_currency, category: cat.id, schedule: { start: 0, every: 1 } });
    edited({ structural: true });
    focusLast("#lines input[data-path$='.name']");
  },
  "del-line"(i) { S.budget.lines.splice(i, 1); edited({ structural: true }); },
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
    const name = t("debts.new");
    b.debts.push({ id: newId(name, b.debts), name, currency: b.base_currency, balance: "0", apr_pct: "0", min_payment: "0" });
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
    const label = t("categories.new");
    b.categories.push({ id: newId(label, b.categories), label, kind: "expense", color: "#7a6ff0" });
    edited({ structural: true });
  },
  "del-category"(i) {
    const c = S.budget.categories[i];
    if (S.budget.lines.some((l) => l.category === c.id)) { status(t("categories.in_use", { label: c.label }), true); return; }
    S.budget.categories.splice(i, 1);
    edited({ structural: true });
  },
  new() { openNewDialog(); },
  "add-wi"() { S.wi.push({ kind: "line", target: S.budget.lines[0]?.id || "", value: "" }); renderWhatIf(); },
  "del-wi"(i) { S.wi.splice(i, 1); renderWhatIf(); },
  "preview-wi"() { previewWhatIf(); },
  "save-wi"() { saveWhatIf(); },
  undo(i, el) { undoChange(i, el); },
  async "adopt-scenario"(i, el) {
    if (!confirm(t("scenarios.adopt_confirm", { name: el.dataset.name }))) return;
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

function renderLineBases() {
  if (!S.report) return;
  const byId = Object.fromEntries(S.report.lines.map((l) => [l.id, l]));
  document.querySelectorAll("[data-base]").forEach((el) => {
    const l = byId[el.dataset.base];
    el.textContent = l && l.amount.currency !== S.report.base_currency ? `≈ ${money(l.per_occurrence_base)}` : "";
  });
}

function renderOutputs() {
  const r = S.report;
  if (!r) return;
  renderRateNotes();
  const m0 = r.months[0];
  const cats = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#h-month").textContent = t("month.heading", { month: monthName(m0.month) });
  $("#incomeBig").textContent = money(m0.income);
  $("#h-proj").textContent = t("projection.heading", { count: r.months.length });

  const num = (m) => Math.max(parseFloat(m.amount), 0); // pixels only
  const segs = Object.entries(m0.by_category)
    .filter(([, v]) => parseFloat(v.amount) > 0)
    .map(([id, v]) => [cats[id]?.label || id, v, cats[id]?.color || "var(--accent)"]);
  if (num(m0.debt_payments) > 0) segs.push([t("split.debt"), m0.debt_payments, "var(--debt)"]);
  if (num(m0.saved) > 0) segs.push([t("split.savings"), m0.saved, "var(--save)"]);
  const tot = segs.reduce((a, s) => a + num(s[1]), 0) || 1;
  const inc = parseFloat(m0.income.amount) || 0;
  $("#bar").innerHTML = segs.map((s) => `<div style="flex-basis:${(num(s[1]) / tot) * 100}%;background:${esc(s[2])}" title="${esc(s[0])}: ${esc(money(s[1]))}"></div>`).join("");
  $("#legend").innerHTML = segs.map((s) => `<div><span class="sw" style="background:${esc(s[2])}"></span>${esc(s[0])}<b>${esc(money(s[1]))}${inc > 0 ? `<small>${Math.round((num(s[1]) / inc) * 100)}%</small>` : ""}</b></div>`).join("");
  const warn = $("#warn");
  warn.hidden = parseFloat(m0.left.amount) >= 0;
  if (!warn.hidden) warn.textContent = t("split.over", { amount: money({ ...m0.left, amount: m0.left.amount.replace("-", "") }) });

  $("#insights").innerHTML = r.insights.map((x) => `<div class="ins ${esc(x.tone)}">${bold(x.text)}</div>`).join("") || `<p class="muted">${esc(t("insights.empty"))}</p>`;

  const last = r.months[r.months.length - 1];
  const goal = r.goals[0];
  const debt = r.debts.find((d) => parseFloat(d.start_balance.amount) > 0);
  $("#stats").innerHTML = `
    <div><span class="muted">${esc(t("stats.savings_after", { count: r.months.length }))}</span><b>${esc(money(last.savings))}</b></div>
    <div><span class="muted">${esc(t("stats.interest"))}</span><b>${esc(money(r.totals.total_interest_base))}</b></div>
    ${goal ? `<div><span class="muted">${esc(goal.name)} (${esc(money(goal.target))})</span><b>${esc(goal.already_met ? t("stats.already_met") : goal.reached_label ? t("stats.reached", { month: goal.reached_label }) : t("stats.not_in_window"))}</b></div>` : ""}
    ${debt ? `<div><span class="muted">${esc(t("stats.paid_off", { name: debt.name }))}</span><b>${esc(debt.paid_off_label || t("stats.not_in_window"))}</b></div>` : ""}`;
  drawChart(r);

  const hasDebt = r.debts.length > 0;
  const th = (k) => `<th>${esc(t(k))}</th>`;
  let h = `<tr>${["table.month", "table.income", "table.spent", "table.to_debts", "table.left", "table.saved", "table.savings"].map(th).join("")}${hasDebt ? th("table.debt") : ""}</tr>`;
  for (const m of r.months) {
    const neg = (x) => (parseFloat(x.amount) < 0 ? "neg" : "");
    h += `<tr><td>${esc(m.label)}</td><td>${esc(money(m.income))}</td><td>${esc(money(m.expenses))}</td><td>${esc(money(m.debt_payments))}</td><td class="${neg(m.left)}">${esc(money(m.left))}</td><td class="${neg(m.saved)}">${esc(money(m.saved))}</td><td class="${neg(m.savings)}">${esc(money(m.savings))}</td>${hasDebt ? `<td>${esc(money(m.debt_balance_base))}</td>` : ""}</tr>`;
  }
  $("#table").innerHTML = h;
  renderLineBases();
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
  const fmt = (v) => moneyAxis(v, cur); // axis ticks: pixels, not money
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
  g += line(sav, "var(--save)", r.months.map((m) => money(m.savings)));
  if (hasDebt) g += line(debt, "var(--debt)", r.months.map((m) => money(m.debt_balance_base)));
  svg.innerHTML = g;
}

// ---------- scenarios ----------

const WI_KINDS = () => ["line", "rate", "extra", "remove"].map((k) => [k, t(`scenarios.kind.${k}`)]);

function renderWhatIf() {
  const b = S.budget;
  const lineOpts = b.lines.map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join("");
  const curOpts = b.currencies.map((c) => `<option value="${esc(c.code)}">${esc(t("scenarios.now", { code: c.code, rate: number(c.per_base) }))}</option>`).join("");
  $("#wiOps").innerHTML = S.wi.map((o, i) => `<div class="row row-wi">
      <label class="f">${esc(t("scenarios.change"))}<select data-wi="${i}" data-k="kind">${WI_KINDS().map(([k, t]) => `<option value="${k}" ${k === o.kind ? "selected" : ""}>${t}</option>`).join("")}</select></label>
      ${o.kind === "extra" ? "<span></span>" : `<label class="f">${esc(t(o.kind === "rate" ? "scenarios.currency" : "scenarios.line"))}<select data-wi="${i}" data-k="target">${o.kind === "rate" ? curOpts : lineOpts}</select></label>`}
      ${o.kind === "remove" ? "<span></span>" : `<label class="f">${esc(t("scenarios.new_value"))}<input data-wi="${i}" data-k="value" inputmode="decimal" value="${esc(o.value)}"></label>`}
      ${del("del-wi", i, t("scenarios.a_change"))}
    </div>`).join("") || `<p class="muted">${esc(t("scenarios.empty"))}</p>`;
  S.wi.forEach((o, i) => { const t = document.querySelector(`[data-wi="${i}"][data-k="target"]`); if (t) { if (o.target) t.value = o.target; o.target = t.value; } });
}

function wiOps() {
  return S.wi.map((o) => {
    const value = parseDecimal(o.value);
    if (o.kind !== "remove" && value === null) throw new Error(t("scenarios.bad_value"));
    switch (o.kind) {
      case "line": return { op: "set_line", target: o.target, value: { amount: value } };
      case "rate": return { op: "set_currency", target: o.target, value: { per_base: value } };
      case "extra": return { op: "set_settings", value: { extra_to_debt_pct: value } };
      case "remove": return { op: "remove_line", target: o.target };
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
    const goalTxt = after.goals.map((g) => `${esc(g.name)}: ${esc(g.already_met ? t("scenarios.goal_met") : g.reached_label || t("scenarios.goal_not"))}`).join(" · ");
    const result = t("scenarios.result", { count: after.months.length, after: esc(money(after.totals.end_savings)), before: esc(money(before.totals.end_savings)), saved: esc(money(after.months[0].saved)) });
    out.innerHTML = `<div class="ins ${endA >= endB ? "good" : "warn"}">${result}${goalTxt ? "<br>" + goalTxt : ""}</div>
      <div class="row-actions"><input id="wiName" placeholder="${esc(t("scenarios.name"))}" style="flex:1"><button type="button" data-action="save-wi">${esc(t("scenarios.save"))}</button></div>`;
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

// ---------- assistant ----------

async function initChat() {
  let st;
  try { st = await api("GET", "/api/assistant"); } catch { return; }
  $("#chatSection").hidden = !st.enabled;
  if (!st.enabled) return;
  $("#chatPrivacy").textContent = st.local ? t("chat.privacy_local", { model: st.model }) : t("chat.privacy_hosted", { model: st.model, provider: st.provider === "anthropic" ? "Anthropic" : st.provider });
}

function chatLine(cls, text, html = false) {
  const el = document.createElement("div");
  el.className = `chat-msg ${cls}`;
  if (html) el.innerHTML = text; else el.textContent = text;
  $("#chatLog").append(el);
  el.scrollIntoView({ block: "nearest" });
  return el;
}

// sendChat posts a message and reads the server-sent events as they come:
// the assistant's text, the tools it uses, and "changed" when it wrote to
// the budget (the page reloads it at the end).
async function sendChat(text) {
  if (S.timer) { clearTimeout(S.timer); S.timer = null; }
  if (S.pending) await save(); // the assistant must see what is on screen
  chatLine("me", text);
  const working = chatLine("note", t("chat.working"));
  $("#chatSend").disabled = true;
  let changed = false;
  try {
    const res = await fetch(`/api/budgets/${encodeURIComponent(S.id)}/chat`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Requested-With": "bbbudget" },
      body: JSON.stringify({ session: S.chatSession || "", message: text }),
    });
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || res.statusText);
    }
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const line = buf.slice(0, i).replace(/^data: /, "");
        buf = buf.slice(i + 2);
        if (!line.trim()) continue;
        const e = JSON.parse(line);
        if (e.type === "text") chatLine("bot", bold(e.text), true);
        else if (e.type === "tool") chatLine("tool", e.detail ? `${e.tool}: ${e.detail}` : e.tool);
        else if (e.type === "changed") changed = true;
        else if (e.type === "session") S.chatSession = e.text;
        else if (e.type === "error") chatLine("err", t("chat.error", { error: e.text }));
      }
    }
  } catch (e) {
    chatLine("err", t("chat.error", { error: e.message }));
  } finally {
    working.remove();
    $("#chatSend").disabled = false;
    if (changed) await open(S.id);
  }
}

// ---------- this month: plan versus actual ----------

async function loadNow() {
  if (!S.id) return;
  const id = encodeURIComponent(S.id);
  let st, txs;
  try {
    [st, txs] = await Promise.all([api("GET", `/api/budgets/${id}/status`), api("GET", `/api/budgets/${id}/transactions?limit=8`)]);
  } catch {
    $("#nowSection").hidden = true; // e.g. today is outside the budget's window
    return;
  }
  $("#nowSection").hidden = false;
  $("#h-now").textContent = t("now.heading", { month: monthName(st.month) });
  $("#nowSub").textContent = st.transactions
    ? t("now.sub", { spent: money(st.spent), planned: money(st.planned_spending), day: st.elapsed, days: st.days })
    : t("now.empty");
  $("#nowFlags").innerHTML = st.flags.map((f) => `<div class="ins ${esc(f.tone)}">${bold(f.text)}</div>`).join("");
  const cats = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#nowCats").innerHTML = st.categories.filter((c) => c.kind !== "income").map((c) => {
    const planned = parseFloat(c.planned.amount), actual = parseFloat(c.actual.amount); // bar widths only
    const pct = planned > 0 ? Math.min(100, (actual / planned) * 100) : actual > 0 ? 100 : 0;
    const color = c.state === "over" || c.state === "unplanned" ? "var(--neg)" : cats[c.category]?.color || "var(--accent)";
    const badge = c.state === "ok" ? "" : `<span class="badge ${esc(c.state)}">${esc(t(`now.state.${c.state}`))}</span>`;
    return `<div class="now-row"><div class="now-head"><span>${esc(c.label)} ${badge}</span><span class="muted">${esc(t("now.of", { actual: money(c.actual), planned: money(c.planned) }))}</span></div>
      <div class="now-bar"><div style="width:${pct}%;background:${esc(color)}"></div></div></div>`;
  }).join("");
  $("#nowTx").innerHTML = txs.length
    ? txs.map((x) => `<tr><td>${esc(x.date)}</td><td>${esc(cats[x.category]?.label || x.category)}</td><td>${esc(money({ amount: x.amount, currency: x.currency }))}</td><td class="muted">${esc(x.note || "")}</td></tr>`).join("")
    : `<tr><td class="muted">${esc(t("now.none_logged"))}</td></tr>`;
}

// ---------- activity ----------

// Who made a change, as the history records it (see store.Change).
const sourceLabel = (src) => { const k = `source.${src || "unknown"}`, v = t(k); return v === k ? src : v; };

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  const rtf = new Intl.RelativeTimeFormat(locale(), { numeric: "auto", style: "short" });
  if (s < 60) return t("activity.just_now");
  if (s < 3600) return rtf.format(-Math.floor(s / 60), "minute");
  if (s < 86400) return rtf.format(-Math.floor(s / 3600), "hour");
  return new Date(iso).toLocaleDateString(locale());
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
      <span class="who">${esc(sourceLabel(e.source))}</span>
      <span class="what">${esc(e.summary)}</span>
      <span class="when muted" title="${esc(new Date(e.at).toLocaleString(locale()))}">r${e.revision} · ${esc(ago(e.at))}</span>
      ${i < h.length - 1 ? `<button type="button" class="linkish" data-action="undo" data-rev="${e.revision}" data-i="${i}">${esc(t("activity.undo"))}</button>` : "<span></span>"}
    </li>`).join("") || `<li class="muted">${esc(t("activity.empty"))}</li>`;
}

async function undoChange(i, el) {
  const rev = Number(el.dataset.rev);
  const later = S.history.slice(0, i);
  if (later.length && !confirm(t("activity.undo_confirm", { count: later.length, list: later.map((e) => `• ${e.summary}`).join("\n") }))) return;
  await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/undo`, { change: rev, force: later.length > 0 }));
}

async function loadCompare() {
  const table = $("#compare");
  if (!S.id) return;
  try {
    const rows = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/compare`);
    if (rows.length < 2) { table.innerHTML = ""; return; }
    let h = `<tr>${["compare.scenario", "compare.this_month", "compare.end_savings", "compare.delta", "compare.interest", "compare.goals"].map((k) => `<th>${esc(t(k))}</th>`).join("")}<th></th></tr>`;
    for (const o of rows) {
      if (o.error) { h += `<tr><td>${esc(o.name)}</td><td colspan="5" class="neg">${esc(o.error)}</td><td>${scenarioButtons(o)}</td></tr>`; continue; }
      const d = o.delta_end_savings;
      const goals = (o.goals || []).map((g) => g.already_met ? t("compare.met") : g.reached_label || "—").join(", ");
      h += `<tr><td>${esc(o.name)}</td><td>${esc(money(o.first_month_saved))}</td><td>${esc(money(o.end_savings))}</td><td class="${d ? (parseFloat(d.amount) < 0 ? "neg" : "pos") : ""}">${d ? esc(money(d)) : ""}</td><td>${esc(money(o.total_interest_base))}</td><td>${esc(goals)}</td><td>${o.id === "baseline" ? "" : scenarioButtons(o)}</td></tr>`;
    }
    table.innerHTML = h;
  } catch { table.innerHTML = ""; }
}
const scenarioButtons = (o) => `<button type="button" class="linkish" data-action="adopt-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}">${esc(t("scenarios.adopt"))}</button> <button type="button" class="linkish" data-action="del-scenario" data-sid="${esc(o.id)}" aria-label="${esc(t("scenarios.delete", { name: o.name }))}">✕</button>`;

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
    const id = prompt(t("import.id"), slug(budget.name || file.name.replace(/\.json$/, "")));
    if (!id) return;
    await api("POST", "/api/budgets", { id, budget });
    await loadList();
    await open(id);
  } catch (e) {
    status(t("import.failed", { error: e.data?.issues ? e.data.issues.map((i) => `${i.path}: ${i.message}`).join("; ") : e.message }), true);
  }
}

async function deleteBudget() {
  if (!S.id || !confirm(t("delete.confirm", { name: S.budget.name }))) return;
  try {
    await api("DELETE", `/api/budgets/${encodeURIComponent(S.id)}`);
    S.id = null;
    await loadList();
    if (S.list.length) await open(S.list[0].id);
    else status(t("status.no_budgets"));
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
$("#chatForm").addEventListener("submit", (e) => {
  e.preventDefault();
  const text = $("#chatInput").value.trim();
  if (!text || $("#chatSend").disabled) return;
  $("#chatInput").value = "";
  sendChat(text);
});
$("#chatInput").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); $("#chatForm").requestSubmit(); }
});
$("#newBtn").addEventListener("click", openNewDialog);
$("#newDialog").addEventListener("close", () => { if ($("#newDialog").returnValue === "ok") createFromDialog(); });
$("#exportBtn").addEventListener("click", exportJSON);
$("#importFile").addEventListener("change", (e) => { if (e.target.files[0]) importJSON(e.target.files[0]); e.target.value = ""; });
$("#deleteBtn").addEventListener("click", deleteBudget);
window.addEventListener("beforeunload", (e) => { if (S.pending || S.saving) e.preventDefault(); });

(async function init() {
  try {
    S.meta = await api("GET", "/api/meta");
    await loadLocale(S.meta.locale, S.meta.crypto);
    $("#license").innerHTML = esc(t("app.license", { license: "\u0001" })).replace("\u0001",
      `<a href="https://www.gnu.org/licenses/agpl-3.0.html" target="_blank" rel="noopener noreferrer">GNU AGPL v3</a>`);
    S.prefs = await api("GET", "/api/settings").catch(() => S.prefs);
    initChat();
    await loadList();
    if (!S.list.length) { status(t("status.no_budgets_yet")); return; }
    let want = decodeURIComponent(location.hash.slice(1));
    if (!want) try { want = localStorage.getItem("bbbudget.last") || ""; } catch {}
    await open(S.list.some((b) => b.id === want) ? want : S.list[0].id);
  } catch (e) {
    status(t("status.unreachable", { error: e.message }), true);
  }
  setInterval(poll, POLL_MS);
})();
