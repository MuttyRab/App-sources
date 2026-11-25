// Broader feature checks. Expects a fresh data folder, LOCALCHAT_SCAN pointing
// at the test models and LOCALCHAT_SAVE_DIR for exports.
// Usage: node e2e/features.cjs <url> <outDir> <saveDir> <junkGguf> <mmprojPath> <pngPath>
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir, saveDir, junk, mmproj, png] = process.argv.slice(2);
fs.mkdirSync(outDir, { recursive: true });
const results = [];
const check = (name, ok, detail = "") => {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + String(detail).slice(0, 140) : ""}`);
};

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && !/status of 4(00|22)/.test(m.text()) && errors.push(m.text()));
  const shot = (n) => page.screenshot({ path: path.join(outDir, n + ".png") });
  const waitDone = () => page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 180000 });
  const send = async (text) => {
    await page.locator("#composer-input").fill(text);
    await page.locator("#composer-input").press("Enter");
    await page.waitForTimeout(300);
  };
  const loadFromMenu = async (name) => {
    await page.locator(".model-switch").click();
    await page.waitForSelector(".model-menu");
    await page.locator(".model-menu .mm-row", { hasText: name }).first().click();
    await page.waitForSelector(".hero-title", { timeout: 120000 });
  };

  await page.goto(url);
  // First run: the app scans for models by itself.
  await page.waitForSelector(".found-list .mm-row", { timeout: 30000 });
  check("first run finds models automatically", (await page.locator(".found-list .mm-row").count()) >= 3);

  // Rejected files.
  const bad = await page.evaluate(async (p) => (await fetch("/api/models/inspect", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: p }) })).json(), junk);
  check("non-GGUF file is rejected with a clear message", /isn't a GGUF/.test(bad?.error?.message || ""), bad?.error?.message);
  const proj = await page.evaluate(async (p) => (await fetch("/api/models/inspect", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path: p }) })).json(), mmproj);
  check("mmproj file is explained", /image add-on/.test(proj?.error?.message || ""), proj?.error?.message);

  // Vision model + image attachment.
  await page.locator(".found-list .mm-row", { hasText: "SmolVLM" }).click();
  await page.waitForSelector(".hero-title", { timeout: 120000 });
  check("vision model hides the Think pill", (await page.locator(".tool-pill:not([hidden])").count()) === 0);
  await page.locator('.composer input[type="file"]').setInputFiles(png);
  await page.waitForSelector(".chip.image img");
  check("image attachment chip shown", true);
  await send("What is in this picture? Answer in one sentence.");
  await waitDone();
  await shot("f01-vision");
  const visionAnswer = await page.locator(".turn.assistant.latest .markdown").innerText();
  check("vision model answered about the image", visionAnswer.trim().length > 3 && !(await page.locator(".turn.assistant.latest .notice.error").count()), visionAnswer);
  check("image thumbnail shown in the user message", (await page.locator(".turn.user .att-thumb").count()) === 1);

  // Edit the user message -> versions on the user turn.
  await page.locator(".turn.user").last().hover();
  await page.locator('.turn.user button[aria-label="Edit message"]').last().click();
  await page.locator(".edit-box textarea").fill("Describe the colors in this picture briefly.");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await waitDone();
  const userVer = await page.locator(".turn.user .versions span").last().textContent().catch(() => "");
  check("editing a message keeps both versions", userVer === "2/2", userVer);

  // Export.
  await page.getByRole("button", { name: "Export chat" }).click();
  await page.waitForTimeout(800);
  const exported = fs.readdirSync(saveDir).filter((f) => f.endsWith(".md"));
  check("chat exported as Markdown", exported.length === 1, exported.join(","));
  if (exported[0]) check("export contains the conversation", /## You/.test(fs.readFileSync(path.join(saveDir, exported[0]), "utf8")));

  // Branch.
  await page.locator(".turn.assistant.latest").hover();
  await page.locator('.turn.assistant.latest button[aria-label="More actions"]').click();
  await page.getByRole("menuitem", { name: "Branch in new chat" }).click();
  await page.waitForTimeout(600);
  check("branch opens a new chat", (await page.locator(".chat-item .chat-title", { hasText: "(branch)" }).count()) === 1);

  // Rename, pin, archive, delete via the sidebar menu.
  const firstItem = page.locator(".chat-item").first();
  await firstItem.hover();
  await firstItem.locator(".chat-more").click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.locator(".chat-rename").fill("Renamed chat");
  await page.locator(".chat-rename").press("Enter");
  await page.waitForTimeout(400);
  check("rename from the sidebar", (await page.locator(".chat-title", { hasText: "Renamed chat" }).count()) === 1);
  const renamed = page.locator(".chat-item", { hasText: "Renamed chat" });
  await renamed.hover();
  await renamed.locator(".chat-more").click();
  await page.getByRole("menuitem", { name: "Pin" }).click();
  await page.waitForTimeout(300);
  check("pinned chats get their own group", (await page.locator(".history-label", { hasText: "Pinned" }).count()) === 1);
  await renamed.hover();
  await renamed.locator(".chat-more").click();
  await page.getByRole("menuitem", { name: "Archive" }).click();
  await page.waitForTimeout(400);
  check("archived chat leaves the sidebar", (await page.locator(".chat-title", { hasText: "Renamed chat" }).count()) === 0);
  const before = await page.locator(".chat-item").count();
  const item = page.locator(".chat-item").first();
  await item.hover();
  await item.locator(".chat-more").click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await page.waitForTimeout(400);
  check("delete removes the chat", (await page.locator(".chat-item").count()) === before - 1);

  // Temporary chat.
  await page.keyboard.press("Control+Shift+o");
  await page.locator('button[aria-label="Turn on temporary chat"]').click();
  const countBefore = await page.locator(".chat-item").count();
  await send("Say hello.");
  await waitDone();
  await page.waitForTimeout(500);
  check("temporary chat is not saved", (await page.locator(".chat-item").count()) === countBefore);

  // Context overflow: a small context and long messages.
  await page.keyboard.press("Control+Shift+o");
  await loadFromMenu("Gemma");
  await page.keyboard.press("Control+,");
  await page.getByRole("tab", { name: "Model & performance" }).click();
  await page.locator('select[aria-label="Context size"]').selectOption("2048");
  await page.getByRole("button", { name: "Reload model" }).click();
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
  await page.waitForSelector(".hero-title", { timeout: 120000 });
  const nCtx = await page.evaluate(async () => (await (await fetch("/props")).json()).default_generation_settings.n_ctx);
  check("context size setting is applied", nCtx === 2048, nCtx);
  const long = "Remember this list of words: " + Array.from({ length: 260 }, (_, i) => `word${i}`).join(" ") + ". Reply with OK.";
  for (let i = 0; i < 4; i++) {
    await send(long);
    await waitDone();
  }
  const err = await page.locator(".turn.assistant .notice.error").count();
  check("long conversations keep working past the context size", err === 0);
  check("a divider marks messages outside the model's memory", (await page.locator(".trim-divider").count()) === 1);
  await shot("f02-trimmed");

  // Stop generation.
  await send("Write a very long fairy tale about a dragon who learns to bake bread. Use at least 1500 words.");
  await page.waitForFunction(() => (document.querySelector(".turn.assistant.latest .markdown")?.textContent || "").length > 20, null, { timeout: 120000 });
  await page.keyboard.press("Escape");
  await waitDone();
  await page.waitForTimeout(800);
  const stopped = await page.locator(".turn.assistant.latest .markdown").textContent();
  const stillStreaming = await page.evaluate(() => !!document.querySelector(".send-btn .stop-square"));
  check("Esc stops the reply and keeps the partial text", stopped.length > 20 && stopped.length < 3000 && !stillStreaming, stopped.length + " chars");

  // Settings persist across a reload.
  await page.keyboard.press("Control+,");
  await page.locator('select[aria-label="Theme"]').selectOption("dark");
  await page.waitForTimeout(700);
  await page.reload();
  await page.waitForSelector(".hero-title, .thread .turn", { timeout: 20000 });
  check("theme setting persists", (await page.evaluate(() => document.documentElement.dataset.theme)) === "dark");
  await shot("f03-dark-after-reload");

  check("no unexpected page errors", errors.length === 0, errors.join(" | "));
  await browser.close();
  const failed = results.filter((r) => !r.ok).length;
  console.log(`\n${results.length - failed}/${results.length} checks passed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
