// The in-app assistant: a chat about the open budget. The server runs the
// model and the tools; this file sends the person's messages and shows the
// events that come back.

import { t } from "./i18n.js";

const $ = (sel) => document.querySelector(sel);
const panel = $("#chat");
const messageList = $("#chatLog");
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
  $("#chatWhere").textContent = modelLocationText(status);
}

function modelLocationText(status) {
  if (!status?.ready) return "";
  if (status.on_this_machine) return t("chat.where_local", { model: status.model });
  return t("chat.where_remote", { model: status.model });
}

function resetConversation() {
  conversation = "";
  conversationBudget = currentBudget();
  messageList.replaceChildren();
}

function openPanel() {
  if (conversationBudget !== currentBudget()) resetConversation();
  panel.show();
  form.message.focus();
}

function appendMessage(kind, text) {
  const line = document.createElement("div");
  line.className = `msg msg-${kind}`;
  line.textContent = text;
  messageList.append(line);
  line.scrollIntoView({ block: "end" });
}

function toolCallText(event) {
  const call = t("chat.tool", { tool: event.tool });
  const outcome = event.error || event.change;
  if (!outcome) return call;
  return `${call}: ${outcome}`;
}

const EVENT_HANDLERS = {
  text: (event) => appendMessage("assistant", event.text),
  tool: (event) => appendMessage("tool", toolCallText(event)),
  changed: () => onChanged(),
  error: (event) => appendMessage("error", event.error),
  done: (event) => { conversation = event.conversation || ""; },
};

function handleEvent(event) {
  EVENT_HANDLERS[event.type]?.(event);
}

// readEvents calls handleEvent for each server-sent event in the response body.
async function readEvents(body) {
  const reader = body.pipeThrough(new TextDecoderStream()).getReader();
  let buffered = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buffered += value;
    const messages = buffered.split("\n\n");
    buffered = messages.pop();
    for (const message of messages) handleEvent(JSON.parse(message.replace(/^data: /, "")));
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

async function submitMessage(event) {
  event.preventDefault();
  const message = form.message.value.trim();
  if (!message || form.send.disabled) return;
  if (conversationBudget !== currentBudget()) resetConversation();
  form.message.value = "";
  appendMessage("user", message);
  setBusy(true);
  try {
    await postMessage(message);
  } catch (err) {
    appendMessage("error", t("chat.failed", { error: err.message }));
  } finally {
    setBusy(false);
  }
}

function isPlainEnter(event) {
  return event.key === "Enter" && !event.shiftKey && !event.isComposing;
}

// Enter sends; Shift+Enter starts a new line.
function sendOnEnter(event) {
  if (isPlainEnter(event)) {
    event.preventDefault();
    form.requestSubmit();
  }
}

$("#chatBtn").addEventListener("click", openPanel);
$("#chatClose").addEventListener("click", () => panel.close());
$("#chatNew").addEventListener("click", resetConversation);
form.addEventListener("submit", submitMessage);
form.message.addEventListener("keydown", sendOnEnter);
