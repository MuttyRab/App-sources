// End-to-end check of RabAI's UI against a running instance.
// Usage: node e2e/smoke.cjs <app-url-with-token> <out-dir>
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir = "e2e/out"] = process.argv.slice(2);
if (!url) {
  console.error("usage: node e2e/smoke.cjs <url> [outDir]");
  process.exit(2);
}
fs.mkdirSync(outDir, { recursive: true });

const results = [];
function check(name, ok, detail = "") {
  results.push({ name, ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + detail : ""}`);
}

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 1, colorScheme: process.env.DARK ? "dark" : "light" });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push("pageerror: " + e.message));
  page.on("console", (m) => m.type() === "error" && errors.push("console: " + m.text()));
  const shot = (name) => page.screenshot({ path: path.join(outDir, name + ".png") });

  await page.goto(url);
  await page.waitForSelector(".center-card, .hero-title, .load-card", { timeout: 15000 });
  await page.waitForTimeout(800);
  await shot("01-welcome");
  check("welcome screen renders", await page.locator(".center-card, .load-card").count() > 0);

  // Load a model through the (test-stubbed) file picker.
  const browse = page.getByRole("button", { name: "Add model files" });
  if (await browse.count()) await browse.first().click();
  await page.waitForSelector(".load-card", { timeout: 10000 }).catch(() => {});
  await shot("02-loading");
  await page.waitForSelector(".hero-title", { timeout: 120000 });
  await page.waitForTimeout(400);
  await shot("03-ready");
  check("model loads and greeting shows", (await page.locator(".hero-title").textContent()).length > 0);
  check("Think pill visible for reasoning model", await page.locator(".tool-pill:not([hidden])").count() === 1);

  // Send a message and wait for the streamed answer.
  const box = page.locator("#composer-input");
  await box.fill("Write a two-line poem about autumn, then a Python hello world in a code block.");
  await box.press("Enter");
  await page.waitForSelector(".turn.assistant", { timeout: 10000 });
  await page.waitForTimeout(1500);
  await shot("04-streaming");
  await page.waitForSelector(".turn.assistant .msg-actions", { timeout: 180000 });
  await page.waitForTimeout(500);
  await shot("05-answer");
  const answer = await page.locator(".turn.assistant .markdown").last().innerText();
  check("assistant answered", answer.trim().length > 10, answer.slice(0, 80).replace(/\n/g, " "));
  check("thinking section shown", await page.locator(".thinking").count() > 0);
  check("code block rendered", await page.locator(".code-block").count() > 0);
  check("chat appears in sidebar", await page.locator(".chat-item").count() > 0);

  // Regenerate -> version arrows.
  await page.locator(".turn.assistant.latest").hover();
  await page.getByRole("button", { name: "Try again" }).last().click();
  await page.waitForTimeout(300);
  const retryItem = page.locator(".menu .menu-item", { hasText: /^\s*Try again\s*$/ });
  if (await retryItem.count()) await retryItem.first().click();
  await page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 180000 });
  const ver = await page.locator(".turn.assistant .versions span").last().textContent().catch(() => "");
  check("regenerate creates version 2/2", ver === "2/2", ver);

  // Turn thinking off and ask again.
  await page.locator(".tool-pill").click();
  await box.fill("What is 2+2? Answer with just the number.");
  await box.press("Enter");
  await page.waitForTimeout(500);
  await page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 120000 });
  const latestThink = await page.locator(".turn.assistant.latest .thinking").count();
  check("no thinking when Think is off", latestThink === 0);
  await shot("06-thinking-off");

  // Search dialog.
  await page.keyboard.press("Control+k");
  await page.waitForSelector(".search-modal");
  await page.keyboard.type("autumn");
  await page.waitForTimeout(500);
  await shot("07-search");
  check("search finds the chat", await page.locator(".search-item .si-title").count() > 1);
  await page.keyboard.press("Escape");

  // Settings.
  await page.keyboard.press("Control+,");
  await page.waitForSelector(".modal.settings");
  await shot("08-settings-general");
  await page.getByRole("tab", { name: "Model & performance" }).click();
  await page.waitForTimeout(300);
  await shot("09-settings-model");
  await page.getByRole("tab", { name: "Personalization" }).click();
  await shot("10-settings-personalization");
  await page.keyboard.press("Escape");

  // Model menu.
  await page.locator(".model-switch").click();
  await page.waitForSelector(".model-menu");
  await shot("11-model-menu");
  await page.keyboard.press("Escape");

  // Dark theme.
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.waitForTimeout(200);
  await shot("12-dark");

  // Collapse sidebar.
  await page.keyboard.press("Control+Shift+s");
  await page.waitForTimeout(400);
  await shot("13-collapsed");
  await page.keyboard.press("Control+Shift+s");

  // Narrow window.
  await page.setViewportSize({ width: 600, height: 800 });
  await page.waitForTimeout(400);
  await shot("14-narrow");

  check("no page errors", errors.length === 0, errors.join(" | "));
  await browser.close();
  const failed = results.filter((r) => !r.ok).length;
  console.log(`\n${results.length - failed}/${results.length} checks passed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
