// Budget planner UI. The server owns every number: this file edits the budget
// document (amounts stay exact decimal strings) and renders the server's
// report. The only floats here are pixel positions and display-only formatting.
//
// Layout: five views (Overview, Lines, Plan, Scenarios, Setup) behind one tab
// list, shown as top tabs with a summary rail on wide screens and as a bottom
// tab bar with a pinned mini-summary on phones (the shape a native app would
// take). The route is #<budget>/<view>.

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const bold = (s) => esc(s).replace(/\*\*(.+?)\*\*/g, "<b>$1</b>");
const DEC_RE = /^[+-]?\d+(\.\d+)?$/;
const SAVE_DELAY = 600;
const POLL_MS = 3000;
const VIEWS = ["overview", "lines", "plan", "scenarios", "setup"];
const UNDO_MS = 6000;

const S = {
  meta: null,
  prefs: { fetch_rates: true, default_rate_source: "auto" }, // app settings (settings.html)
  list: [],
  id: null,
  view: "overview",
  revision: 0,
  budget: null,
  report: null,
  saving: false,
  pending: false, // local edits not yet sent
  fetching: 0, // rate fetches in flight: saving waits so a placeholder rate is never stored
  notice: null, // an error to keep showing after the next save, instead of "saved"
  timer: null,
  sheet: null, // index of the line open in the editor sheet
  allInsights: false,
  lastSummary: "",
  wi: [], // what-if draft ops
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

// ---------- months, ids, amounts ----------

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

// fmtAmount pads a fiat amount to its currency's decimals for display
// ("45.5" → "45.50"). The stored value is untouched; crypto stays as typed.
function fmtAmount(v, code) {
  const s = String(v ?? "");
  const digits = S.meta?.digits?.[code];
  if (!DEC_RE.test(s) || digits == null || digits > 3) return s;
  const [i, f = ""] = s.split(".");
  return digits === 0 ? s : `${i}.${f.padEnd(digits, "0")}`;
}

// ---------- routing and views ----------

function setRoute() {
  if (S.id) history.replaceState(null, "", `#${encodeURIComponent(S.id)}/${S.view}`);
}

function setView(view, { focus = false } = {}) {
  if (!VIEWS.includes(view)) view = "overview";
  const changed = view !== S.view;
  if (changed) closeSheet();
  S.view = view;
  for (const tab of document.querySelectorAll('[role="tab"]')) {
    const on = tab.dataset.view === view;
    tab.setAttribute("aria-selected", on);
    tab.tabIndex = on ? 0 : -1;
    if (on && focus) tab.focus();
  }
  for (const v of VIEWS) $(`#view-${v}`).hidden = v !== view;
  $("#appAdd").hidden = view !== "lines";
  $("#minibar").hidden = view === "overview" || !S.report;
  setRoute();
  if (view === "overview") drawChart(); // it can't measure itself while hidden
  if (changed && matchMedia("(max-width: 959.98px)").matches) window.scrollTo({ top: 0 });
}

// ---------- loading ----------

async function loadList() {
  S.list = await api("GET", "/api/budgets");
  $("#picker").innerHTML = S.list.map((b) => `<option value="${esc(b.id)}">${esc(b.name || b.id)}</option>`).join("");
  $("#empty").hidden = S.list.length > 0;
  $("#workspace").hidden = S.list.length === 0;
  $("#picker").hidden = S.list.length === 0;
  if (!S.list.length) $("#minibar").hidden = true;
}

async function open(id, view = S.view) {
  const v = await api("GET", `/api/budgets/${encodeURIComponent(id)}`);
  S.id = v.id;
  S.lastSummary = "";
  adopt(v, true);
  $("#picker").value = id;
  try { localStorage.setItem("bbbudget.last", id); } catch {}
  setView(view);
  status("All changes saved");
  loadCompare();
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
  status("Saving…");
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
    else status("All changes saved");
    S.notice = null;
    loadCompare();
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
  if (!S.id || S.pending || S.saving || $("#sheet").open || document.visibilityState !== "visible") return;
  try {
    const list = await api("GET", "/api/budgets");
    const me = list.find((b) => b.id === S.id);
    if (me && me.revision > S.revision && !S.pending && !S.saving) {
      const v = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}`);
      adopt(v, true);
      status("Updated with changes made elsewhere");
      loadCompare();
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
    // A field inside a closed sheet can't be highlighted: mark its row instead.
    document.querySelectorAll("[data-row]").forEach((el) => { if (path.startsWith(el.dataset.row + ".")) el.classList.add("invalid"); });
  }
}

// ---------- undo ----------

let toastTimer = null;
function toast(text, undo) {
  $("#toastText").textContent = text;
  $("#toast").hidden = false;
  $("#toastUndo").onclick = () => { hideToast(); undo(); };
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, UNDO_MS);
}
function hideToast() { $("#toast").hidden = true; clearTimeout(toastTimer); }

// removeWithUndo deletes list[i] and offers to put it back where it was.
function removeWithUndo(list, i, what) {
  closeSheet();
  const arr = S.budget[list];
  const [item] = arr.splice(i, 1);
  edited({ structural: true });
  toast(`Deleted ${what}`, () => {
    S.budget[list].splice(Math.min(i, S.budget[list].length), 0, item);
    edited({ structural: true });
  });
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

const field = (label, path, { type = "text", attrs = "", cls = "", value } = {}) => {
  const v = value ?? getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<input data-path="${esc(path)}" data-kind="${type === "decimal" ? "decimal" : type}" ${
    type === "decimal" ? 'inputmode="decimal" autocomplete="off"' : type === "int" ? 'type="number" step="1"' : `type="${type}"`
  } value="${esc(v ?? "")}" ${attrs}></label>`;
};
const opts = (options, v) => options.map(([val, text]) => `<option value="${esc(val)}" ${String(val) === String(v) ? "selected" : ""}>${esc(text)}</option>`).join("");
const select = (label, path, options, { structural = false, cls = "" } = {}) => {
  const v = getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<select data-path="${esc(path)}" data-kind="text" ${structural ? "data-structural" : ""}>${opts(options, v)}</select></label>`;
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
  // Re-rendering replaces the inputs: keep the focus where it was.
  const ae = document.activeElement?.dataset || {};
  const active = { path: ae.path, row: ae.row };

  document.title = `${b.name} · Budget planner`;
  document.querySelectorAll(".baseCode").forEach((el) => { el.textContent = b.base_currency; });
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

  $("#refreshRates").hidden = !b.currencies.length || !S.prefs.fetch_rates;
  renderLines();
  renderItems();
  if (S.sheet && $("#sheet").open) renderSheet();
  renderWhatIf();
  renderLineNumbers();
  renderRateNotes();
  restoreFocus(active);
}

// restoreFocus puts the focus back on the same field or row after a re-render.
function restoreFocus(active) {
  if (!active) return;
  const sel = active.path ? `[data-path="${CSS.escape(active.path)}"]` : active.row ? `button[data-row="${CSS.escape(active.row)}"]` : "";
  if (sel) document.querySelector(sel)?.focus();
}

const CHEVRON = `<svg class="ico chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>`;

// itemRow is one readable row that opens the editor sheet for that item.
const itemRow = ({ kind, i, name, meta = "", metaAttrs = "", value = "", color = "var(--line)" }) =>
  `<button type="button" class="line item" data-row="${kind}.${i}" data-action="edit" data-kind="${kind}" data-i="${i}" style="box-shadow:inset 3px 0 0 ${esc(color)}">
    <span class="line-main"><span class="name">${esc(name)}</span><span class="meta" ${metaAttrs}>${meta}</span></span>
    <span class="val">${value}</span>${CHEVRON}</button>`;

// renderItems draws debts, goals, currencies and categories as lists. Their
// texts come partly from the report, so this also runs after every save.
function renderItems() {
  const b = S.budget, r = S.report;
  const debtRep = Object.fromEntries((r?.debts || []).map((d) => [d.id, d]));
  const goalRep = Object.fromEntries((r?.goals || []).map((g) => [g.id, g]));
  const crypto = new Set(S.meta?.crypto || []);

  $("#debts").innerHTML = b.debts.map((d, i) => {
    const rep = debtRep[d.id];
    const when = rep ? (rep.paid_off_label ? `paid off ${esc(rep.paid_off_label)}` : parseFloat(d.balance) > 0 ? "not paid off in this window" : "") : "";
    return itemRow({ kind: "debts", i, name: d.name, color: "var(--debt)",
      meta: [`${esc(d.apr_pct)}% APR`, `min ${esc(fmtAmount(d.min_payment, d.currency))} ${esc(d.currency)}/mo`, when].filter(Boolean).join(" · "),
      value: `${esc(fmtAmount(d.balance, d.currency))} <small>${esc(d.currency)}</small>` });
  }).join("") || `<p class="muted pad">No debts. Add credit cards or loans to see interest and payoff dates.</p>`;

  $("#goals").innerHTML = b.goals.map((g, i) => {
    const rep = goalRep[g.id];
    const meta = !rep ? "" : rep.already_met ? "already covered by savings" : rep.reached_label ? `reached ${esc(rep.reached_label)}` : rep.needed_per_month ? `needs ${esc(rep.needed_per_month.display)}/mo` : "";
    return itemRow({ kind: "goals", i, name: g.name, color: "var(--save)", meta, value: `${esc(fmtAmount(g.target, b.base_currency))} <small>${esc(b.base_currency)}</small>` });
  }).join("") || `<p class="muted pad">No goals yet.</p>`;

  $("#currencies").innerHTML = b.currencies.map((c, i) => itemRow({ kind: "currencies", i,
    name: crypto.has(c.code) ? `${c.code} · crypto` : c.code, metaAttrs: `data-rate="${esc(c.code)}"`,
    value: `${esc(c.per_base)} <small>per ${esc(b.base_currency)}</small>` })).join("") || `<p class="muted pad">Only ${esc(b.base_currency)} so far.</p>`;

  $("#categories").innerHTML = b.categories.map((c, i) => {
    const n = b.lines.filter((l) => l.category === c.id).length;
    return itemRow({ kind: "categories", i, name: c.label, color: c.color || "var(--line)",
      meta: `${c.kind === "income" ? "Income" : "Cost"} · ${n} line${n === 1 ? "" : "s"}` });
  }).join("");
}

// ---------- lines: grouped list + editor sheet ----------

// lineGroups orders lines income first, then by expense category, as
// [{cat, lines: [index, ...]}].
function lineGroups() {
  const b = S.budget;
  const order = [...b.categories.filter((c) => c.kind === "income"), ...b.categories.filter((c) => c.kind !== "income")];
  const groups = order.map((cat) => ({ cat, lines: [] }));
  const byId = Object.fromEntries(groups.map((g) => [g.cat.id, g]));
  const other = { cat: { id: "", label: "Other", color: "" }, lines: [] };
  b.lines.forEach((l, i) => (byId[l.category] || other).lines.push(i));
  return [...groups, other].filter((g) => g.lines.length);
}

function renderLines() {
  const b = S.budget;
  const rep = Object.fromEntries((S.report?.lines || []).map((l) => [l.id, l]));
  $("#lines").innerHTML = lineGroups().map(({ cat, lines }) => `<div class="group">
      <div class="group-head"><span><span class="sw" style="background:${esc(cat.color || "var(--line)")}"></span>${esc(cat.label)}</span>
        <span class="total" data-total="${esc(cat.id)}"></span></div>
      ${lines.map((i) => {
        const l = b.lines[i];
        return `<div class="line" data-row="lines.${i}" style="box-shadow:inset 3px 0 0 ${esc(cat.color || "var(--line)")}">
          <button type="button" class="line-main" data-action="edit" data-kind="lines" data-i="${i}" aria-label="Edit ${esc(l.name)}">
            <span class="name">${esc(l.name)}</span><span class="meta">${esc(rep[l.id]?.schedule_text || "")}</span></button>
          <label class="amt"><span class="sr">Amount for ${esc(l.name)}</span>
            <input data-path="lines.${i}.amount" data-kind="decimal" data-cur="${esc(l.currency)}" inputmode="decimal" autocomplete="off" value="${esc(fmtAmount(l.amount, l.currency))}">
            <span class="cur">${esc(l.currency)}</span></label>
          <span class="chev-hit" data-action="edit" data-kind="lines" data-i="${i}">${CHEVRON}</span>
          <span class="base" data-base="${esc(l.id)}"></span>
        </div>`;
      }).join("")}
    </div>`).join("") || `<div class="card"><p class="muted">No lines yet. Add your income and regular costs.</p></div>`;
}

// renderLineNumbers updates the computed figures in place, so typing in an
// amount never loses focus when the report comes back.
function renderLineNumbers() {
  if (!S.report || !S.budget) return;
  const base = S.report.base_currency;
  const byId = Object.fromEntries(S.report.lines.map((l) => [l.id, l]));
  document.querySelectorAll("[data-base]").forEach((el) => {
    const l = byId[el.dataset.base];
    if (!l) { el.textContent = ""; return; }
    const once = l.schedule.every === 0;
    const v = once ? l.per_occurrence_base : l.monthly_equivalent_base;
    const foreign = l.amount.currency !== base;
    el.textContent = once ? (foreign ? `≈ ${v.display} once` : "once") : foreign || l.schedule.every !== 1 ? `≈ ${v.display}/mo` : "";
  });
  // Group totals, display only: recurring per month, one-offs separately.
  const totals = {}, once = {};
  for (const l of S.report.lines) {
    const cat = S.budget.categories.some((c) => c.id === l.category) ? l.category : "";
    totals[cat] = (totals[cat] || 0) + parseFloat(l.monthly_equivalent_base.amount);
    if (l.schedule.every === 0) once[cat] = (once[cat] || 0) + parseFloat(l.per_occurrence_base.amount);
  }
  const fmt = new Intl.NumberFormat("en-GB", { style: "currency", currency: base, maximumFractionDigits: 0 });
  document.querySelectorAll("[data-total]").forEach((el) => {
    const k = el.dataset.total, m = totals[k] || 0, o = once[k] || 0;
    const parts = [];
    if (m || !o) parts.push(`${esc(fmt.format(m))}<small>/mo</small>`);
    if (o) parts.push(`${esc(fmt.format(o))}<small> once</small>`);
    el.innerHTML = parts.join(" + ");
  });
  // Schedule texts come from the report too.
  document.querySelectorAll('#lines .line[data-row^="lines."]').forEach((row) => {
    const l = S.budget.lines[+row.dataset.row.split(".")[1]];
    const meta = row.querySelector(".meta");
    if (l && meta) meta.textContent = byId[l.id]?.schedule_text || "";
  });
  if ($("#sheet").open && S.sheet?.kind === "lines") {
    const l = S.budget.lines[S.sheet.i];
    const r = l && byId[l.id];
    const el = $("#sheetBase");
    if (el) el.textContent = r && r.amount.currency !== base
      ? `Each ${r.income ? "payment you receive" : "payment"} ≈ ${r.per_occurrence_base.display} in ${base}, ${r.income ? "after cash-out loss" : "transfer loss included"}.`
      : "";
  }
}

function categorySelect(path) {
  const v = getPath(S.budget, path);
  const group = (kind, label) => {
    const cs = S.budget.categories.filter((c) => (kind === "income") === (c.kind === "income"));
    return cs.length ? `<optgroup label="${label}">${opts(cs.map((c) => [c.id, c.label]), v)}</optgroup>` : "";
  };
  return `<label class="f">Category<select data-path="${esc(path)}" data-kind="text" data-structural>${group("income", "Income")}${group("expense", "Costs")}</select></label>`;
}

// SHEETS describes the editor for each kind of item: its title, fields and
// the delete action. Fields use the same data-path inputs as everywhere else.
const SHEETS = {
  lines: {
    noun: "line", title: (l) => l.name,
    body(l, i) {
      const sch = l.schedule;
      return `${field("Name", `lines.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${field("Amount", `lines.${i}.amount`, { type: "decimal", value: fmtAmount(l.amount, l.currency) })}
        ${select("Currency", `lines.${i}.currency`, currencyOptions(), { structural: true })}
        ${categorySelect(`lines.${i}.category`)}
        ${select("Repeats", `lines.${i}.schedule.every`, EVERY, { structural: true })}
        <label class="f">${sch.every === 0 ? "In" : "From"}<input type="month" data-path="lines.${i}.schedule.start" data-kind="monthidx" value="${monthKey(sch.start)}"></label>
        ${sch.every === 0 ? "<span></span>" :
          `<label class="f">Until (optional)<input type="month" data-path="lines.${i}.schedule.end" data-kind="monthidx-opt" value="${sch.end == null ? "" : monthKey(sch.end)}"></label>`}
        <p class="base" id="sheetBase"></p>`;
    },
  },
  debts: {
    noun: "debt", title: (d) => d.name,
    body(d, i) {
      return `${field("Name", `debts.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${field("Balance", `debts.${i}.balance`, { type: "decimal" })}
        ${select("Currency", `debts.${i}.currency`, currencyOptions(), { structural: true })}
        ${field("APR %", `debts.${i}.apr_pct`, { type: "decimal" })}
        ${field("Minimum / month", `debts.${i}.min_payment`, { type: "decimal" })}
        <p class="base">Interest is charged monthly at APR ÷ 12. The minimum is paid each month until the balance is zero.</p>`;
    },
  },
  goals: {
    noun: "goal", title: (g) => g.name,
    body(g, i) {
      return `${field("Goal", `goals.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${field(`Target (${S.budget.base_currency})`, `goals.${i}.target`, { type: "decimal" })}`;
    },
  },
  currencies: {
    noun: "currency", title: (c) => c.code,
    body(c, i) {
      const crypto = new Set(S.meta?.crypto || []);
      const codes = (S.meta?.currencies || []).filter((x) => x !== S.budget.base_currency).map((x) => [x, crypto.has(x) ? `${x} (crypto)` : x]);
      return `${select("Code", `currencies.${i}.code`, codes, { structural: true })}
        ${select("Rate from", `currencies.${i}.source`, RATE_SOURCES, { structural: true })}
        ${field(`Units per 1 ${S.budget.base_currency}`, `currencies.${i}.per_base`, { type: "decimal" })}
        ${field("Payment loss %", `currencies.${i}.transfer_fee_pct`, { type: "decimal" })}
        ${field("Cash-out loss %", `currencies.${i}.cash_out_fee_pct`, { type: "decimal", attrs: 'placeholder="0"' })}
        <p class="base" data-rate="${esc(c.code)}"></p>
        <p class="base">Payment loss is added to every payment in this currency. Cash-out loss is taken off income in it, such as fees for selling crypto. Typing a rate makes it manual.</p>`;
    },
  },
  categories: {
    noun: "category", title: (c) => c.label,
    body(c, i) {
      return `${field("Label", `categories.${i}.label`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${select("Kind", `categories.${i}.kind`, [["expense", "Cost"], ["income", "Income"]], { structural: true })}
        <label class="f">Colour<input type="color" data-path="categories.${i}.color" data-kind="text" value="${esc(c.color || "#888888")}"></label>`;
    },
  },
};

function renderSheet() {
  const { kind, i } = S.sheet;
  const item = S.budget[kind]?.[i];
  const def = SHEETS[kind];
  if (!item || !def) { closeSheet(); return; }
  $("#sheetTitle").textContent = def.title(item) || def.noun;
  $("#sheetBody").innerHTML = `<div class="sheet-body">${def.body(item, i)}</div>
    <div class="sheet-foot">
      <button type="button" class="danger" data-action="delete-item" data-kind="${kind}" data-i="${i}">Delete ${def.noun}</button>
      <button type="button" class="primary" data-action="close-sheet">Done</button>
    </div>`;
  renderLineNumbers();
  renderRateNotes();
}

function openSheet(kind, i, { focusFirst = false } = {}) {
  S.sheet = { kind, i };
  renderSheet();
  const d = $("#sheet");
  if (!d.open) d.showModal();
  renderLineNumbers(); // the base-currency note needs the sheet open
  // A new item starts on its name; an existing line on its amount.
  const first = focusFirst ? d.querySelector("#sheetBody input, #sheetBody select")
    : kind === "lines" ? d.querySelector(`[data-path="lines.${i}.amount"]`) : null;
  first?.focus();
  if (focusFirst && first?.select) first.select();
}
function closeSheet() {
  const d = $("#sheet");
  if (d.open) d.close();
}

// ---------- input handling ----------

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
  // The same item can be edited in its list and in the sheet: keep both in step.
  const item = path.match(/^(\w+)\.(\d+)\.(amount|name|label)$/);
  if (item) {
    document.querySelectorAll(`[data-path="${CSS.escape(path)}"]`).forEach((o) => { if (o !== el) o.value = v; });
    if (item[3] !== "amount") {
      document.querySelectorAll(`[data-row="${item[1]}.${item[2]}"] .name`).forEach((n) => { n.textContent = v; });
      if (S.sheet?.kind === item[1] && S.sheet.i === +item[2]) $("#sheetTitle").textContent = v || SHEETS[item[1]]?.noun || "";
    }
  }
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
  "add-line"() {
    const b = S.budget;
    const cat = b.categories.find((c) => c.kind === "expense") || b.categories[0];
    if (!cat) { setView("setup"); status("Add a category first.", true); return; }
    b.lines.push({ id: newId("New line", b.lines), name: "New line", amount: "0", currency: b.base_currency, category: cat.id, schedule: { start: 0, every: 1 } });
    edited({ structural: true });
    openSheet("lines", b.lines.length - 1, { focusFirst: true });
  },
  edit(i, el) { openSheet(el.dataset.kind, i); },
  "close-sheet"() { closeSheet(); },
  "delete-item"(i, el) {
    const kind = el.dataset.kind;
    const item = S.budget[kind][i];
    if (kind === "currencies") {
      const user = [...S.budget.lines, ...S.budget.debts].find((x) => x.currency === item.code);
      if (user) { status(`${item.code} is still used by “${user.name}”.`, true); return; }
    }
    if (kind === "categories" && S.budget.lines.some((l) => l.category === item.id)) {
      status(`“${item.label}” is still used by a line.`, true);
      return;
    }
    removeWithUndo(kind, i, kind === "currencies" ? item.code : `“${item.name || item.label}”`);
  },
  "add-currency"() {
    const b = S.budget;
    const used = new Set([b.base_currency, ...b.currencies.map((c) => c.code)]);
    const crypto = new Set(S.meta?.crypto || []);
    const code = (S.meta?.currencies || []).find((c) => !used.has(c) && !crypto.has(c));
    if (!code) return;
    const fetch = S.prefs.fetch_rates && S.prefs.default_rate_source !== "manual";
    // The rate starts at 1 for a moment, until fetchRate fills in the real one.
    b.currencies.push({ code, per_base: "1", transfer_fee_pct: "0", source: fetch ? S.prefs.default_rate_source : "manual" });
    const i = b.currencies.length - 1;
    if (!fetch) { edited({ structural: true }); openSheet("currencies", i, { focusFirst: true }); return; }
    // Pending now, so poll() can't replace the budget (and drop this row) while
    // the rate loads; fetchRate's edited() then saves it.
    S.pending = true;
    renderEditors();
    openSheet("currencies", i, { focusFirst: true });
    const row = b.currencies[i];
    fetchRate(i, () => {
      const at = S.budget.currencies.indexOf(row);
      if (at >= 0) S.budget.currencies.splice(at, 1);
    });
  },
  "refresh-rates"() { refreshAll(); },
  "add-debt"() {
    const b = S.budget;
    b.debts.push({ id: newId("Debt", b.debts), name: "Debt", currency: b.base_currency, balance: "0", apr_pct: "0", min_payment: "0" });
    edited({ structural: true });
    openSheet("debts", b.debts.length - 1, { focusFirst: true });
  },
  "add-goal"() {
    const b = S.budget;
    b.goals.push({ id: newId("Goal", b.goals), name: "Goal", target: "1000" });
    edited({ structural: true });
    openSheet("goals", b.goals.length - 1, { focusFirst: true });
  },
  "add-category"() {
    const b = S.budget;
    b.categories.push({ id: newId("Category", b.categories), label: "Category", kind: "expense", color: "#7a6ff0" });
    edited({ structural: true });
    openSheet("categories", b.categories.length - 1, { focusFirst: true });
  },
  "all-insights"() { S.allInsights = !S.allInsights; renderInsights(); },
  new() { openNewDialog(); },
  export() { exportJSON(); },
  "add-wi"() { S.wi.push({ kind: "line", target: S.budget.lines[0]?.id || "", value: "" }); renderWhatIf(); },
  "del-wi"(i) { S.wi.splice(i, 1); renderWhatIf(); },
  "preview-wi"() { previewWhatIf(); },
  "save-wi"() { saveWhatIf(); },
  async "adopt-scenario"(i, el) {
    if (!confirm(`Apply “${el.dataset.name}” to the real budget?`)) return;
    await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/scenarios/${encodeURIComponent(el.dataset.sid)}/adopt`, {}));
  },
  async "del-scenario"(i, el) {
    if (!confirm(`Delete the scenario “${el.dataset.name}”?`)) return;
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

// summaryParts are the few numbers that answer "how am I doing?", shown in
// the rail, at the top of Overview on phones, and in the phone minibar.
function summaryParts(r) {
  const m0 = r.months[0];
  const last = r.months[r.months.length - 1];
  const goal = r.goals[0];
  const debts = r.debts.filter((d) => parseFloat(d.start_balance.amount) > 0);
  const debtFree = !debts.length ? null : debts.every((d) => d.paid_off_label)
    ? debts.map((d) => d.paid_off_month).reduce((a, b) => Math.max(a, b)) : -1;
  const warn = r.insights.find((x) => x.tone === "warn");
  return { m0, last, goal, debts, debtFree, warn, n: r.months.length };
}

function renderSummary() {
  const r = S.report;
  const p = summaryParts(r);
  const neg = parseFloat(p.m0.left.amount) < 0;
  const month = p.m0.label.split(" ")[0];
  const goalTxt = p.goal ? (p.goal.already_met ? "Already met" : p.goal.reached_label || `Not in ${p.n} months`) : "";
  const debtTxt = p.debtFree === null ? "" : p.debtFree < 0 ? `Not in ${p.n} months` : r.months[p.debtFree].label;
  const html = `<h2>${neg ? "Short" : "Left"} in ${esc(month)}</h2>
    <div class="big ${neg ? "neg" : ""}">${esc(p.m0.left.display.replace("−", ""))}</div>
    <div class="sub">of ${esc(p.m0.income.display)} income · ${esc(p.m0.saved.display)} to savings</div>
    <dl>
      <dt>Savings in ${p.n} months</dt><dd>${esc(p.last.savings.display)}</dd>
      ${p.goal ? `<dt>${esc(p.goal.name)}</dt><dd>${esc(goalTxt)}</dd>` : ""}
      ${p.debtFree !== null ? `<dt>Debt-free</dt><dd>${esc(debtTxt)}</dd>` : ""}
      ${p.debts.length ? `<dt>Interest, ${p.n} months</dt><dd>${esc(r.totals.total_interest_base.display)}</dd>` : ""}
    </dl>
    ${p.warn ? `<div class="callout">${bold(p.warn.text)}</div>` : ""}`;
  // Flash the summary when an edit changed it, so the effect is noticed.
  const changed = S.lastSummary && S.lastSummary !== html;
  S.lastSummary = html;
  if (changed) announce(`${neg ? "Short" : "Left"} in ${month}: ${p.m0.left.display.replace("−", "")}. Savings in ${p.n} months: ${p.last.savings.display}.`);
  for (const el of [$("#railSummary"), $("#mobileSummary")]) {
    el.innerHTML = html;
    if (changed) { el.classList.remove("flash"); void el.offsetWidth; el.classList.add("flash"); }
  }
  const mb = $("#minibar");
  mb.innerHTML = `<span>${neg ? "Short" : "Left"} in ${esc(month)} <b class="${neg ? "neg" : ""}">${esc(p.m0.left.display.replace("−", ""))}</b></span>
    <span>In ${p.n} months <b>${esc(p.last.savings.display)}</b></span>`;
  mb.hidden = S.view === "overview";
}

// announce tells screen readers what an edit changed, once, after typing pauses.
let announceTimer = null;
function announce(text) {
  clearTimeout(announceTimer);
  announceTimer = setTimeout(() => { $("#announce").textContent = text; }, 1200);
}

const FX_RE = /^(fx_risk_|transfer_loss_|cash_out_loss_)/;
// Risks: things that can make the plan go wrong even when nothing is wrong yet.
const RISK_RE = /^(crypto_income|negative_savings|debt_min_)/;

// relevance ranks what to show first: warnings, then risks (crypto income,
// currency exposure), then good news, then the rest. Within a rank the
// server's order is kept.
function relevance(x) {
  if (x.tone === "warn") return 0;
  if (x.group || RISK_RE.test(x.id || "")) return 1;
  if (x.tone === "good") return 2;
  return 3;
}

// renderInsights ranks by relevance, folds the per-currency notes into one
// "Currency exposure" card, and shows three until asked for all.
function renderInsights() {
  const r = S.report;
  const fx = r.insights.filter((x) => FX_RE.test(x.id));
  const items = r.insights.filter((x) => !FX_RE.test(x.id)).map((x) => ({ ...x }));
  if (fx.length) items.push({ tone: fx.some((x) => x.tone === "warn") ? "warn" : "info", group: fx });
  items.sort((a, b) => relevance(a) - relevance(b));
  const shown = S.allInsights ? items : items.slice(0, 3);
  const card = (x) => x.group
    ? `<details class="ins ${esc(x.tone)}"><summary><span><b>Currency exposure</b> · ${x.group.length} note${x.group.length === 1 ? "" : "s"} on rates and fees</span></summary>
        <div class="ins-group">${x.group.map((g) => `<p>${bold(g.text)}</p>`).join("")}</div></details>`
    : `<div class="ins ${esc(x.tone)}">${bold(x.text)}</div>`;
  $("#insights").innerHTML = (shown.map(card).join("") || `<p class="muted">Add income and costs to see insights.</p>`) +
    (items.length > 3 ? `<button type="button" class="linkbtn" data-action="all-insights" aria-expanded="${S.allInsights}">${S.allInsights ? "Show fewer" : `Show all ${items.length}`}</button>` : "");
}

function renderOutputs() {
  const r = S.report;
  if (!r) return;
  renderSummary();
  renderRateNotes();
  const m0 = r.months[0];
  const cats = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#monthName").textContent = m0.label.split(" ")[0];
  $("#incomeBig").textContent = m0.income.display;
  $("#monthsOut").textContent = r.months.length;

  const num = (m) => Math.max(parseFloat(m.amount), 0); // pixels only
  const segs = Object.entries(m0.by_category)
    .filter(([, v]) => parseFloat(v.amount) > 0)
    .map(([id, v]) => [cats[id]?.label || id, v, cats[id]?.color || "var(--accent)", ""]);
  if (num(m0.debt_payments) > 0) segs.push(["Debt payments", m0.debt_payments, "var(--debt)", "hatch"]);
  if (num(m0.saved) > 0) segs.push(["To savings", m0.saved, "var(--save)", ""]);
  const tot = segs.reduce((a, s) => a + num(s[1]), 0) || 1;
  const inc = parseFloat(m0.income.amount) || 0;
  $("#bar").innerHTML = segs.map((s) => `<div class="${s[3]}" style="flex-basis:${(num(s[1]) / tot) * 100}%;background:${esc(s[2])}" title="${esc(s[0])}: ${esc(s[1].display)}"></div>`).join("");
  $("#legend").innerHTML = segs.map((s) => `<div><span class="sw ${s[3]}" style="background:${esc(s[2])}"></span>${esc(s[0])}<b>${esc(s[1].display)}${inc > 0 ? `<small>${Math.round((num(s[1]) / inc) * 100)}%</small>` : ""}</b></div>`).join("");

  renderInsights();
  // Debts, goals and currencies show report figures (payoff dates, rates).
  const ae = document.activeElement?.dataset || {};
  renderItems();
  renderRateNotes(); // fills the currency rows just drawn
  if (ae.row) restoreFocus({ row: ae.row });
  drawChart();

  const hasDebt = r.debts.length > 0;
  let h = `<thead><tr><th scope="col">Month</th><th scope="col">Income</th><th scope="col">Spent</th><th scope="col">To debts</th><th scope="col">Left</th><th scope="col">Saved</th><th scope="col">Savings</th>${hasDebt ? '<th scope="col">Debt</th>' : ""}</tr></thead><tbody>`;
  for (const m of r.months) {
    const neg = (x) => (parseFloat(x.amount) < 0 ? "neg" : "");
    h += `<tr><th scope="row">${esc(m.label)}</th><td>${esc(m.income.display)}</td><td>${esc(m.expenses.display)}</td><td>${esc(m.debt_payments.display)}</td><td class="${neg(m.left)}">${esc(m.left.display)}</td><td class="${neg(m.saved)}">${esc(m.saved.display)}</td><td class="${neg(m.savings)}">${esc(m.savings.display)}</td>${hasDebt ? `<td>${esc(m.debt_balance_base.display)}</td>` : ""}</tr>`;
  }
  $("#table").innerHTML = h + "</tbody>";
  renderLineNumbers();
  $("#minibar").hidden = S.view === "overview";
}

// niceTicks returns round axis values covering [lo, hi]: 0, 5k, 10k, ...
function niceTicks(lo, hi, count) {
  const raw = (hi - lo) / count || 1;
  const p = 10 ** Math.floor(Math.log10(raw));
  const m = raw / p;
  const step = (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * p;
  const ticks = [];
  for (let v = Math.floor(lo / step) * step; v < hi + step * 0.999; v += step) ticks.push(Math.round(v / step) * step);
  return ticks;
}

// drawChart sizes the chart to its container, so text stays 12px on a phone,
// and supports hover/touch to read any month.
function drawChart() {
  const r = S.report;
  const wrap = $("#chartWrap");
  if (!r || !wrap || $("#view-overview").hidden) return;
  const svg = $("#chart");
  const W = Math.max(280, Math.round(wrap.clientWidth));
  const compact = W < 520;
  const H = compact ? 220 : 260, R = 12, T = 14, B = 28;
  const sav = r.months.map((m) => parseFloat(m.savings.amount));
  const debt = r.months.map((m) => parseFloat(m.debt_balance_base.amount));
  const hasDebt = debt.some((v) => v > 0);
  const goals = r.goals.map((g) => parseFloat(g.target.amount));
  const all = sav.concat(hasDebt ? debt : [], goals);
  const ticks = niceTicks(Math.min(0, ...all), Math.max(100, ...all), compact ? 3 : 4);
  const lo = ticks[0], hi = ticks[ticks.length - 1];
  const cur = r.base_currency;
  const fmt = new Intl.NumberFormat("en-GB", { style: "currency", currency: cur, maximumFractionDigits: 0, notation: compact ? "compact" : "standard" });
  const L = compact ? 48 : 70;
  const n = Math.max(r.months.length - 1, 1);
  const x = (i) => L + (i * (W - L - R)) / n;
  const y = (v) => T + ((hi - v) / (hi - lo)) * (H - T - B);
  let g = "";
  for (const v of ticks) {
    const yy = y(v);
    g += `<line x1="${L}" x2="${W - R}" y1="${yy}" y2="${yy}" stroke="var(--line)"/><text x="${L - 8}" y="${yy + 4}" text-anchor="end">${esc(fmt.format(v))}</text>`;
  }
  if (lo < 0) g += `<line x1="${L}" x2="${W - R}" y1="${y(0)}" y2="${y(0)}" stroke="var(--muted)" stroke-dasharray="3 3"/>`;
  const step = Math.ceil(r.months.length / (compact ? 4 : 8));
  r.months.forEach((m, i) => { if (i % step === 0) g += `<text x="${x(i)}" y="${H - 8}" text-anchor="middle">${esc(m.label.replace(/ (\d\d)(\d\d)$/, " $2"))}</text>`; });
  r.goals.forEach((gl, i) => {
    g += `<line x1="${L}" x2="${W - R}" y1="${y(goals[i])}" y2="${y(goals[i])}" stroke="var(--save)" stroke-dasharray="6 4" opacity=".55"/><text x="${W - R}" y="${y(goals[i]) - 5}" text-anchor="end">${esc(gl.name)}</text>`;
  });
  const line = (arr, c) => `<polyline fill="none" stroke="${c}" stroke-width="2.5" stroke-linejoin="round" points="${arr.map((v, i) => x(i) + "," + y(v)).join(" ")}"/>` +
    arr.map((v, i) => `<circle cx="${x(i)}" cy="${y(v)}" r="${compact ? 2.5 : 3}" fill="${c}"/>`).join("");
  g += line(sav, "var(--save)");
  if (hasDebt) g += line(debt, "var(--debt)");
  g += `<line id="guide" x1="0" x2="0" y1="${T}" y2="${H - B}" stroke="var(--muted)" stroke-dasharray="2 3" visibility="hidden"/>`;
  g += `<rect id="hit" x="${L}" y="${T}" width="${W - L - R}" height="${H - T - B}" fill="transparent"/>`;
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  svg.setAttribute("height", H);
  svg.innerHTML = g;

  const tip = $("#chartTip"), guide = svg.querySelector("#guide"), hit = svg.querySelector("#hit");
  const show = (ev) => {
    const box = svg.getBoundingClientRect();
    const i = Math.max(0, Math.min(r.months.length - 1, Math.round(((ev.clientX - box.left) * (W / box.width) - L) / ((W - L - R) / n))));
    const m = r.months[i];
    guide.setAttribute("x1", x(i)); guide.setAttribute("x2", x(i)); guide.setAttribute("visibility", "visible");
    tip.innerHTML = `<b>${esc(m.label)}</b><span>Savings <strong>${esc(m.savings.display)}</strong></span>${hasDebt ? `<span>Debt <strong>${esc(m.debt_balance_base.display)}</strong></span>` : ""}<span>Left <strong>${esc(m.left.display)}</strong></span>`;
    tip.hidden = false;
    const px = (x(i) / W) * box.width;
    tip.style.left = `${Math.min(Math.max(px - tip.offsetWidth / 2, 0), box.width - tip.offsetWidth)}px`;
  };
  const hide = () => { tip.hidden = true; guide.setAttribute("visibility", "hidden"); };
  hit.addEventListener("pointermove", show);
  hit.addEventListener("pointerdown", show);
  hit.addEventListener("pointerleave", hide);
}

// ---------- scenarios ----------

const WI_KINDS = [["line", "Change a line's amount"], ["rate", "Change an exchange rate"], ["extra", "Change share to debts (%)"], ["remove", "Remove a line"]];

function renderWhatIf() {
  const b = S.budget;
  const lineOpts = b.lines.map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join("");
  const curOpts = b.currencies.map((c) => `<option value="${esc(c.code)}">${esc(c.code)} (now ${esc(c.per_base)})</option>`).join("");
  $("#wiOps").innerHTML = S.wi.map((o, i) => `<div class="row row-wi">
      <label class="f">Change<select data-wi="${i}" data-k="kind">${WI_KINDS.map(([k, t]) => `<option value="${k}" ${k === o.kind ? "selected" : ""}>${t}</option>`).join("")}</select></label>
      ${o.kind === "extra" ? "<span></span>" : `<label class="f">${o.kind === "rate" ? "Currency" : "Line"}<select data-wi="${i}" data-k="target">${o.kind === "rate" ? curOpts : lineOpts}</select></label>`}
      ${o.kind === "remove" ? "<span></span>" : `<label class="f">New value<input data-wi="${i}" data-k="value" inputmode="decimal" value="${esc(o.value)}"></label>`}
      ${del("del-wi", i, "change")}
    </div>`).join("") || `<p class="muted">Add a change, for example rent down to 1,000 or a different exchange rate, then preview it.</p>`;
  $("#previewBtn").disabled = !S.wi.length;
  S.wi.forEach((o, i) => { const t = document.querySelector(`[data-wi="${i}"][data-k="target"]`); if (t) { if (o.target) t.value = o.target; o.target = t.value; } });
}

function wiOps() {
  return S.wi.map((o) => {
    if (o.kind !== "remove" && !DEC_RE.test(String(o.value).trim())) throw new Error("Each new value must be a plain number like 800 or 92.5");
    const value = String(o.value).trim();
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
    const goalTxt = after.goals.map((g) => `${esc(g.name)}: ${g.already_met ? "already met" : g.reached_label ? esc(g.reached_label) : "not reached"}`).join(" · ");
    out.innerHTML = `<div class="ins ${endA >= endB ? "good" : "warn"}" style="margin-top:12px">After ${after.months.length} months: <b>${esc(after.totals.end_savings.display)}</b> instead of ${esc(before.totals.end_savings.display)}. This month saves ${esc(after.months[0].saved.display)}.${goalTxt ? "<br>" + goalTxt : ""}</div>
      <div class="row-actions"><input id="wiName" placeholder="Scenario name" aria-label="Scenario name" style="flex:1;min-width:160px"><button type="button" data-action="save-wi">Save as scenario</button></div>`;
  } catch (e) {
    out.innerHTML = `<div class="ins warn" style="margin-top:12px">${esc(e.message)}</div>`;
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

async function loadCompare() {
  const table = $("#compare");
  if (!S.id) return;
  try {
    const rows = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/compare`);
    $("#compareEmpty").hidden = rows.length >= 2;
    if (rows.length < 2) { table.innerHTML = ""; return; }
    let h = `<thead><tr><th scope="col">Scenario</th><th scope="col">This month</th><th scope="col">End savings</th><th scope="col">Δ</th><th scope="col">Interest</th><th scope="col">Goals</th><th scope="col"><span class="sr">Actions</span></th></tr></thead><tbody>`;
    for (const o of rows) {
      if (o.error) { h += `<tr><th scope="row">${esc(o.name)}</th><td colspan="5" class="neg">${esc(o.error)}</td><td>${scenarioButtons(o)}</td></tr>`; continue; }
      const d = o.delta_end_savings;
      const goals = (o.goals || []).map((g) => g.already_met ? "met" : g.reached_label || "—").join(", ");
      h += `<tr><th scope="row">${esc(o.name)}</th><td>${esc(o.first_month_saved.display)}</td><td>${esc(o.end_savings.display)}</td><td class="${d ? (parseFloat(d.amount) < 0 ? "neg" : "pos") : ""}">${d ? esc(d.display) : ""}</td><td>${esc(o.total_interest_base.display)}</td><td>${esc(goals)}</td><td>${o.id === "baseline" ? "" : scenarioButtons(o)}</td></tr>`;
    }
    table.innerHTML = h + "</tbody>";
  } catch { table.innerHTML = ""; }
}
const scenarioButtons = (o) => `<button type="button" class="linkish" data-action="adopt-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}">Adopt</button> <button type="button" class="linkish" data-action="del-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}" aria-label="Delete ${esc(o.name)}">✕</button>`;

// ---------- new / import / export / delete ----------

function openNewDialog() {
  $("details.menu")?.removeAttribute("open");
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
    await open(v.id, "overview");
  } catch (e) { status(e.message, true); }
}

function exportJSON() {
  $("details.menu")?.removeAttribute("open");
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
    await open(id, "overview");
  } catch (e) {
    status(`Import failed: ${e.data?.issues ? e.data.issues.map((i) => `${i.path}: ${i.message}`).join("; ") : e.message}`, true);
  }
}

async function deleteBudget() {
  $("details.menu")?.removeAttribute("open");
  if (!S.id || !confirm(`Delete “${S.budget.name}” permanently? This can't be undone.`)) return;
  try {
    await api("DELETE", `/api/budgets/${encodeURIComponent(S.id)}`);
    S.id = null;
    await loadList();
    if (S.list.length) await open(S.list[0].id, "overview");
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
// Show amounts with their currency's decimals once the field is left.
document.addEventListener("focusout", (e) => {
  const el = e.target;
  const m = el.dataset?.path?.match(/^lines\.(\d+)\.amount$/);
  if (m && DEC_RE.test(el.value.trim())) el.value = fmtAmount(el.value.trim(), S.budget.lines[+m[1]]?.currency);
});
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-action]");
  if (!btn) return;
  ACTIONS[btn.dataset.action]?.(Number(btn.dataset.i), btn);
});

const tablist = $(".tabs");
tablist.addEventListener("click", (e) => {
  const tab = e.target.closest('[role="tab"]');
  if (tab) setView(tab.dataset.view);
});
tablist.addEventListener("keydown", (e) => {
  const i = VIEWS.indexOf(S.view);
  const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: VIEWS.length - 1 }[e.key];
  if (next === undefined) return;
  e.preventDefault();
  setView(VIEWS[(next + VIEWS.length) % VIEWS.length], { focus: true });
});

const sheet = $("#sheet");
sheet.addEventListener("click", (e) => { if (e.target === sheet) closeSheet(); }); // tap outside
sheet.addEventListener("close", () => {
  const was = S.sheet;
  S.sheet = null;
  renderLines();
  renderItems();
  renderLineNumbers();
  renderRateNotes();
  // Back to the row that was edited, for keyboard users.
  if (was) document.querySelector(`[data-row="${was.kind}.${was.i}"] .line-main, button[data-row="${was.kind}.${was.i}"]`)?.focus({ preventScroll: true });
});

const isField = (el) => el?.matches?.("input:not([type=color]):not([type=checkbox]), select, textarea");
document.addEventListener("focusin", (e) => document.body.classList.toggle("typing", isField(e.target)));
document.addEventListener("focusout", () => setTimeout(() => document.body.classList.toggle("typing", isField(document.activeElement)), 0));

let resizeTimer = null;
window.addEventListener("resize", () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(drawChart, 120); });
window.addEventListener("hashchange", () => {
  const [id, view] = decodeURIComponent(location.hash.slice(1)).split("/");
  if (id && id !== S.id && S.list.some((b) => b.id === id)) open(id, view);
  else if (view && view !== S.view) setView(view);
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
    let [want, view] = decodeURIComponent(location.hash.slice(1)).split("/");
    if (!want) try { want = localStorage.getItem("bbbudget.last") || ""; } catch {}
    await open(S.list.some((b) => b.id === want) ? want : S.list[0].id, VIEWS.includes(view) ? view : "overview");
  } catch (e) {
    status(`Could not reach the server: ${e.message}`, true);
  }
  setInterval(poll, POLL_MS);
})();
