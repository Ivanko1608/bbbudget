// The assistant chat, end to end: the real server and page, with a fake
// OpenAI-compatible model server standing in for the model.

import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { findChrome, startServer, startBrowser, waitFor } from "./harness.mjs";

const chrome = findChrome();
const skip = chrome ? false : "no Chromium found (set CHROME)";
let srv, page, model;

const SET_RENT_TOOL_CALL = {
  role: "assistant",
  content: "",
  tool_calls: [{
    id: "call_1",
    type: "function",
    function: { name: "update_item", arguments: JSON.stringify({ item: "Rent", amount: "1000" }) },
  }],
};
const CONFIRMATION = { role: "assistant", content: "Rent is now 1000 a month." };

// fakeModelReply answers the person with a tool call, and the tool's result with text.
function fakeModelReply(request) {
  const last = request.messages.at(-1);
  if (last.role === "tool") return CONFIRMATION;
  return SET_RENT_TOOL_CALL;
}

function startFakeModel() {
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    const message = fakeModelReply(JSON.parse(body));
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ choices: [{ message }] }));
  });
  return new Promise((ok) => server.listen(0, "127.0.0.1", () => ok(server)));
}

before(async () => {
  if (skip) return;
  model = await startFakeModel();
  srv = await startServer();
  page = await startBrowser(chrome);
  await srv.api("PUT", "/api/settings", {
    assistant_provider: "openai-compatible",
    assistant_url: `http://127.0.0.1:${model.address().port}/v1`,
    assistant_model: "fake",
  });
});
after(async () => {
  await page?.close();
  await srv?.stop();
  model?.close();
});

async function sendChatMessage(message) {
  await page.click("#chatBtn");
  await page.fill("#chatForm textarea", message);
  await page.eval(`document.querySelector("#chatForm").requestSubmit()`);
}

test("the assistant changes the budget and the page shows it", { skip }, async () => {
  const id = "e2e-chat";
  await srv.api("POST", "/api/budgets", { id, template: "example" });
  await page.goto(`${srv.base}/#${id}/items`);
  const summaryBefore = await page.text("#railSummary");

  await sendChatMessage("Set rent to 1000");

  await waitFor(() => page.exists(".msg-assistant"), "the answer");
  assert.equal(await page.text(".msg-assistant"), CONFIRMATION.content);
  assert.match(await page.text(".msg-tool"), /update_item/);
  const rent = (await srv.budget(id)).items.find((item) => item.name === "Rent");
  assert.equal(rent.amount, "1000");
  await waitFor(async () => (await page.text("#railSummary")) !== summaryBefore, "the summary to change");
  assert.deepEqual(page.errors, [], "the page logged errors");
});

test("the assistant panel shows the not-advice notice", { skip }, async () => {
  await page.goto(`${srv.base}/`);

  await page.click("#chatBtn");

  assert.match(await page.text("#chatNotice"), /isn't a licensed adviser/);
  assert.ok(await page.eval(`document.querySelector("#chatNotice").offsetHeight > 0`));
});

test("the settings page says where the model runs", { skip }, async () => {
  await page.goto(`${srv.base}/settings.html`, "#form:not([hidden])");

  const status = await page.text("#aiStatus");

  assert.match(status, /fake runs on this computer/);
});
