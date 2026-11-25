// Pictures and attachments. Run RabAI with a fresh data folder, an image
// engine (--sd-engine), RABAI_SAVE_DIR, and RABAI_PICK listing a chat model
// and an image model (path-list separated). Then:
//   node e2e/images.cjs <url> <outDir> <saveDir> <fixturesDir>
// fixturesDir holds report.pdf, notes.docx, sales.xlsx, deck.pptx, tone.wav
// and clip.webm (see the test notes in README).
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir = "e2e/out", saveDir, fixtures] = process.argv.slice(2);
fs.mkdirSync(outDir, { recursive: true });
const results = [];
const check = (name, ok, detail = "") => {
  results.push({ name, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? "  — " + String(detail).slice(0, 200) : ""}`);
};

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => m.type() === "error" && !/status of 4\d\d/.test(m.text()) && errors.push(m.text()));
  const shot = (n) => page.screenshot({ path: path.join(outDir, n + ".png") });
  const send = async (text) => {
    await page.locator("#composer-input").fill(text);
    await page.locator("#composer-input").press("Enter");
  };
  const waitPicture = async () => {
    await page.waitForSelector(".turn.assistant.latest .gen-image", { timeout: 600000 });
    await page.waitForFunction(() => {
      const i = document.querySelector(".turn.assistant.latest .gen-image");
      return i && i.complete && i.naturalWidth > 0;
    });
  };

  await page.goto(url);
  await page.waitForSelector(".center-card", { timeout: 30000 });
  await page.getByRole("button", { name: "Add model files" }).click();
  await page.waitForSelector(".toast", { timeout: 30000 });
  await page.waitForTimeout(800);
  const toasts = (await page.locator(".toast").allTextContents()).join(" | ");
  check("image model is recognised when added", /image model/i.test(toasts), toasts);
  await page.waitForSelector(".hero-title", { timeout: 300000 });

  // Model menu lists the image model with a check mark.
  await page.locator(".model-switch").click();
  await page.waitForSelector(".model-menu");
  await page.waitForTimeout(300);
  await shot("i01-model-menu");
  const imgRows = await page.locator(".model-menu .mm-row", { has: page.locator(".mm-icon") }).count();
  check("model menu shows an Image model section", imgRows === 1, imgRows);
  check("the image model is the default", (await page.locator(".model-menu .mm-row.current", { has: page.locator(".mm-icon") }).count()) === 1);
  await page.keyboard.press("Escape");

  // 1. Asking in plain words makes a picture.
  if (await page.locator('.tool-pill[aria-pressed="true"]').count()) {
    await page.locator('.tool-pill[aria-pressed="true"]').click();
    const off = page.locator(".menu .menu-item", { hasText: "Turn off thinking" });
    if (await off.count()) await off.click();
  }
  await send("Draw a red apple on a wooden table");
  await page.waitForSelector(".turn.assistant.latest .gen-card", { timeout: 30000 });
  await page.waitForTimeout(1500);
  await shot("i02-creating");
  check("a picture card shows progress while it's made", (await page.locator(".gen-card .gen-stage").count()) === 1);
  await waitPicture();
  await page.waitForTimeout(400);
  await shot("i03-picture");
  const nat = await page.locator(".turn.assistant.latest .gen-image").evaluate((i) => [i.naturalWidth, i.naturalHeight]);
  check("the picture appears in the chat", nat[0] >= 256 && nat[1] >= 256, nat.join("x"));
  check("the picture has a caption", /Made in/.test(await page.locator(".turn.assistant.latest .gen-caption").textContent()));

  // 2. Download.
  await page.locator(".turn.assistant.latest .gen-frame").hover();
  await page.getByRole("button", { name: "Download picture" }).last().click();
  await page.waitForTimeout(1200);
  const saved = fs.readdirSync(saveDir).filter((f) => f.endsWith(".png"));
  check("download saves a PNG", saved.length >= 1, saved.join(", "));

  // 3. Make another: a second version.
  await page.locator(".turn.assistant.latest").hover();
  await page.getByRole("button", { name: "Make another" }).last().click();
  await waitPicture();
  const ver = await page.locator(".turn.assistant.latest .versions span").textContent().catch(() => "");
  check("make another adds a version", ver === "2/2", ver);

  // 4. Image mode from the + menu with a shape.
  await page.getByRole("button", { name: "Add photos and files" }).click();
  await page.locator(".menu .menu-item", { hasText: "Create image" }).click();
  await page.waitForSelector(".image-pill:not([hidden])");
  check("image mode shows the Image pill", (await page.locator(".image-pill .pill-main").textContent()).includes("Image"));
  await page.locator(".image-pill .pill-main").click();
  await page.locator(".menu .menu-item", { hasText: "Landscape" }).click();
  await shot("i04-image-mode");
  await send("a lighthouse on a cliff at sunset");
  await waitPicture();
  const land = await page.locator(".turn.assistant.latest .gen-image").evaluate((i) => i.naturalWidth > i.naturalHeight);
  check("landscape shape is used", land);

  // 5. Edit the picture (image-to-image).
  await page.locator(".turn.assistant.latest").hover();
  await page.getByRole("button", { name: "Edit picture" }).last().click();
  await page.waitForSelector(".chips .chip.image");
  check("edit puts the picture in the message box", (await page.locator(".chips .chip.image").count()) === 1);
  await send("make the sky purple");
  await waitPicture();
  await shot("i05-edited");
  check("an edited picture is made", true);
  // Turn image mode off.
  await page.locator(".image-pill .pill-x").click();
  check("image mode turns off", await page.locator(".image-pill").isHidden());

  // 6. Attachments: documents, audio, video.
  await page.getByRole("button", { name: /New chat/ }).first().click();
  await page.waitForSelector(".hero-title");
  const files = ["report.pdf", "notes.docx", "sales.xlsx", "deck.pptx", "tone.wav", "clip.webm"].map((f) => path.join(fixtures, f));
  await page.locator('.composer input[type="file"]').setInputFiles(files);
  await page.waitForFunction(() => document.querySelectorAll(".chips .chip").length >= 6 && !document.querySelector(".chips .att-ico.spin"), null, { timeout: 60000 });
  await page.waitForTimeout(300);
  await shot("i06-attachments");
  const kinds = await page.locator(".chips .chip .chip-kind").allTextContents();
  check("PDF is read", kinds.some((k) => /^PDF/.test(k)), kinds.join(" | "));
  check("Word, Excel and PowerPoint are read", ["Word document", "Spreadsheet", "Presentation"].every((x) => kinds.some((k) => k.startsWith(x))), kinds.join(" | "));
  check("audio is read", kinds.some((k) => /^Audio/.test(k)));
  check("video is read into frames", (await page.locator(".chips .chip.video img").count()) === 1);
  const bodies = [];
  page.on("request", (r) => {
    if (r.url().endsWith("/v1/chat/completions") && r.method() === "POST") {
      try {
        bodies.push(JSON.parse(r.postData() || "{}"));
      } catch {}
    }
  });
  await send("What is the secret word in the PDF? Reply with one word.");
  await page.waitForSelector(".turn.assistant.latest .msg-actions", { timeout: 300000 });
  const last = bodies.filter((b) => b.stream).pop() || {};
  const userMsg = (last.messages || []).filter((m) => m.role === "user").pop();
  const txt = typeof userMsg?.content === "string" ? userMsg.content : (userMsg?.content || []).map((p) => p.text || "").join(" ");
  check("document text is sent to the model", /PELICAN/.test(txt) && /4200 dollars/.test(txt) && /February\t175/.test(txt) && /hire two designers/.test(txt));
  check("the model is told about audio it can't hear", /can't listen to audio/.test(txt));
  await shot("i07-doc-answer");

  // 7. Models window: image models tab.
  await page.getByRole("button", { name: "Models", exact: true }).click();
  await page.waitForSelector(".models-modal");
  await page.getByRole("tab", { name: "Image models" }).click();
  await page.waitForSelector(".img-row");
  await shot("i08-image-models");
  check("Models window lists the image model as default", /Default/.test(await page.locator(".img-row").first().textContent()));
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
