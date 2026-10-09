// App-wide settings: rate fetching, the default rate source, automatic
// refresh and the CoinGecko key. The key is write-only: the server only says
// whether one is set.

const $ = (sel) => document.querySelector(sel);
const form = $("#form");

async function api(method, body) {
  const res = await fetch("/api/settings", {
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

const LABELS = { data: "Budgets", templates: "Your templates", settings: "This settings file" };

function render(s) {
  form.fetch_rates.checked = s.fetch_rates;
  form.default_rate_source.value = s.default_rate_source;
  form.auto_refresh.value = s.auto_refresh;
  form.coingecko_api_key.value = "";
  $("#keyState").textContent = s.coingecko_api_key_set ? "(a key is set)" : "(none)";
  $("#clearKey").hidden = !s.coingecko_api_key_set;
  syncDisabled();

  const st = s.auto_refresh_status;
  let text = "Automatic updates run while “bbbudget serve” is running.";
  if (st?.last_run) {
    text += ` Last run ${new Date(st.last_run).toLocaleString()}: ${st.updated} rate${st.updated === 1 ? "" : "s"} in ${st.budgets} budget${st.budgets === 1 ? "" : "s"}`;
    text += st.failures?.length ? `; problems: ${st.failures.join("; ")}` : ".";
  }
  $("#autoStatus").textContent = text;

  const dl = $("#paths");
  dl.replaceChildren();
  for (const [k, v] of Object.entries(s.paths || {})) {
    const dt = document.createElement("dt");
    dt.textContent = LABELS[k] || k;
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

async function save(patch, done) {
  try {
    render(await api("PUT", patch));
    status(done);
  } catch (e) {
    status(`Not saved: ${e.message}`, true);
  }
}

form.fetch_rates.addEventListener("change", syncDisabled);
form.addEventListener("submit", (e) => {
  e.preventDefault();
  const patch = {
    fetch_rates: form.fetch_rates.checked,
    default_rate_source: form.default_rate_source.value,
    auto_refresh: form.auto_refresh.value,
  };
  const key = form.coingecko_api_key.value.trim();
  if (key) patch.coingecko_api_key = key; // empty means "keep the current key"
  save(patch, "Saved.");
});
$("#clearKey").addEventListener("click", () => save({ coingecko_api_key: "" }, "Key removed."));

(async function init() {
  try {
    render(await api("GET"));
    form.hidden = false;
    status("Changes apply to every budget.");
  } catch (e) {
    status(`Could not load settings: ${e.message}`, true);
  }
})();
