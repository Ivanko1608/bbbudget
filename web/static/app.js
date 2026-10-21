// Budget planner UI. The server owns every number: this file edits the budget
// document (amounts stay exact decimal strings) and renders the server's
// report. The only floats here are pixel positions and display-only formatting.
//
// Layout: five views (Overview, Items, Plan, Scenarios, Setup) behind one tab
// list, shown as top tabs with a summary rail on wide screens and as a bottom
// tab bar with a pinned mini-summary on phones (the shape a native app would
// take). The route is #<budget>/<view>.
//
// Every piece of text comes from i18n/<locale>.json through t(), and money is
// formatted for the reader's locale from the exact amounts the server sends.

import { t, tHTML, loadLocale, money, moneyAxis, number, monthName, date, dateTime, toInput, parseDecimal } from "./i18n.js";
import { initAssistant } from "./assistant.js";

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const bold = (s) => esc(s).replace(/\*\*(.+?)\*\*/g, "<b>$1</b>");
const DEC_RE = /^[+-]?\d+(\.\d+)?$/;
const SAVE_DELAY = 600;
const POLL_MS = 3000;
const VIEWS = ["overview", "items", "plan", "scenarios", "setup"];
const UNDO_MS = 6000;

const S = {
  meta: null,
  prefs: { fetch_rates: false, default_rate_source: "auto" }, // app settings (settings.html); offline until loaded
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
  sheet: null, // index of the item open in the editor sheet
  allInsights: false,
  lastSummary: "",
  wi: [], // what-if draft ops
  history: [], // recent changes, newest first (Activity)
};

// ---------- API ----------

const OPS = {
  setItem: "set_item",
  removeItem: "remove_item",
  setCurrency: "set_currency",
  setSettings: "set_settings",
};

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
  $("#appAdd").hidden = view !== "items";
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
  status(t("status.saved"));
  loadCompare();
  loadActivity();
  loadMonthStatus();
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
  status(t("status.saving"));
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
    else status(t("status.saved"));
    S.notice = null;
    loadCompare();
    loadActivity();
    loadMonthStatus();
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
  if (!S.id || S.pending || S.saving || $("#sheet").open || document.visibilityState !== "visible") return;
  try {
    const list = await api("GET", "/api/budgets");
    const me = list.find((b) => b.id === S.id);
    if (me && me.revision > S.revision && !S.pending && !S.saving) {
      const v = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}`);
      adopt(v, true);
      status(t("status.updated_elsewhere"));
      loadCompare();
      loadActivity();
    loadMonthStatus();
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
  toast(t("toast.deleted", { what }), () => {
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

const field = (label, path, { type = "text", attrs = "", cls = "", value, opt = false } = {}) => {
  const v = value ?? getPath(S.budget, path);
  const shown = type === "decimal" ? toInput(v) : v;
  return `<label class="f ${cls}">${esc(label)}<input data-path="${esc(path)}" data-kind="${type === "decimal" ? "decimal" : type}" ${opt ? "data-optional" : ""} ${
    type === "decimal" ? 'inputmode="decimal" autocomplete="off"' : type === "int" ? 'type="number" step="1"' : `type="${type}"`
  } value="${esc(shown ?? "")}" ${attrs}></label>`;
};
const opts = (options, v) => options.map(([val, text]) => `<option value="${esc(val)}" ${String(val) === String(v) ? "selected" : ""}>${esc(text)}</option>`).join("");
const select = (label, path, options, { structural = false, cls = "" } = {}) => {
  const v = getPath(S.budget, path);
  return `<label class="f ${cls}">${esc(label)}<select data-path="${esc(path)}" data-kind="text" ${structural ? "data-structural" : ""}>${opts(options, v)}</select></label>`;
};
const del = (action, i, what) => `<button type="button" class="del" data-action="${action}" data-i="${i}" aria-label="${esc(t("remove", { what }))}">✕</button>`;

function currencyOptions() {
  return [S.budget.base_currency, ...S.budget.currencies.map((c) => c.code)].map((c) => [c, c]);
}

const rateSourceOptions = () => S.meta.rate_sources.map((s) => [s, t(`rate_source_opt.${s}`)]);
const isManual = (c) => !c.source || c.source === "manual";

const POT_KINDS = ["general", "sinking", "emergency"];
const potKind = (kind) => (POT_KINDS.includes(kind) ? t(`pot_kind.${kind}`) : kind);
const POT_COLOR = { general: "var(--save)", sinking: "#7a6ff0", emergency: "var(--debt)" };

// renderFlow lists, in order, where each month's leftover goes, with the
// debt share editable in place.
function renderFlow() {
  if ($("#flow").contains(document.activeElement)) return; // don't redraw under the cursor
  const b = S.budget, r = S.report;
  const fmtItem = (name, v) => esc(t("flow.fixed.item", { name, amount: money({ amount: v, currency: b.base_currency }) }));
  const owes = parseFloat(b.overdraft_start || 0) > 0 || (r?.months || []).some((m) => parseFloat(m.overdraft.amount) > 0);
  const fixed = b.pots.filter((p) => p.monthly);
  const debts = b.debts.filter((d) => parseFloat(d.balance) > 0);
  const targets = b.pots.filter((p) => p.target);
  const def = (r?.pots || []).find((x) => x.default)?.name || b.pots[0]?.name || t("flow.a_pot");
  const strategies = (S.meta?.strategies || ["avalanche", "snowball", "listed"]).map((s) => [s, t(`strategy.${s}`)]);
  const step = (what, detail, off = false) => `<li class="${off ? "off" : ""}"><span class="what">${what}</span><span class="detail">${detail}</span></li>`;
  $("#flow").innerHTML = [
    step(esc(t("flow.repay")), esc(t(owes ? "flow.repay.owes" : "flow.repay.none")), !owes),
    step(esc(t("flow.fixed")), fixed.length ? fixed.map((p) => fmtItem(p.name, p.monthly)).join(" · ") : esc(t("flow.fixed.none")), !fixed.length),
    step(esc(t("flow.extra")), esc(t(debts.length ? "flow.extra.some" : "flow.extra.none")) + `<br>
      <label class="inline">${esc(t("flow.share"))}<input data-path="extra_to_debt_pct" data-kind="decimal" inputmode="decimal" value="${esc(toInput(b.extra_to_debt_pct))}" aria-label="${esc(t("flow.share_aria"))}">%</label>
      ${debts.length > 1 ? `<label class="inline">${esc(t("flow.order"))}${select("", "debt_strategy", strategies).replace(/^<label class="f ">|<\/label>$/g, "")}</label>` : ""}`, !debts.length || !parseFloat(b.extra_to_debt_pct)),
    step(esc(t("flow.targets")), targets.length
      ? esc(t("flow.targets.list", { list: targets.map((p) => t("flow.targets.item", { name: p.name, amount: money({ amount: p.target, currency: b.base_currency }) })).join(" · ") }))
      : esc(t("flow.targets.none")), !targets.length),
    step(esc(t("flow.rest", { pot: def })), esc(t("flow.rest.detail"))),
  ].join("");
}

// scheduleText describes a withdrawal's schedule like the report does for items.
function scheduleText(s) {
  const label = (m) => S.report?.months[m]?.label || monthKey(m);
  if (!s || s.every === 0) return t("sched.once", { month: label(s?.start || 0) });
  const every = [1, 3, 12].includes(s.every) ? t(`sched.every.${s.every}`) : t("sched.every", { count: s.every });
  const from = t("sched.from", { schedule: every, month: label(s.start) });
  return s.end != null ? t("sched.until", { schedule: from, month: label(s.end) }) : from;
}

const EVERY_N = [0, 1, 2, 3, 6, 12];
const everyOptions = () => EVERY_N.map((n) => [n, t(`every.${n}`)]);

function renderEditors() {
  const b = S.budget;
  // Re-rendering replaces the inputs: keep the focus where it was.
  const ae = document.activeElement?.dataset || {};
  const active = { path: ae.path, row: ae.row };

  document.title = t("app.page_title", { name: b.name });
  $("#itemsText").textContent = t("items.text", { currency: b.base_currency });
  const crypto = new Set(S.meta?.crypto || []);
  $("#settings").innerHTML = [
    field(t("field.name"), "name"),
    select(t("field.base_currency"), "base_currency", (S.meta?.currencies || [b.base_currency]).filter((c) => !crypto.has(c)).map((c) => [c, c]), { structural: true }),
    field(t("field.first_month"), "start_month", { type: "month" }),
    select(t("field.months"), "months", [[6, "6"], [12, "12"], [24, "24"], [36, "36"], [60, "60"]].concat(
      [6, 12, 24, 36, 60].includes(b.months) ? [] : [[b.months, String(b.months)]])),
    field(t("field.overdraft_start", { currency: b.base_currency }), "overdraft_start", { type: "decimal", opt: true, attrs: 'placeholder="0"' }),
  ].join("");

  $("#refreshRates").hidden = !b.currencies.length || !S.prefs.fetch_rates;
  renderBudgetItems();
  renderItems();
  if (S.sheet && $("#sheet").open) renderSheet();
  renderWhatIf();
  renderItemNumbers();
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

// renderItems draws debts, pots, withdrawals, currencies and categories as lists. Their
// texts come partly from the report, so this also runs after every save.
function renderItems() {
  const b = S.budget, r = S.report;
  const debtRep = Object.fromEntries((r?.debts || []).map((d) => [d.id, d]));
  const crypto = new Set(S.meta?.crypto || []);

  $("#debts").innerHTML = b.debts.map((d, i) => {
    const rep = debtRep[d.id];
    const when = rep ? (rep.paid_off_label ? t("debt.paid_off", { month: rep.paid_off_label }) : parseFloat(d.balance) > 0 ? t("debt.not_paid") : "") : "";
    const payoff = d.payoff_month == null ? "" : t("debt.payoff", {
      month: r?.months?.[d.payoff_month]?.label || monthKey(d.payoff_month),
      from: b.pots.find((p) => p.id === d.payoff_from)?.name || t("debt.month_money"),
    });
    const meta = [t("debt.apr", { apr: number(d.apr_pct) }), t("debt.min", { amount: money({ amount: d.min_payment, currency: d.currency }) }), payoff, when];
    return itemRow({ kind: "debts", i, name: d.name, color: "var(--debt)",
      meta: esc(meta.filter(Boolean).join(" · ")),
      value: `${esc(toInput(fmtAmount(d.balance, d.currency)))} <small>${esc(d.currency)}</small>` });
  }).join("") || `<p class="muted pad">${esc(t("debts.empty"))}</p>`;

  const potRep = Object.fromEntries((r?.pots || []).map((x) => [x.id, x]));
  const base = (v) => money({ amount: v, currency: b.base_currency });
  $("#pots").innerHTML = b.pots.map((pt, i) => {
    const rep = potRep[pt.id], g = rep?.goal;
    const kind = potKind(pt.kind);
    const parts = pt.name.toLowerCase() === kind.toLowerCase() ? [] : [kind];
    if (rep) parts.push(t("pot.after", { amount: money(rep.end), count: S.report.months.length }));
    if (pt.target) {
      const target = base(pt.target);
      parts.push(!g ? t("pot.target", { amount: target }) : g.already_met ? t("pot.target_met")
        : g.reached_label ? t("pot.target_by", { amount: target, month: g.reached_label }) : t("pot.target_missed", { amount: target }));
    }
    if (pt.monthly) parts.push(t("pot.monthly", { amount: base(pt.monthly) }));
    if (rep?.default) parts.push(t("pot.default"));
    const fed = b.items.filter((l) => l.pot === pt.id);
    if (fed.length) parts.push(t("pot.fed", { names: fed.map((l) => l.name).join(", "), count: fed.length }));
    return itemRow({ kind: "pots", i, name: pt.name, color: POT_COLOR[pt.kind] || "var(--save)", meta: esc(parts.join(" · ")),
      value: `${esc(base(pt.balance))} <small>${esc(t("pot.now"))}</small>` });
  }).join("") || `<p class="muted pad">${esc(t("pots.empty"))}</p>`;
  renderFlow();

  $("#withdrawals").innerHTML = b.withdrawals.map((wd, i) => {
    const pot = b.pots.find((x) => x.id === wd.pot);
    return itemRow({ kind: "withdrawals", i, name: wd.name, color: "var(--accent)",
      meta: esc(t("wd.meta", { pot: pot?.name || wd.pot, schedule: scheduleText(wd.schedule) })), value: esc(base(wd.amount)) });
  }).join("") || `<p class="muted pad">${esc(t("wd.empty"))}</p>`;

  $("#currencies").innerHTML = b.currencies.map((c, i) => itemRow({ kind: "currencies", i,
    name: crypto.has(c.code) ? t("cur.crypto_row", { code: c.code }) : c.code, metaAttrs: `data-rate="${esc(c.code)}"`,
    value: `${esc(toInput(c.per_base))} <small>${esc(t("cur.per", { base: b.base_currency }))}</small>` })).join("")
    || `<p class="muted pad">${esc(t("currencies.only_base", { currency: b.base_currency }))}</p>`;

  $("#categories").innerHTML = b.categories.map((c, i) => {
    const n = b.items.filter((l) => l.category === c.id).length;
    return itemRow({ kind: "categories", i, name: c.label, color: c.color || "var(--line)",
      meta: esc(`${t(c.kind === "income" ? "kind.income" : "kind.expense")} · ${t("cat.items", { count: n })}`) });
  }).join("");
}

// ---------- items: grouped list + editor sheet ----------

// itemGroups orders items income first, then by expense category, as
// [{cat, items: [index, ...]}].
function itemGroups() {
  const b = S.budget;
  const order = [...b.categories.filter((c) => c.kind === "income"), ...b.categories.filter((c) => c.kind !== "income")];
  const groups = order.map((cat) => ({ cat, items: [] }));
  const byId = Object.fromEntries(groups.map((g) => [g.cat.id, g]));
  const other = { cat: { id: "", label: t("items.other_group"), color: "" }, items: [] };
  b.items.forEach((l, i) => (byId[l.category] || other).items.push(i));
  return [...groups, other].filter((g) => g.items.length);
}

function renderBudgetItems() {
  const b = S.budget;
  const rep = Object.fromEntries((S.report?.items || []).map((l) => [l.id, l]));
  $("#items").innerHTML = itemGroups().map(({ cat, items }) => `<div class="group">
      <div class="group-head"><span><span class="sw" style="background:${esc(cat.color || "var(--line)")}"></span>${esc(cat.label)}</span>
        <span class="total" data-total="${esc(cat.id)}"></span></div>
      ${items.map((i) => {
        const l = b.items[i];
        return `<div class="line" data-row="items.${i}" style="box-shadow:inset 3px 0 0 ${esc(cat.color || "var(--line)")}">
          <button type="button" class="line-main" data-action="edit" data-kind="items" data-i="${i}" aria-label="${esc(t("items.edit_aria", { name: l.name }))}">
            <span class="name">${esc(l.name)}</span><span class="meta">${esc(rep[l.id]?.schedule_text || "")}</span></button>
          <label class="amt"><span class="sr">${esc(t("items.amount_for", { name: l.name }))}</span>
            <input data-path="items.${i}.amount" data-kind="decimal" data-cur="${esc(l.currency)}" inputmode="decimal" autocomplete="off" value="${esc(toInput(fmtAmount(l.amount, l.currency)))}">
            <span class="cur">${esc(l.currency)}</span></label>
          <span class="chev-hit" data-action="edit" data-kind="items" data-i="${i}">${CHEVRON}</span>
          <span class="base" data-base="${esc(l.id)}"></span>
        </div>`;
      }).join("")}
    </div>`).join("") || `<div class="card"><p class="muted">${esc(t("items.empty"))}</p></div>`;
}

// renderItemNumbers updates the computed figures in place, so typing in an
// amount never loses focus when the report comes back.
function renderItemNumbers() {
  if (!S.report || !S.budget) return;
  const base = S.report.base_currency;
  const byId = Object.fromEntries(S.report.items.map((l) => [l.id, l]));
  document.querySelectorAll("[data-base]").forEach((el) => {
    const l = byId[el.dataset.base];
    if (!l) { el.textContent = ""; return; }
    const once = l.schedule.every === 0;
    const v = once ? l.per_occurrence_base : l.monthly_equivalent_base;
    const foreign = l.amount.currency !== base;
    el.textContent = once ? (foreign ? t("base.once_foreign", { amount: money(v) }) : t("base.once")) : foreign || l.schedule.every !== 1 ? t("base.per_month", { amount: money(v) }) : "";
  });
  document.querySelectorAll("[data-total]").forEach((el) => {
    const total = S.report.category_totals[el.dataset.total];
    el.innerHTML = total ? categoryTotalHTML(total) : "";
  });
  // Schedule texts come from the report too; add where a routed item goes.
  document.querySelectorAll('#items .line[data-row^="items."]').forEach((row) => {
    const l = S.budget.items[+row.dataset.row.split(".")[1]];
    const meta = row.querySelector(".meta");
    if (!l || !meta) return;
    const pot = l.pot && S.budget.pots.find((p) => p.id === l.pot);
    const route = pot ? " · " + t(byId[l.id]?.income ? "items.into_pot" : "items.from_pot", { pot: pot.name }) : "";
    meta.textContent = (byId[l.id]?.schedule_text || "") + route;
  });
  if ($("#sheet").open && S.sheet?.kind === "items") {
    const l = S.budget.items[S.sheet.i];
    const r = l && byId[l.id];
    const el = $("#sheetBase");
    if (el) el.textContent = r && r.amount.currency !== base
      ? t(r.income ? "sheet.base_income" : "sheet.base_cost", { amount: money(r.per_occurrence_base), base })
      : "";
  }
}

function categorySelect(path) {
  const v = getPath(S.budget, path);
  const group = (kind, label) => {
    const cs = S.budget.categories.filter((c) => (kind === "income") === (c.kind === "income"));
    return cs.length ? `<optgroup label="${esc(label)}">${opts(cs.map((c) => [c.id, c.label]), v)}</optgroup>` : "";
  };
  return `<label class="f">${esc(t("field.category"))}<select data-path="${esc(path)}" data-kind="text" data-structural>${group("income", t("kind.income"))}${group("expense", t("cat.costs"))}</select></label>`;
}

// potRouteSelect lets an item skip the month's budget: income goes straight
// into a pot, a cost is paid from one.
function potRouteSelect(l, i) {
  const income = S.budget.categories.find((c) => c.id === l.category)?.kind === "income";
  const opts2 = [["", t("route.month_budget")], ...S.budget.pots.map((p) => [p.id, p.name])];
  return `<label class="f wide">${esc(t(income ? "route.into" : "route.from"))}<select data-path="items.${i}.pot" data-kind="text" data-structural>${opts(opts2, l.pot || "")}</select></label>
    <p class="base">${esc(t(income ? "route.income_help" : "route.cost_help"))}</p>`;
}

// SHEETS describes the editor for each kind of item: its title, fields and
// the delete action. Fields use the same data-path inputs as everywhere else.
// noun is the catalog key for what one of them is called.
const SHEETS = {
  items: {
    noun: "noun.item", title: (l) => l.name,
    body(l, i) {
      const sch = l.schedule;
      return `${field(t("field.name"), `items.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${field(t("field.amount"), `items.${i}.amount`, { type: "decimal", value: fmtAmount(l.amount, l.currency) })}
        ${select(t("field.currency"), `items.${i}.currency`, currencyOptions(), { structural: true })}
        ${categorySelect(`items.${i}.category`)}
        ${select(t("field.repeats"), `items.${i}.schedule.every`, everyOptions(), { structural: true })}
        ${scheduleFields("items", i, sch)}
        ${potRouteSelect(l, i)}
        <p class="base" id="sheetBase"></p>`;
    },
  },
  debts: {
    noun: "noun.debt", title: (d) => d.name,
    body(d, i) {
      const from = [["", t("debt.month_money_option")], ...S.budget.pots.map((p) => [p.id, p.name])];
      return `${field(t("field.name"), `debts.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${field(t("field.balance"), `debts.${i}.balance`, { type: "decimal" })}
        ${select(t("field.currency"), `debts.${i}.currency`, currencyOptions(), { structural: true })}
        ${field(t("field.apr"), `debts.${i}.apr_pct`, { type: "decimal" })}
        ${field(t("field.min_payment"), `debts.${i}.min_payment`, { type: "decimal" })}
        <p class="base">${esc(t("debt.help_interest"))}</p>
        <label class="f">${esc(t("field.payoff"))}<input type="month" data-path="debts.${i}.payoff_month" data-kind="monthidx-opt" data-structural value="${d.payoff_month == null ? "" : monthKey(d.payoff_month)}"></label>
        ${d.payoff_month == null ? "" : `<label class="f">${esc(t("field.paid_from"))}<select data-path="debts.${i}.payoff_from" data-kind="text" data-structural>${opts(from, d.payoff_from || "")}</select></label>`}
        <p class="base">${esc(t("debt.help_payoff"))}</p>`;
    },
  },
  pots: {
    noun: "noun.pot", title: (p) => p.name,
    body(p, i) {
      const currency = S.budget.base_currency;
      const none = `placeholder="${esc(t("field.none"))}"`;
      const kindHelp = POT_KINDS.includes(p.kind) ? t(`pot.help.${p.kind}`) : "";
      return `${field(t("field.name"), `pots.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${select(t("field.kind"), `pots.${i}.kind`, POT_KINDS.map((k) => [k, potKind(k)]), { structural: true })}
        ${field(t("field.holds_now", { currency }), `pots.${i}.balance`, { type: "decimal" })}
        ${field(t("field.target_optional", { currency }), `pots.${i}.target`, { type: "decimal", opt: true, attrs: none })}
        ${field(t("field.monthly_optional", { currency }), `pots.${i}.monthly`, { type: "decimal", opt: true, attrs: none })}
        <div class="wide row-actions" style="margin-top:0">
          <button type="button" data-action="move-pot" data-i="${i}" data-dir="-1" ${i === 0 ? "disabled" : ""}>${esc(t("pot.move_up"))}</button>
          <button type="button" data-action="move-pot" data-i="${i}" data-dir="1" ${i === S.budget.pots.length - 1 ? "disabled" : ""}>${esc(t("pot.move_down"))}</button>
        </div>
        <p class="base">${esc([t("pot.help.order"), kindHelp, t("pot.help.target")].filter(Boolean).join(" "))}</p>`;
    },
  },
  withdrawals: {
    noun: "noun.withdrawal", title: (w) => w.name,
    body(w, i) {
      const sch = w.schedule || { start: 0, every: 0 };
      return `${field(t("field.name"), `withdrawals.${i}.name`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${select(t("field.from_pot"), `withdrawals.${i}.pot`, S.budget.pots.map((p) => [p.id, p.name]), { structural: true })}
        ${field(t("field.amount_in", { currency: S.budget.base_currency }), `withdrawals.${i}.amount`, { type: "decimal" })}
        ${select(t("field.repeats"), `withdrawals.${i}.schedule.every`, everyOptions(), { structural: true })}
        ${scheduleFields("withdrawals", i, sch)}
        <p class="base">${esc(t("wd.help"))}</p>`;
    },
  },
  currencies: {
    noun: "noun.currency", title: (c) => c.code,
    body(c, i) {
      const crypto = new Set(S.meta?.crypto || []);
      const codes = (S.meta?.currencies || []).filter((x) => x !== S.budget.base_currency).map((x) => [x, crypto.has(x) ? t("cur.crypto_option", { code: x }) : x]);
      return `${select(t("field.code"), `currencies.${i}.code`, codes, { structural: true })}
        ${select(t("field.rate_from"), `currencies.${i}.source`, rateSourceOptions(), { structural: true })}
        ${field(t("field.per_base", { currency: S.budget.base_currency }), `currencies.${i}.per_base`, { type: "decimal" })}
        ${field(t("field.payment_loss"), `currencies.${i}.transfer_fee_pct`, { type: "decimal" })}
        ${field(t("field.cashout_loss"), `currencies.${i}.cash_out_fee_pct`, { type: "decimal", attrs: 'placeholder="0"' })}
        <p class="base" data-rate="${esc(c.code)}"></p>
        <p class="base">${esc(t("cur.help"))}</p>`;
    },
  },
  categories: {
    noun: "noun.category", title: (c) => c.label,
    body(c, i) {
      return `${field(t("field.label"), `categories.${i}.label`, { cls: "wide", attrs: 'autocomplete="off"' })}
        ${select(t("field.kind"), `categories.${i}.kind`, [["expense", t("kind.expense")], ["income", t("kind.income")]], { structural: true })}
        <label class="f">${esc(t("field.colour"))}<input type="color" data-path="categories.${i}.color" data-kind="text" value="${esc(c.color || "#888888")}"></label>`;
    },
  },
};

// scheduleFields are the month inputs under "Repeats": the month of a
// one-off, or the first and (optional) last month of a repeating one.
function scheduleFields(list, i, sch) {
  const start = `<label class="f">${esc(t(sch.every === 0 ? "field.in" : "field.from"))}<input type="month" data-path="${list}.${i}.schedule.start" data-kind="monthidx" value="${monthKey(sch.start)}"></label>`;
  if (sch.every === 0) return start + "<span></span>";
  return start + `<label class="f">${esc(t("field.until_optional"))}<input type="month" data-path="${list}.${i}.schedule.end" data-kind="monthidx-opt" value="${sch.end == null ? "" : monthKey(sch.end)}"></label>`;
}

function renderSheet() {
  const { kind, i } = S.sheet;
  const item = S.budget[kind]?.[i];
  const def = SHEETS[kind];
  if (!item || !def) { closeSheet(); return; }
  $("#sheetTitle").textContent = def.title(item) || t(def.noun);
  $("#sheetBody").innerHTML = `<div class="sheet-body">${def.body(item, i)}</div>
    <div class="sheet-foot">
      <button type="button" class="danger" data-action="delete-item" data-kind="${kind}" data-i="${i}">${esc(t("sheet.delete", { what: t(def.noun) }))}</button>
      <button type="button" class="primary" data-action="close-sheet">${esc(t("sheet.done"))}</button>
    </div>`;
  renderItemNumbers();
  renderRateNotes();
}

function openSheet(kind, i, { focusFirst = false } = {}) {
  S.sheet = { kind, i };
  renderSheet();
  const d = $("#sheet");
  if (!d.open) d.showModal();
  renderItemNumbers(); // the base-currency note needs the sheet open
  // A new item starts on its name; an existing item on its amount.
  const first = focusFirst ? d.querySelector("#sheetBody input, #sheetBody select")
    : kind === "items" ? d.querySelector(`[data-path="items.${i}.amount"]`) : null;
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
    case "decimal": {
      if (v.trim() === "" && el.hasAttribute("data-optional")) { v = undefined; el.classList.remove("invalid"); break; }
      const d = parseDecimal(v); // "12,50" in a decimal-comma locale
      el.classList.toggle("invalid", d === null);
      if (d === null) return; // keep editing; never send a malformed amount
      v = d;
      break;
    }
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
  if (/^(items\.\d+\.pot|debts\.\d+\.payoff_from)$/.test(path) && v === "") v = undefined;
  // Without a payoff month there is nothing to pay it from.
  const payoff = path.match(/^debts\.(\d+)\.payoff_month$/);
  if (payoff && v === undefined) delete S.budget.debts[+payoff[1]].payoff_from;
  if (path.startsWith("withdrawals.") && path.endsWith(".schedule.every") && v === 0) delete getPath(S.budget, path.replace(/\.every$/, "")).end;
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
    document.querySelectorAll(`[data-path="${CSS.escape(path)}"]`).forEach((o) => { if (o !== el) o.value = item[3] === "amount" ? toInput(v) : v; });
    if (item[3] !== "amount") {
      document.querySelectorAll(`[data-row="${item[1]}.${item[2]}"] .name`).forEach((n) => { n.textContent = v; });
      if (S.sheet?.kind === item[1] && S.sheet.i === +item[2]) $("#sheetTitle").textContent = v || (SHEETS[item[1]] ? t(SHEETS[item[1]].noun) : "");
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
  let text = t("rates.updated", { list: changed.join(", ") });
  if (failed.length) text += " · " + t("rates.failed", { list: failed.join(", ") });
  if (changed.length) status(text, failed.length > 0);
}

function renderRateNotes() {
  if (!S.report || !S.budget) return;
  const unit = Object.fromEntries((S.report.currencies || []).map((c) => [c.code, c]));
  document.querySelectorAll("[data-rate]").forEach((el) => {
    const c = S.budget.currencies.find((x) => x.code === el.dataset.rate);
    const u = unit[el.dataset.rate];
    if (!c) return;
    const worth = u ? t("rate.worth", { units: number(u.units), code: c.code, value: money(u.base_value, { exact: true }) }) : "";
    const provider = ({ ecb: "ECB", coingecko: "CoinGecko" })[c.rate_from] || c.rate_from;
    const from = isManual(c) ? t("rate.typed") : c.rate_date ? t("rate.of", { source: provider, date: date(c.rate_date) }) : t("rate.not_fetched");
    el.textContent = [worth, from].filter(Boolean).join(" · ");
  });
}

const ACTIONS = {
  "add-item"() {
    const b = S.budget;
    const cat = b.categories.find((c) => c.kind === "expense") || b.categories[0];
    if (!cat) { setView("setup"); status(t("status.need_category"), true); return; }
    const name = t("items.new");
    b.items.push({ id: newId(name, b.items), name, amount: "0", currency: b.base_currency, category: cat.id, schedule: { start: 0, every: 1 } });
    edited({ structural: true });
    openSheet("items", b.items.length - 1, { focusFirst: true });
  },
  edit(i, el) { openSheet(el.dataset.kind, i); },
  "close-sheet"() { closeSheet(); },
  "delete-item"(i, el) {
    const kind = el.dataset.kind;
    const item = S.budget[kind][i];
    if (kind === "currencies") {
      const user = [...S.budget.items, ...S.budget.debts].find((x) => x.currency === item.code);
      if (user) { status(t("cur.in_use", { code: item.code, name: user.name }), true); return; }
    }
    if (kind === "pots") {
      const w = S.budget.withdrawals.find((x) => x.pot === item.id);
      if (w) { status(t("pot.in_use.transfer", { name: item.name, other: w.name }), true); return; }
      const l = S.budget.items.find((x) => x.pot === item.id);
      if (l) { status(t("pot.in_use.item", { name: item.name, other: l.name }), true); return; }
      const d = S.budget.debts.find((x) => x.payoff_from === item.id);
      if (d) { status(t("pot.in_use.debt", { name: item.name, other: d.name }), true); return; }
      if (S.budget.pots.length === 1) { status(t("pot.keep_one"), true); return; }
    }
    if (kind === "categories" && S.budget.items.some((l) => l.category === item.id)) {
      status(t("categories.in_use", { label: item.label }), true);
      return;
    }
    removeWithUndo(kind, i, kind === "currencies" ? item.code : t("quoted", { name: item.name || item.label }));
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
    const name = t("debts.new");
    b.debts.push({ id: newId(name, b.debts), name, currency: b.base_currency, balance: "0", apr_pct: "0", min_payment: "0" });
    edited({ structural: true });
    openSheet("debts", b.debts.length - 1, { focusFirst: true });
  },
  "add-pot"() {
    const b = S.budget;
    const name = t("pots.new");
    b.pots.push({ id: newId(name, b.pots), name, kind: "sinking", balance: "0" });
    edited({ structural: true });
    openSheet("pots", b.pots.length - 1, { focusFirst: true });
  },
  "add-withdrawal"() {
    const b = S.budget;
    if (!b.pots.length) { status(t("status.need_pot"), true); return; }
    const name = t("wd.new");
    b.withdrawals.push({ id: newId(name, b.withdrawals), name, pot: b.pots[0].id, amount: "100", schedule: { start: 0, every: 0 } });
    edited({ structural: true });
    openSheet("withdrawals", b.withdrawals.length - 1, { focusFirst: true });
  },
  "move-pot"(i, el) {
    const pots = S.budget.pots, j = i + Number(el.dataset.dir);
    if (j < 0 || j >= pots.length) return;
    [pots[i], pots[j]] = [pots[j], pots[i]];
    S.sheet = { kind: "pots", i: j };
    edited({ structural: true });
  },
  // cover plans a one-off withdrawal for a short month, from the chosen pot.
  cover(i, el) {
    const b = S.budget;
    const month = +el.dataset.month, pot = b.pots.find((p) => p.id === el.dataset.pot);
    if (!pot) return;
    const label = S.report.months[month]?.label || monthKey(month);
    const w = { id: newId(`cover-${monthKey(month)}`, b.withdrawals), name: t("cover.name", { month: label }), pot: pot.id, amount: el.dataset.amount, schedule: { start: month, every: 0 } };
    b.withdrawals.push(w);
    edited({ structural: true });
    toast(t("cover.toast", { amount: money({ amount: w.amount, currency: b.base_currency }), pot: pot.name, month: label }), () => {
      const at = S.budget.withdrawals.indexOf(w);
      if (at >= 0) { S.budget.withdrawals.splice(at, 1); edited({ structural: true }); }
    });
  },
  "add-category"() {
    const b = S.budget;
    const label = t("categories.new");
    b.categories.push({ id: newId(label, b.categories), label, kind: "expense", color: "#7a6ff0" });
    edited({ structural: true });
    openSheet("categories", b.categories.length - 1, { focusFirst: true });
  },
  "all-insights"() { S.allInsights = !S.allInsights; renderInsights(); },
  new() { openNewDialog(); },
  export() { exportJSON(); },
  "add-wi"() { S.wi.push({ kind: "item", target: S.budget.items[0]?.id || "", value: "" }); renderWhatIf(); },
  "del-wi"(i) { S.wi.splice(i, 1); renderWhatIf(); },
  "preview-wi"() { previewWhatIf(); },
  "save-wi"() { saveWhatIf(); },
  undo(i, el) { undoChange(i, el); },
  async "adopt-scenario"(i, el) {
    if (!confirm(t("scenarios.adopt_confirm", { name: el.dataset.name }))) return;
    await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/scenarios/${encodeURIComponent(el.dataset.sid)}/adopt`, {}));
  },
  async "del-scenario"(i, el) {
    if (!confirm(t("scenarios.delete_confirm", { name: el.dataset.name }))) return;
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
  // The headline is what's still uncovered after any planned withdrawals.
  const short = parseFloat(p.m0.short.amount) > 0;
  const neg = short;
  const from = parseFloat(p.m0.from_savings.amount) > 0;
  const head = short ? money(p.m0.short) : money({ ...p.m0.left, amount: p.m0.left.amount.replace(/^-/, "") });
  const month = monthName(p.m0.month);
  const state = t(neg ? "summary.short_in" : "summary.left_in", { month });
  const notIn = t("summary.not_in", { count: p.n });
  const goalTxt = p.goal ? (p.goal.already_met ? t("summary.already_met") : p.goal.reached_label || notIn) : "";
  const debtTxt = p.debtFree === null ? "" : p.debtFree < 0 ? notIn : r.months[p.debtFree].label;
  const fromSavings = money(p.m0.from_savings);
  const sub = short
    ? (from ? t("summary.after_savings", { amount: fromSavings }) + " · " : "") + t("summary.nothing_else")
    : from ? t("summary.after_planned", { amount: fromSavings })
    : t("summary.of_income", { income: money(p.m0.income), saved: money(p.m0.to_savings) });
  const savingsIn = t("summary.savings_in", { count: p.n });
  const html = `<h2>${esc(state)}</h2>
    <div class="big ${neg ? "neg" : ""}">${esc(head)}</div>
    <div class="sub">${esc(sub)}</div>
    <dl>
      <dt>${esc(savingsIn)}</dt><dd>${esc(money(p.last.savings))}</dd>
      ${p.goal ? `<dt>${esc(p.goal.name)}</dt><dd>${esc(goalTxt)}</dd>` : ""}
      ${p.debtFree !== null ? `<dt>${esc(t("summary.debt_free"))}</dt><dd>${esc(debtTxt)}</dd>` : ""}
      ${p.debts.length ? `<dt>${esc(t("summary.interest", { count: p.n }))}</dt><dd>${esc(money(r.totals.total_interest_base))}</dd>` : ""}
      ${parseFloat(p.last.overdraft.amount) > 0 ? `<dt>${esc(t("summary.still_owed", { count: p.n }))}</dt><dd class="neg">${esc(money(p.last.overdraft))}</dd>` : ""}
    </dl>
    ${p.warn?.id === "shortfall"
      ? `<div class="callout">${esc(t("summary.pots_callout"))} <button type="button" class="linkbtn" data-view-link="overview">${esc(t("summary.see_options"))}</button></div>`
      : p.warn ? `<div class="callout">${bold(p.warn.text)}</div>` : ""}`;
  // Flash the summary when an edit changed it, so the effect is noticed.
  const changed = S.lastSummary && S.lastSummary !== html;
  S.lastSummary = html;
  if (changed) announce(t("summary.announce", { state, amount: head, savings_label: savingsIn, savings: money(p.last.savings) }));
  for (const el of [$("#railSummary"), $("#mobileSummary")]) {
    el.innerHTML = html;
    if (changed) { el.classList.remove("flash"); void el.offsetWidth; el.classList.add("flash"); }
  }
  const mb = $("#minibar");
  mb.innerHTML = `<span>${esc(state)} <b class="${neg ? "neg" : ""}">${esc(head)}</b></span>
    <span>${esc(t("summary.in_months", { count: p.n }))} <b>${esc(money(p.last.savings))}</b></span>`;
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
const RISK_RE = /^(crypto_income|emergency_fund|debt_min_)/;

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
  const items = r.insights.filter((x) => !FX_RE.test(x.id) && x.id !== "shortfall").map((x) => ({ ...x }));
  if (fx.length) items.push({ tone: fx.some((x) => x.tone === "warn") ? "warn" : "info", group: fx });
  items.sort((a, b) => relevance(a) - relevance(b));
  const shown = S.allInsights ? items : items.slice(0, 3);
  const card = (x) => x.group
    ? `<details class="ins ${esc(x.tone)}"><summary data-show="${esc(t("ins.show"))}" data-hide="${esc(t("ins.hide"))}"><span><b>${esc(t("ins.fx_title"))}</b> · ${esc(t("ins.fx_count", { count: x.group.length }))}</span></summary>
        <div class="ins-group">${x.group.map((g) => `<p>${bold(g.text)}</p>`).join("")}</div></details>`
    : `<div class="ins ${esc(x.tone)}">${bold(x.text)}</div>`;
  $("#insights").innerHTML = (shown.map(card).join("") || `<p class="muted">${esc(t("insights.empty"))}</p>`) +
    (items.length > 3 ? `<button type="button" class="linkbtn" data-action="all-insights" aria-expanded="${S.allInsights}">${esc(S.allInsights ? t("ins.fewer") : t("ins.all", { count: items.length }))}</button>` : "");
}

function isZeroMoney(value) {
  return parseFloat(value.amount) === 0;
}

function categoryTotalHTML(total) {
  const hasMonthly = !isZeroMoney(total.monthly_equivalent_base);
  const hasOneOff = !isZeroMoney(total.one_off_base);
  const monthly = esc(money(total.monthly_equivalent_base));
  const oneOff = esc(money(total.one_off_base));
  const perMonth = esc(t("unit.per_month"));
  const once = esc(t("unit.once"));
  const parts = [];
  if (hasMonthly || !hasOneOff) parts.push(`${monthly}<small>${perMonth}</small>`);
  if (hasOneOff) parts.push(`${oneOff}<small>${once}</small>`);
  return parts.join(" + ");
}

function renderOutputs() {
  const r = S.report;
  if (!r) return;
  renderSummary();
  renderRateNotes();
  const m0 = r.months[0];
  const cats = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#h-month").textContent = t("month.heading", { month: monthName(m0.month) });
  $("#incomeBig").textContent = money(m0.income);
  $("#h-proj").textContent = t("projection.heading", { count: r.months.length });

  const num = (m) => Math.max(parseFloat(m.amount), 0); // pixels only
  const segs = Object.entries(m0.by_category)
    .filter(([, v]) => parseFloat(v.amount) > 0)
    .map(([id, v]) => [cats[id]?.label || id, v, cats[id]?.color || "var(--accent)", ""]);
  if (num(m0.debt_payments) > 0) segs.push([t("split.debt"), m0.debt_payments, "var(--debt)", "hatch"]);
  if (num(m0.income_to_pots) > 0) segs.push([t("split.straight"), m0.income_to_pots, "var(--save)", "hatch-save"]);
  const intoSavings = m0.left_after_routing;
  if (num(intoSavings) > 0) segs.push([t("split.savings"), intoSavings, "var(--save)", ""]);
  const tot = segs.reduce((a, s) => a + num(s[1]), 0) || 1;
  const inc = parseFloat(m0.income.amount) || 0;
  $("#bar").innerHTML = segs.map((s) => `<div class="${s[3]}" style="flex-basis:${(num(s[1]) / tot) * 100}%;background:${esc(s[2])}" title="${esc(s[0])}: ${esc(money(s[1]))}"></div>`).join("");
  $("#legend").innerHTML = segs.map((s) => `<div><span class="sw ${s[3]}" style="background:${esc(s[2])}"></span>${esc(s[0])}<b>${esc(money(s[1]))}${inc > 0 ? `<small>${Math.round((num(s[1]) / inc) * 100)}%</small>` : ""}</b></div>`).join("");

  renderInsights();
  renderShortfall();
  // Debts, pots and currencies show report figures (payoff dates, rates).
  const ae = document.activeElement?.dataset || {};
  renderItems();
  renderRateNotes(); // fills the currency rows just drawn
  if (ae.row) restoreFocus({ row: ae.row });
  drawChart();

  const hasDebt = r.debts.length > 0;
  const hasFrom = r.months.some((m) => parseFloat(m.from_savings.amount) > 0);
  const hasOwed = r.months.some((m) => parseFloat(m.overdraft.amount) > 0);
  const cols = ["month", "income", "spent", "to_debts", "left", hasFrom && "from_savings", hasOwed && "owed", "saved", "savings", hasDebt && "debt"];
  let h = `<thead><tr>${cols.filter(Boolean).map((c) => `<th scope="col">${esc(t(`table.${c}`))}</th>`).join("")}</tr></thead><tbody>`;
  for (const m of r.months) {
    const neg = (x) => (parseFloat(x.amount) < 0 ? "neg" : "");
    const owed = parseFloat(m.overdraft.amount) > 0;
    h += `<tr><th scope="row">${esc(m.label)}</th><td>${esc(money(m.income))}</td><td>${esc(money(m.expenses))}</td><td>${esc(money(m.debt_payments))}</td><td class="${neg(m.left)}">${esc(money(m.left))}</td>${hasFrom ? `<td>${esc(money(m.from_savings))}</td>` : ""}${hasOwed ? `<td class="${owed ? "neg" : ""}">${esc(money(m.overdraft))}</td>` : ""}<td class="${neg(m.saved)}">${esc(money(m.saved))}</td><td>${esc(money(m.savings))}</td>${hasDebt ? `<td>${esc(money(m.debt_balance_base))}</td>` : ""}</tr>`;
  }
  $("#table").innerHTML = h + "</tbody>";
  renderItemNumbers();
  $("#minibar").hidden = S.view === "overview";
}

// renderShortfall shows the first short month: savings are not touched, but
// the pots that could cover it are one tap away (general first, emergency last).
function renderShortfall() {
  const card = $("#shortfall");
  const sfs = S.report.shortfalls || [];
  card.hidden = !sfs.length;
  if (!sfs.length) { card.innerHTML = ""; return; }
  const sf = sfs[0];
  const more = sfs.length - 1;
  const note = (o) => ["sinking", "emergency"].includes(o.kind) ? t(`short.note.${o.kind}`)
    : o.covers.amount === sf.amount.amount ? t("short.covers_all") : t("short.covers_part", { amount: money(o.covers) });
  card.innerHTML = `<h2 id="h-short">${esc(t("short.title", { month: sf.label, amount: money(sf.amount) }))}</h2>
    <p class="muted">${esc(t("short.untouched"))}${more ? " " + esc(t("short.more", { count: more })) : ""}</p>
    ${sf.options.length ? `<p style="margin-top:10px">${esc(t("short.cover_from"))}</p>
    <div class="opts">${sf.options.map((o) => `<button type="button" class="opt ${o.kind === "emergency" ? "last" : ""}" data-action="cover"
        data-pot="${esc(o.pot)}" data-month="${sf.month}" data-amount="${esc(o.covers.amount)}">
        <span>${esc(o.name)} <small>· ${esc(t("short.there", { amount: money(o.available) }))}</small></span>
        <small>${esc(note(o))}</small>
        <b>${esc(t("short.use", { amount: money(o.covers) }))}</b></button>`).join("")}</div>` : `<p style="margin-top:10px">${esc(t("short.no_pots"))}</p>`}
    <p class="muted foot">${esc(t("short.owed_note"))}</p>`;
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
  const debt = r.months.map((m) => parseFloat(m.debt_balance_base.amount) + parseFloat(m.overdraft.amount));
  const hasDebt = debt.some((v) => v > 0);
  const all = sav.concat(hasDebt ? debt : []);
  $("#debtLegend").textContent = t(r.months.some((m) => parseFloat(m.overdraft.amount) > 0) ? "projection.debt_owed" : "projection.debt");
  const ticks = niceTicks(Math.min(0, ...all), Math.max(100, ...all), compact ? 3 : 4);
  const lo = ticks[0], hi = ticks[ticks.length - 1];
  const cur = r.base_currency;
  const fmt = { format: (v) => moneyAxis(v, cur, compact) };
  // Room for the longest axis label: "6 тыс. €" is longer than "€6K".
  const L = Math.max(compact ? 48 : 70, 14 + 7 * Math.max(...ticks.map((v) => fmt.format(v).length)));
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
    const row = (key, v) => `<span>${esc(t(key))} <strong>${esc(money(v))}</strong></span>`;
    tip.innerHTML = `<b>${esc(m.label)}</b>${row("table.savings", m.savings)}${parseFloat(m.debt_balance_base.amount) > 0 ? row("table.debt", m.debt_balance_base) : ""}${parseFloat(m.overdraft.amount) > 0 ? row("table.owed", m.overdraft) : ""}${row("table.left", m.left)}`;
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

const WI_KINDS = ["item", "rate", "extra", "remove"];

function renderWhatIf() {
  const b = S.budget;
  const itemOpts = b.items.map((l) => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join("");
  const curOpts = b.currencies.map((c) => `<option value="${esc(c.code)}">${esc(t("scenarios.now", { code: c.code, rate: number(c.per_base) }))}</option>`).join("");
  const kinds = (o) => WI_KINDS.map((k) => `<option value="${k}" ${k === o.kind ? "selected" : ""}>${esc(t(`scenarios.kind.${k}`))}</option>`).join("");
  $("#wiOps").innerHTML = S.wi.map((o, i) => `<div class="row row-wi">
      <label class="f">${esc(t("scenarios.change"))}<select data-wi="${i}" data-k="kind">${kinds(o)}</select></label>
      ${o.kind === "extra" ? "<span></span>" : `<label class="f">${esc(t(o.kind === "rate" ? "field.currency" : "noun.item_title"))}<select data-wi="${i}" data-k="target">${o.kind === "rate" ? curOpts : itemOpts}</select></label>`}
      ${o.kind === "remove" ? "<span></span>" : `<label class="f">${esc(t("scenarios.new_value"))}<input data-wi="${i}" data-k="value" inputmode="decimal" value="${esc(o.value)}"></label>`}
      ${del("del-wi", i, t("scenarios.a_change"))}
    </div>`).join("") || `<p class="muted">${esc(t("scenarios.empty"))}</p>`;
  $("#previewBtn").disabled = !S.wi.length;
  S.wi.forEach((o, i) => { const t = document.querySelector(`[data-wi="${i}"][data-k="target"]`); if (t) { if (o.target) t.value = o.target; o.target = t.value; } });
}

function wiOps() {
  return S.wi.map((o) => {
    const value = parseDecimal(o.value ?? "");
    if (o.kind !== "remove" && value === null) throw new Error(t("scenarios.bad_value"));
    switch (o.kind) {
      case "item": return { op: OPS.setItem, target: o.target, value: { amount: value } };
      case "rate": return { op: OPS.setCurrency, target: o.target, value: { per_base: value } };
      case "extra": return { op: OPS.setSettings, value: { extra_to_debt_pct: value } };
      case "remove": return { op: OPS.removeItem, target: o.target };
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
    const goalTxt = after.goals.map((g) => `${g.name}: ${g.already_met ? t("scenarios.goal_met") : g.reached_label || t("scenarios.goal_not")}`).join(" · ");
    const result = tHTML("scenarios.result", {
      count: after.months.length, after: money(after.totals.end_savings), before: money(before.totals.end_savings), saved: money(after.months[0].saved),
    });
    const name = esc(t("scenarios.name"));
    out.innerHTML = `<div class="ins ${endA >= endB ? "good" : "warn"}" style="margin-top:12px">${result}${goalTxt ? "<br>" + esc(goalTxt) : ""}</div>
      <div class="row-actions"><input id="wiName" placeholder="${name}" aria-label="${name}" style="flex:1;min-width:160px"><button type="button" data-action="save-wi">${esc(t("scenarios.save"))}</button></div>`;
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

// ---------- this month: plan versus actual ----------

async function loadMonthStatus() {
  if (!S.id) return;
  const id = encodeURIComponent(S.id);
  let monthStatus, transactions;
  try {
    [monthStatus, transactions] = await Promise.all([
      api("GET", `/api/budgets/${id}/status`),
      api("GET", `/api/budgets/${id}/transactions?limit=8`),
    ]);
  } catch {
    $("#monthStatusSection").hidden = true; // e.g. today is outside the budget's window
    return;
  }
  const categories = Object.fromEntries(S.budget.categories.map((c) => [c.id, c]));
  $("#monthStatusSection").hidden = false;
  $("#h-month-status").textContent = t("month_status.heading", { month: monthName(monthStatus.month) });
  $("#monthStatusSub").textContent = monthStatusSubtitle(monthStatus);
  $("#monthStatusFlags").innerHTML = monthStatus.flags.map((flag) => `<div class="ins ${esc(flag.tone)}">${bold(flag.text)}</div>`).join("");
  $("#monthStatusCategories").innerHTML = monthStatus.categories
    .filter((row) => row.kind !== "income")
    .map((row) => renderCategoryBar(row, categories[row.category]))
    .join("");
  $("#monthStatusTransactions").innerHTML = renderRecentTransactions(transactions, categories);
}

function monthStatusSubtitle(monthStatus) {
  if (!monthStatus.transaction_count) return t("month_status.empty");
  const sums = {
    spent: money(monthStatus.spent),
    planned: money(monthStatus.planned_spending),
    day: monthStatus.days_elapsed,
    days: monthStatus.days_in_month,
  };
  return t("month_status.sub", sums);
}

// renderCategoryBar is one spending category's bar: how much of the plan is spent.
function renderCategoryBar(row, category) {
  const width = spentPercent(row);
  const color = barColor(row, category);
  const badge = row.state === "ok" ? "" : `<span class="badge ${esc(row.state)}">${esc(t(`month_status.state.${row.state}`))}</span>`;
  const ofPlan = t("month_status.of", { actual: money(row.actual), planned: money(row.planned) });
  return `<div class="category-bar-row">
    <div class="category-bar-head"><span>${esc(row.label)} ${badge}</span><span class="muted">${esc(ofPlan)}</span></div>
    <div class="category-bar"><div style="width:${width}%;background:${esc(color)}"></div></div>
  </div>`;
}

// spentPercent is the share of the plan spent, in percent, capped at 100.
// Floats are fine here: they only size a bar.
function spentPercent(row) {
  const planned = parseFloat(row.planned.amount);
  const actual = parseFloat(row.actual.amount);
  if (planned > 0) return Math.min(100, (actual / planned) * 100);
  if (actual > 0) return 100;
  return 0;
}

function barColor(row, category) {
  const overspent = row.state === "over" || row.state === "unplanned";
  if (overspent) return "var(--neg)";
  return category?.color || "var(--accent)";
}

function renderRecentTransactions(transactions, categories) {
  if (!transactions.length) return `<tr><td class="muted">${esc(t("month_status.none_logged"))}</td></tr>`;
  return transactions.map((transaction) => {
    const label = categories[transaction.category]?.label || transaction.category;
    const amount = money({ amount: transaction.amount, currency: transaction.currency });
    return `<tr><td>${esc(transaction.date)}</td><td>${esc(label)}</td><td>${esc(amount)}</td><td class="muted">${esc(transaction.note || "")}</td></tr>`;
  }).join("");
}

// ---------- activity ----------

// Who made a change, as the history records it (see store.Change).
const SOURCES = ["web", "mcp", "assistant", "cli", "auto-refresh", "earlier"];
const sourceLabel = (s) => t(SOURCES.includes(s) ? `source.${s}` : "source.unknown");

function ago(iso) {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t("activity.just_now");
  if (s < 3600) return t("activity.min_ago", { count: Math.floor(s / 60) });
  if (s < 86400) return t("activity.h_ago", { count: Math.floor(s / 3600) });
  return date(iso);
}

async function loadActivity() {
  if (!S.id) return;
  const box = $("#activity");
  try {
    S.history = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/history?limit=15`);
  } catch {
    return; // keep what is shown; the next refresh retries
  }
  box.innerHTML = S.history.map(activityRow).join("") || `<li class="muted">${esc(t("activity.empty"))}</li>`;
}

// activityRow is one change in the Recent changes list: who, what, when, and
// an Undo button when there is an earlier version to go back to. i is its
// place in S.history, newest first.
function activityRow(e, i) {
  const who = sourceLabel(e.source);
  const when = `r${e.revision} · ${ago(e.at)}`;
  const undo = e.undoable ? undoButton(e.revision, i) : "<span></span>";
  return `<li class="act ${esc(e.source)}">
      <span class="who">${esc(who)}</span>
      <span class="what">${esc(e.summary)}</span>
      <span class="when muted" title="${esc(dateTime(e.at))}">${esc(when)}</span>
      ${undo}
    </li>`;
}

function undoButton(revision, i) {
  const data = `data-action="undo" data-rev="${revision}" data-i="${i}"`;
  return `<button type="button" class="linkish" ${data}>${esc(t("activity.undo"))}</button>`;
}

function confirmUndoingLater(later) {
  const changes = later.map((e) => `• ${e.summary}`).join("\n");
  return confirm(t("activity.undo_confirm", { count: later.length, list: changes }));
}

async function undoChange(i, el) {
  const rev = Number(el.dataset.rev);
  const later = S.history.slice(0, i);
  if (later.length && !confirmUndoingLater(later)) return;
  // seen is the newest change the person was shown: if more came in since,
  // the server refuses again instead of undoing something nobody saw.
  const req = { revision: rev, force: later.length > 0, seen: S.history[0].revision };
  await serverChange(() => api("POST", `/api/budgets/${encodeURIComponent(S.id)}/undo`, req));
  loadActivity(); // also after a refusal, so the list shows what came in
}

async function loadCompare() {
  const table = $("#compare");
  if (!S.id) return;
  try {
    const rows = await api("GET", `/api/budgets/${encodeURIComponent(S.id)}/compare`);
    $("#compareEmpty").hidden = rows.length >= 2;
    if (rows.length < 2) { table.innerHTML = ""; return; }
    const head = ["scenario", "this_month", "end_savings", "delta", "interest", "goals"].map((c) => `<th scope="col">${esc(t(`compare.${c}`))}</th>`).join("");
    let h = `<thead><tr>${head}<th scope="col"><span class="sr">${esc(t("compare.actions"))}</span></th></tr></thead><tbody>`;
    for (const o of rows) {
      if (o.error) { h += `<tr><th scope="row">${esc(o.name)}</th><td colspan="5" class="neg">${esc(o.error)}</td><td>${scenarioButtons(o)}</td></tr>`; continue; }
      const d = o.delta_end_savings;
      const goals = (o.goals || []).map((g) => g.already_met ? t("compare.met") : g.reached_label || "—").join(", ");
      h += `<tr><th scope="row">${esc(o.name)}</th><td>${esc(money(o.first_month_saved))}</td><td>${esc(money(o.end_savings))}</td><td class="${d ? (parseFloat(d.amount) < 0 ? "neg" : "pos") : ""}">${d ? esc(money(d)) : ""}</td><td>${esc(money(o.total_interest_base))}</td><td>${esc(goals)}</td><td>${o.id === "baseline" ? "" : scenarioButtons(o)}</td></tr>`;
    }
    table.innerHTML = h + "</tbody>";
  } catch { table.innerHTML = ""; }
}
const scenarioButtons = (o) => `<button type="button" class="linkish" data-action="adopt-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}">${esc(t("scenarios.adopt"))}</button> <button type="button" class="linkish" data-action="del-scenario" data-sid="${esc(o.id)}" data-name="${esc(o.name)}" aria-label="${esc(t("scenarios.delete", { name: o.name }))}">✕</button>`;

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
    const id = prompt(t("import.id"), slug(budget.name || file.name.replace(/\.json$/, "")));
    if (!id) return;
    await api("POST", "/api/budgets", { id, budget });
    await loadList();
    await open(id, "overview");
  } catch (e) {
    const error = e.data?.issues ? e.data.issues.map((i) => `${i.path}: ${i.message}`).join("; ") : e.message;
    status(t("import.failed", { error }), true);
  }
}

async function deleteBudget() {
  $("details.menu")?.removeAttribute("open");
  if (!S.id || !confirm(t("delete.confirm", { name: S.budget.name }))) return;
  try {
    await api("DELETE", `/api/budgets/${encodeURIComponent(S.id)}`);
    S.id = null;
    await loadList();
    if (S.list.length) await open(S.list[0].id, "overview");
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
// Show amounts with their currency's decimals once the field is left.
document.addEventListener("focusout", (e) => {
  const el = e.target;
  const m = el.dataset?.path?.match(/^items\.(\d+)\.amount$/);
  const d = m && parseDecimal(el.value);
  if (d) el.value = toInput(fmtAmount(d, S.budget.items[+m[1]]?.currency));
});
document.addEventListener("click", (e) => {
  const link = e.target.closest("[data-view-link]");
  if (link) { setView(link.dataset.viewLink); $("#shortfall")?.scrollIntoView({ block: "start" }); return; }
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
  renderBudgetItems();
  renderItems();
  renderItemNumbers();
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
    await loadLocale(S.meta.locale, S.meta.crypto);
    S.prefs = await api("GET", "/api/settings").catch(() => S.prefs);
    initAssistant(S.prefs.assistant_status, { budget: () => S.id, changed: poll });
    await loadList();
    if (!S.list.length) { status(t("status.no_budgets_yet")); return; }
    let [want, view] = decodeURIComponent(location.hash.slice(1)).split("/");
    if (!want) try { want = localStorage.getItem("bbbudget.last") || ""; } catch {}
    await open(S.list.some((b) => b.id === want) ? want : S.list[0].id, VIEWS.includes(view) ? view : "overview");
  } catch (e) {
    // A failed fetch is a TypeError with no status; anything else is a bug in this page.
    const offline = e instanceof TypeError && /fetch|network/i.test(e.message);
    status(t(offline ? "status.unreachable" : "status.load_failed", { error: e.message }), true);
    if (!offline) console.error(e);
  }
  setInterval(poll, POLL_MS);
})();
