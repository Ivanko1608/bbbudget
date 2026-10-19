// The in-app assistant: a chat about the open budget. The server runs the
// model and the tools; this file sends the person's messages and shows the
// events that come back.

import { t } from "./i18n.js";

const $ = (sel) => document.querySelector(sel);
const panel = $("#chat");
const log = $("#chatLog");
const form = $("#chatForm");

let conversation = "";
let conversationBudget = null;
let currentBudget = () => null;
let onChanged = () => {};

// initAssistant shows the Ask button when the assistant is ready. budget
// returns the open budget's id; changed runs after the assistant edits it.
export function initAssistant(status, { budget, changed }) {
  currentBudget = budget;
  onChanged = changed;
  $("#chatBtn").hidden = !status?.ready;
  $("#chatWhere").textContent = whereItRuns(status);
}

function whereItRuns(status) {
  if (!status?.ready) return "";
  if (status.on_this_machine) return t("chat.where_local", { model: status.model });
  return t("chat.where_remote", { model: status.model });
}

function startOver() {
  conversation = "";
  conversationBudget = currentBudget();
  log.replaceChildren();
}

function openPanel() {
  if (conversationBudget !== currentBudget()) startOver();
  panel.show();
  form.message.focus();
}

function addLine(kind, text) {
  const line = document.createElement("div");
  line.className = `msg msg-${kind}`;
  line.textContent = text;
  log.append(line);
  line.scrollIntoView({ block: "end" });
}

function toolLine(event) {
  const call = t("chat.tool", { tool: event.tool });
  const outcome = event.error || event.change;
  if (!outcome) return call;
  return `${call}: ${outcome}`;
}

const HANDLERS = {
  text: (event) => addLine("assistant", event.text),
  tool: (event) => addLine("tool", toolLine(event)),
  changed: () => onChanged(),
  error: (event) => addLine("error", event.error),
  done: (event) => { conversation = event.conversation || ""; },
};

function handle(event) {
  HANDLERS[event.type]?.(event);
}

// readEvents calls handle for each server-sent event in the response body.
async function readEvents(body) {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader();
  let buffered = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buffered += value;
    const messages = buffered.split("\n\n");
    buffered = messages.pop();
    for (const message of messages) handle(JSON.parse(message.replace(/^data: /, "")));
  }
}

async function postMessage(message) {
  const path = `/api/budgets/${encodeURIComponent(currentBudget())}/chat`;
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Requested-With": "bbbudget" },
    body: JSON.stringify({ conversation, message }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => null);
    throw new Error(data?.error || res.statusText);
  }
  await readEvents(res.body);
}

function setBusy(busy) {
  form.send.disabled = busy;
  $("#chatState").textContent = busy ? t("chat.working") : "";
}

async function send(e) {
  e.preventDefault();
  const message = form.message.value.trim();
  if (!message || form.send.disabled) return;
  if (conversationBudget !== currentBudget()) startOver();
  form.message.value = "";
  addLine("user", message);
  setBusy(true);
  try {
    await postMessage(message);
  } catch (err) {
    addLine("error", t("chat.failed", { error: err.message }));
  } finally {
    setBusy(false);
  }
}

// Enter sends; Shift+Enter starts a new line.
function sendOnEnter(e) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    form.requestSubmit();
  }
}

$("#chatBtn").addEventListener("click", openPanel);
$("#chatClose").addEventListener("click", () => panel.close());
$("#chatNew").addEventListener("click", startOver);
form.addEventListener("submit", send);
form.message.addEventListener("keydown", sendOnEnter);
