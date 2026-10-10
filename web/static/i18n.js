// UI translations and locale-aware formatting. Catalogs live in i18n/<locale>.json
// (English is the fallback for any missing key). The server decides the
// locale (/api/meta: the app setting, else the browser's languages), so the
// UI and the server-rendered text always agree.
//
// Money is formatted from the exact decimal strings the server sends:
// Intl.NumberFormat accepts strings without going through a float.

let LOCALE = "en";
let CAT = {};
let FALLBACK = {};
let DECIMAL = ".";
let GROUP = ",";
let CRYPTO = new Set();

async function fetchCatalog(locale) {
  const res = await fetch(`i18n/${encodeURIComponent(locale)}.json`);
  if (!res.ok) throw new Error(`no catalog for ${locale}`);
  return res.json();
}

// loadLocale loads the catalog for locale (and English as the fallback) and
// translates the static page. crypto lists the cryptocurrency codes, which
// are shown as "0.015 BTC" rather than with a currency symbol.
export async function loadLocale(locale, crypto = []) {
  FALLBACK = await fetchCatalog("en");
  LOCALE = locale || "en";
  try {
    CAT = LOCALE === "en" ? FALLBACK : await fetchCatalog(LOCALE);
  } catch {
    LOCALE = "en";
    CAT = FALLBACK;
  }
  CRYPTO = new Set(crypto);
  const parts = new Intl.NumberFormat(LOCALE).formatToParts(12345.6); // separators only
  DECIMAL = parts.find((p) => p.type === "decimal")?.value || ".";
  GROUP = parts.find((p) => p.type === "group")?.value || ",";
  document.documentElement.lang = LOCALE;
  translatePage();
}

export const locale = () => LOCALE;

// t renders a message as plain text. A "count" argument picks the plural
// form ({"one": ..., "few": ..., "other": ...}) with the browser's CLDR rules.
export function t(key, args = {}) {
  return fill(template(key, args), args, String);
}

// tHTML renders a message whose catalog text holds markup (<b>…</b>): the
// text is trusted, as it ships with the app, and every argument is escaped.
export function tHTML(key, args = {}) {
  return fill(template(key, args), args, escapeHTML);
}

function template(key, args) {
  let msg = CAT[key] ?? FALLBACK[key];
  if (msg == null) return key; // visible, and caught by the catalog tests
  if (typeof msg === "object") {
    const rules = new Intl.PluralRules(CAT[key] ? LOCALE : "en");
    msg = msg[rules.select(Number(args.count ?? 0))] ?? msg.other;
  }
  return msg;
}

function fill(msg, args, show) {
  return msg.replace(/\{([a-z_0-9]+)\}/g, (ph, k) => (k in args ? show(args[k]) : ph));
}

const escapeHTML = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

// translatePage fills elements that have a data-i18n attribute (text) and
// data-i18n-attr, a list like "placeholder:some.key;aria-label:other.key" (attributes).
export function translatePage(root = document) {
  root.querySelectorAll("[data-i18n]").forEach((el) => { el.textContent = t(el.dataset.i18n); });
  root.querySelectorAll("[data-i18n-attr]").forEach((el) => {
    for (const pair of el.dataset.i18nAttr.split(";")) {
      const [attr, key] = pair.split(":");
      if (attr && key) el.setAttribute(attr.trim(), t(key.trim()));
    }
  });
}

// money renders {amount, currency} the way the server's Display does: whole
// units for fiat, the exact amount for crypto. Rounding is half-even like Go.
// With exact, fiat keeps its minor units too ("€0.61").
export function money(m, { exact = false } = {}) {
  if (!m) return "—";
  if (CRYPTO.has(m.currency)) {
    const n = new Intl.NumberFormat(LOCALE, { maximumFractionDigits: 20 }).format(m.amount);
    return `${n} ${m.currency}`;
  }
  const digits = exact ? {} : { maximumFractionDigits: 0, minimumFractionDigits: 0 };
  try {
    return new Intl.NumberFormat(LOCALE, { style: "currency", currency: m.currency, roundingMode: "halfEven", ...digits }).format(m.amount);
  } catch {
    return m.display || `${m.amount} ${m.currency}`;
  }
}

// moneyAxis formats a chart axis value (a float: pixels, not money).
// compact shortens it for narrow charts ("€10K", "10 тыс. €").
export function moneyAxis(v, currency, compact = false) {
  return new Intl.NumberFormat(LOCALE, { style: "currency", currency, maximumFractionDigits: 0, notation: compact ? "compact" : "standard" }).format(v);
}

// number groups an exact decimal string ("1000.5" → "1,000.5" / "1 000,5").
export function number(d) {
  return new Intl.NumberFormat(LOCALE, { maximumFractionDigits: 20 }).format(String(d));
}

// toInput shows a stored decimal ("45.50") the way this locale types it ("45,50").
export function toInput(d) {
  return d == null ? "" : String(d).replace(".", DECIMAL);
}

// parseDecimal reads what someone typed into an exact decimal string, or
// null. In a decimal-comma locale "12,50" is twelve and a half, never 1250;
// a dot is accepted too, and spaces group digits. Elsewhere commas group.
export function parseDecimal(input) {
  let s = String(input).trim().replace(/[\s  ']/g, "");
  if (DECIMAL === ",") {
    if (s.includes(",") && s.includes(".")) return null; // ambiguous: 1.234,5 or 1,234.5?
    s = s.replace(",", ".");
  } else {
    s = s.replace(/,/g, "");
  }
  return /^[+-]?\d+(\.\d+)?$/.test(s) ? s : null;
}

// monthName is the full name of month "YYYY-MM" ("October", "октябрь").
export function monthName(ym) {
  const [y, m] = ym.split("-").map(Number);
  return new Intl.DateTimeFormat(LOCALE, { month: "long", timeZone: "UTC" }).format(Date.UTC(y, m - 1, 15));
}

export function dateTime(iso) {
  return new Date(iso).toLocaleString(LOCALE);
}

// date renders a timestamp, or a calendar date ("2026-10-07") as that day
// wherever the reader is.
export function date(iso) {
  const day = /^\d{4}-\d{2}-\d{2}$/.test(iso);
  return new Date(iso).toLocaleDateString(LOCALE, day ? { timeZone: "UTC" } : {});
}
