// Multi-model checks: add several models at once, pick from the dropdown,
// switch models mid-chat and keep the conversation, retry with another model.
// Start RabAI with a fresh data folder and RABAI_PICK set to several model
// paths (path-list separated). Usage: node e2e/models.cjs <url> <outDir>
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir = "e2e/out"] = process.argv.slice(2);
fs.mkdirSync(outDir, { recursive: true });
const results = [];
const check = (name, ok, detail = "") => {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + String(detail).slice(0, 160) : ""}`);
};

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && !/status of 4\d\d/.test(m.text()) && errors.push(m.text()));
  const bodies = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/v1/chat/completions") && r.method() === "POST") {
      try {
        const b = JSON.parse(r.postData() || "{}");
        if (b.stream) bodies.push(b);
      } catch {}
    }
  });
  const shot = (n) => page.screenshot({ path: path.join(outDir, n + ".png") });
  const waitDone = () => page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 180000 });
  const send = async (text) => {
    await page.locator("#composer-input").fill(text);
    await page.locator("#composer-input").press("Enter");
    await page.waitForTimeout(250);
  };
  const header = () => page.locator(".model-switch .ms-name").textContent();

  await page.goto(url);
  await page.waitForSelector(".center-card", { timeout: 30000 });
  // Add several models in one go (the file dialog is stubbed by RABAI_PICK).
  await page.getByRole("button", { name: "Add model files" }).click();
  await page.waitForSelector(".hero-title", { timeout: 120000 });
  const toasts = await page.locator(".toast").allTextContents();
  check("adding several files adds them all", toasts.some((t) => /Added 3 models/.test(t)), toasts.join(" | "));
  check("the first added model loads automatically", /Qwen3/i.test(await header()), await header());

  // ChatGPT-style dropdown.
  await page.locator(".model-switch").click();
  await page.waitForSelector(".model-menu");
  await page.waitForTimeout(250);
  await shot("m01-dropdown");
  const rows = await page.locator(".model-menu .mm-row").count();
  check("dropdown lists every model", rows === 3, rows);
  check("current model has a check mark", (await page.locator(".model-menu .mm-row.current .mm-check").count()) === 1);
  check("rows describe the model", /Thinking/.test(await page.locator(".model-menu .mm-row.current .mm-meta").textContent()));
  await page.keyboard.press("Escape");

  // Chat with model 1.
  if (await page.locator('.tool-pill[aria-pressed="true"]').count()) await page.locator(".tool-pill").click();
  await send("My name is Rab and my favorite color is green. Reply with just: noted.");
  await waitDone();

  // Switch mid-chat and send right away: the message waits for the new model.
  await page.locator(".model-switch").click();
  await page.locator(".model-menu .mm-row", { hasText: "Gemma" }).click();
  await send("What is my name and favorite color?");
  const waited = await page.locator(".turn.assistant.latest .progress-note").textContent().catch(() => "");
  await shot("m02-waiting-for-model");
  check("message waits while the new model loads", /Loading/i.test(waited), waited);
  await waitDone();
  await page.waitForTimeout(400);
  await shot("m03-after-switch");
  const last = bodies[bodies.length - 1];
  const userTurns = (last?.messages || []).filter((m) => m.role === "user").map((m) => (typeof m.content === "string" ? m.content : ""));
  check("new model receives the whole conversation", userTurns.some((t) => /My name is Rab/.test(t)) && userTurns.length === 2, JSON.stringify(userTurns));
  check("header shows the new model", /Gemma/i.test(await header()), await header());
  const tags = await page.locator(".model-tag").allTextContents();
  check("replies are labeled with the model that wrote them", tags.some((t) => /Qwen3/i.test(t)) && tags.some((t) => /Gemma/i.test(t)), tags.join(", "));
  check("no error after switching", (await page.locator(".turn.assistant .notice.error").count()) === 0);

  // Try again with another model.
  await page.locator(".turn.assistant.latest").hover();
  await page.getByRole("button", { name: "Try again" }).last().click();
  await page.waitForSelector(".menu");
  await shot("m04-try-again-menu");
  await page.locator(".menu .menu-item", { hasText: "SmolVLM" }).click();
  await page.waitForTimeout(300);
  await waitDone();
  const ver = await page.locator(".turn.assistant.latest .versions span").textContent().catch(() => "");
  const latestTag = await page.locator(".turn.assistant.latest .model-tag").textContent().catch(() => "");
  check("retry with another model adds a new version", ver === "2/2", ver);
  check("the retry was written by the chosen model", /SmolVLM/i.test(latestTag), latestTag);

  // Models window.
  await page.getByRole("button", { name: "Models", exact: true }).click();
  await page.waitForSelector(".models-modal .mgr-row");
  await shot("m05-manage");
  const before = await page.locator(".models-modal .mgr-row").count();
  await page.locator(".models-modal .mgr-row", { hasText: "Qwen3" }).getByRole("button", { name: /Remove/ }).click();
  await page.waitForTimeout(500);
  const after = await page.locator(".models-modal .mgr-row").count();
  check("models can be removed from the list", before === 3 && after === 2, `${before} -> ${after}`);
  await page.keyboard.press("Escape");

  check("no unexpected page errors", errors.length === 0, errors.join(" | "));
  await browser.close();
  const failed = results.filter((r) => !r.ok).length;
  console.log(`\n${results.length - failed}/${results.length} checks passed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
