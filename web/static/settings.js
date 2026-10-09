// App-wide settings: language, rate fetching, the default rate source,
// automatic refresh and the CoinGecko key. The key is write-only: the server
// only says whether one is set.

import { t, loadLocale, dateTime } from "./i18n.js";

const $ = (sel) => document.querySelector(sel);
const form = $("#form");

let META = { locales: [] };

async function api(method, body, path = "/api/settings") {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json", "X-Requested-With": "bbbudget" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error || res.statusText);
  return data;
}

function status(text, isErr = false) {
  $("#status").textContent = text;
  $("#status").classList.toggle("err", isErr);
}


function render(s) {
  form.locale.innerHTML = [["auto", t("prefs.language.auto")], ...META.locales.map((l) => [l.code, l.name])]
    .map(([v, n]) => `<option value="${v}">${n.replace(/[&<>"]/g, "")}</option>`).join("");
  form.locale.value = s.locale || "auto";
  const ai = s.assistant || { provider: "off", status: {} };
  form.assistant_provider.value = ai.provider || "off";
  form.assistant_model.value = ai.model || "";
  form.assistant_url.value = ai.url || "";
  form.assistant_api_key.value = "";
  $("#aiKeyState").textContent = t(ai.api_key_set ? "prefs.cg.set" : "prefs.cg.none");
  $("#clearAiKey").hidden = !ai.api_key_set;
  $("#aiStatus").textContent = ai.status?.enabled ? t("prefs.assistant.status_on", { model: ai.status.model }) : t("prefs.assistant.status_off");
  syncAssistant();
  form.fetch_rates.checked = s.fetch_rates;
  form.default_rate_source.value = s.default_rate_source;
  form.auto_refresh.value = s.auto_refresh;
  form.coingecko_api_key.value = "";
  $("#keyState").textContent = t(s.coingecko_api_key_set ? "prefs.cg.set" : "prefs.cg.none");
  $("#clearKey").hidden = !s.coingecko_api_key_set;
  syncDisabled();

  const st = s.auto_refresh_status;
  let text = t("prefs.auto_status");
  if (st?.last_run) {
    text += " " + t("prefs.last_run", { when: dateTime(st.last_run), rates: t("prefs.rates_count", { count: st.updated }), budgets: t("prefs.budgets_count", { count: st.budgets }) });
    text += st.failures?.length ? t("prefs.problems", { list: st.failures.join("; ") }) : ".";
  }
  $("#autoStatus").textContent = text;

  const dl = $("#paths");
  dl.replaceChildren();
  for (const [k, v] of Object.entries(s.paths || {})) {
    const dt = document.createElement("dt");
    const label = t(`prefs.files.${k}`);
    dt.textContent = label === `prefs.files.${k}` ? k : label;
    const dd = document.createElement("dd");
    dd.textContent = v || "—";
    dl.append(dt, dd);
  }
  $("#h-files").parentElement.hidden = !dl.children.length;
}

// The server URL is only for an OpenAI-compatible server.
function syncAssistant() {
  const p = form.assistant_provider.value;
  $("#aiUrlRow").hidden = p !== "openai-compatible";
  for (const name of ["assistant_model", "assistant_api_key"]) form[name].disabled = p === "off";
}

// Rate options mean nothing while fetching is off.
function syncDisabled() {
  const off = !form.fetch_rates.checked;
  for (const name of ["default_rate_source", "auto_refresh", "coingecko_api_key"]) form[name].disabled = off;
}

// save reports whether the settings were saved.
async function save(patch, done) {
  try {
    render(await api("PUT", patch));
    status(done);
    return true;
  } catch (e) {
    status(t("prefs.not_saved", { error: e.message }), true);
    return false;
  }
}

form.fetch_rates.addEventListener("change", syncDisabled);
form.assistant_provider.addEventListener("change", syncAssistant);
$("#clearAiKey").addEventListener("click", () => save({ assistant_api_key: "" }, t("prefs.key_removed")));
form.addEventListener("submit", (e) => {
  e.preventDefault();
  const patch = {
    locale: form.locale.value,
    assistant_provider: form.assistant_provider.value,
    assistant_model: form.assistant_model.value.trim(),
    assistant_url: form.assistant_url.value.trim(),
    fetch_rates: form.fetch_rates.checked,
    default_rate_source: form.default_rate_source.value,
    auto_refresh: form.auto_refresh.value,
  };
  const key = form.coingecko_api_key.value.trim();
  if (key) patch.coingecko_api_key = key; // empty means "keep the current key"
  const aiKey = form.assistant_api_key.value.trim();
  if (aiKey) patch.assistant_api_key = aiKey;
  const relang = patch.locale !== CURRENT;
  save(patch, t("prefs.saved")).then((ok) => { if (ok && relang) location.reload(); }); // show the page in the new language
});
$("#clearKey").addEventListener("click", () => save({ coingecko_api_key: "" }, t("prefs.key_removed")));

let CURRENT = "auto";

(async function init() {
  try {
    META = await api("GET", undefined, "/api/meta");
    await loadLocale(META.locale);
    const s = await api("GET");
    CURRENT = s.locale || "auto";
    render(s);
    form.hidden = false;
    status(t("prefs.apply_all"));
  } catch (e) {
    status(t("prefs.load_failed", { error: e.message }), true);
  }
})();
