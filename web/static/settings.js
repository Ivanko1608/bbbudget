// App-wide settings: language, rate fetching, the default rate source,
// automatic refresh, the CoinGecko key and the assistant. Keys are
// write-only: the server only says whether one is set.

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
  form.fetch_rates.checked = s.fetch_rates;
  form.default_rate_source.value = s.default_rate_source;
  form.auto_refresh.value = s.auto_refresh;
  form.coingecko_api_key.value = "";
  $("#keyState").textContent = t(s.coingecko_api_key_set ? "prefs.cg.set" : "prefs.cg.none");
  $("#clearKey").hidden = !s.coingecko_api_key_set;
  syncDisabled();
  renderAssistant(s);

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

// Rate options mean nothing while fetching is off.
function syncDisabled() {
  const off = !form.fetch_rates.checked;
  for (const name of ["default_rate_source", "auto_refresh", "coingecko_api_key"]) form[name].disabled = off;
}

const MODEL_PLACEHOLDERS = { anthropic: "claude-opus-5-5", "openai-compatible": "llama3.1" };

function renderAssistant(s) {
  form.assistant_provider.value = s.assistant_provider;
  form.assistant_model.value = s.assistant_model;
  form.assistant_url.value = s.assistant_url;
  form.assistant_api_key.value = "";
  const keyState = s.assistant_api_key_set ? "prefs.cg.set" : "prefs.cg.none";
  $("#aiKeyState").textContent = t(keyState);
  $("#clearAiKey").hidden = !s.assistant_api_key_set;
  $("#aiStatus").textContent = assistantStatusText(s.assistant_status);
  syncAssistantFields();
}

function assistantStatusText(status) {
  const model = status.model;
  if (status.provider === "off") return t("prefs.ai.off_status");
  if (!status.ready) return t("prefs.ai.problem", { problem: status.problem });
  if (status.on_this_machine) return t("prefs.ai.local", { model });
  if (status.provider === "anthropic") return t("prefs.ai.remote_anthropic", { model });
  return t("prefs.ai.remote_server", { model });
}

// The server address only applies to an OpenAI-compatible server.
function syncAssistantFields() {
  const provider = form.assistant_provider.value;
  for (const name of ["assistant_model", "assistant_url", "assistant_api_key"]) {
    form[name].disabled = provider === "off";
  }
  $("#aiUrlField").hidden = provider !== "openai-compatible";
  form.assistant_model.placeholder = MODEL_PLACEHOLDERS[provider] || "";
}

function assistantPatch() {
  const patch = {
    assistant_provider: form.assistant_provider.value,
    assistant_model: form.assistant_model.value.trim(),
    assistant_url: form.assistant_url.value.trim(),
  };
  const key = form.assistant_api_key.value.trim();
  if (key) patch.assistant_api_key = key;
  return patch;
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
form.assistant_provider.addEventListener("change", syncAssistantFields);
form.addEventListener("submit", (e) => {
  e.preventDefault();
  const patch = {
    locale: form.locale.value,
    fetch_rates: form.fetch_rates.checked,
    default_rate_source: form.default_rate_source.value,
    auto_refresh: form.auto_refresh.value,
    ...assistantPatch(),
  };
  const key = form.coingecko_api_key.value.trim();
  if (key) patch.coingecko_api_key = key; // empty means "keep the current key"
  const relang = patch.locale !== CURRENT;
  save(patch, t("prefs.saved")).then((ok) => { if (ok && relang) location.reload(); }); // show the page in the new language
});
$("#clearKey").addEventListener("click", () => save({ coingecko_api_key: "" }, t("prefs.key_removed")));
$("#clearAiKey").addEventListener("click", () => {
  save({ assistant_api_key: "" }, t("prefs.key_removed"));
});

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
