// Thinking on/off with a thinking model. Run RabAI with a fresh data folder
// and RABAI_PICK set to a thinking model (e.g. K2-Horizon or Qwen3), then:
//   node e2e/thinking.cjs <url> <outDir>
// Checks that "Think" off never leaks reasoning or raw think tags into the
// answer, and that the request only asks for effort levels the model knows.
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir = "e2e/out"] = process.argv.slice(2);
fs.mkdirSync(outDir, { recursive: true });
const results = [];
const check = (name, ok, detail = "") => {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + String(detail).slice(0, 200) : ""}`);
};

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const bodies = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/v1/chat/completions") && r.method() === "POST") {
      try {
        const b = JSON.parse(r.postData() || "{}");
        if (b.stream) bodies.push(b);
      } catch {}
    }
  });
  const waitDone = () => page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 300000 });
  const send = async (text) => {
    await page.locator("#composer-input").fill(text);
    await page.locator("#composer-input").press("Enter");
    await page.waitForTimeout(250);
  };
  const thinkPill = page.locator(".tool-pill", { hasText: "Think" });
  const thinkOn = async () => (await thinkPill.getAttribute("aria-pressed")) === "true";
  // With effort levels, clicking the pill while on opens a menu instead.
  const setThink = async (on) => {
    if ((await thinkOn()) === on) return;
    await thinkPill.click();
    const off = page.locator(".menu .menu-item", { hasText: "Turn off thinking" });
    if (await off.count()) await off.click();
    await page.waitForTimeout(200);
  };
  const answerText = () => page.locator(".turn.assistant.latest .markdown").last().innerText();

  await page.goto(url);
  await page.waitForSelector(".center-card", { timeout: 30000 });
  await page.getByRole("button", { name: "Add model files" }).click();
  await page.waitForSelector(".hero-title", { timeout: 300000 });
  const caps = await page.evaluate(() => fetch("/props").then((r) => r.json()).then((p) => p.chat_template_caps || {}));
  const levels = caps.reasoning_effort_levels || [];
  console.log("effort levels:", JSON.stringify(levels));

  // Think off.
  await setThink(false);
  check("Think can be switched off", !(await thinkOn()));
  await send("What is the capital of France? Answer in one sentence.");
  await waitDone();
  let b = bodies[bodies.length - 1] || {};
  const effort = b.reasoning_effort;
  check("Think off only asks for an effort level the model knows", !levels.length || levels.includes(effort), `${effort} of ${levels}`);
  if (levels.length && !levels.includes("none")) {
    check("Think off doesn't ask a model that can't stop thinking to stop", b.chat_template_kwargs?.enable_thinking !== false, JSON.stringify(b.chat_template_kwargs));
  }
  let answer = await answerText();
  check("Think off answer has no think tags", !/think>/i.test(answer), answer);
  check("Think off answer is on topic", /paris/i.test(answer), answer);
  await page.screenshot({ path: path.join(outDir, "t01-think-off.png") });

  // Think on.
  await page.getByRole("button", { name: /New chat/ }).first().click();
  await page.waitForSelector(".hero-title");
  await setThink(true);
  check("Think can be switched on", await thinkOn());
  await send("Name three primary colors.");
  await waitDone();
  b = bodies[bodies.length - 1] || {};
  check("Think on asks the model to think", b.chat_template_kwargs?.enable_thinking === true);
  answer = await answerText();
  check("Think on answer has no think tags", !/think>/i.test(answer), answer);
  check("Think on answer is on topic", /red|blue|yellow/i.test(answer), answer);
  await page.screenshot({ path: path.join(outDir, "t02-think-on.png") });

  check("no page errors", errors.length === 0, errors.join(" | "));
  await browser.close();
  const failed = results.filter((r) => !r.ok).length;
  console.log(`\n${results.length - failed}/${results.length} checks passed`);
  process.exit(failed ? 1 : 0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
