// Visual audit: seeds realistic data, captures every screen in light and dark
// at several window sizes, and flags overlapping, clipped or overflowing UI.
// Usage: node e2e/visual.cjs <url> <outDir> <pngPath>
// Start RabAI with a fresh data folder and RABAI_PICK set to 2+ model paths.
const { chromium } = require("playwright");
const fs = require("fs");
const path = require("path");

const [url, outDir, pngPath] = process.argv.slice(2);
fs.mkdirSync(outDir, { recursive: true });
const imgData = "data:image/png;base64," + fs.readFileSync(pngPath).toString("base64");
const issues = [];

const RICH = `Here's a quick overview of **sorting algorithms**, with an example and a comparison.

## How quicksort works

1. Pick a *pivot* element.
2. Partition the array so smaller items go left and larger items go right.
3. Recursively sort both halves.

> Quicksort is usually the fastest in practice, though its worst case is $O(n^2)$.

The average cost is $T(n) = 2T(n/2) + O(n)$, which solves to:

$$
T(n) = O(n \\log n)
$$

\`\`\`python
def quicksort(items):
    """Return a new sorted list."""
    if len(items) <= 1:
        return items
    pivot, *rest = items
    smaller = [x for x in rest if x < pivot]
    larger = [x for x in rest if x >= pivot]
    return quicksort(smaller) + [pivot] + quicksort(larger)

print(quicksort([5, 3, 8, 1, 9, 2]))  # [1, 2, 3, 5, 8, 9]
\`\`\`

### Comparison

| Algorithm | Best | Average | Worst | Stable | Extra memory |
|---|---|---|---|---|---|
| Quicksort | O(n log n) | O(n log n) | O(n²) | No | O(log n) |
| Merge sort | O(n log n) | O(n log n) | O(n log n) | Yes | O(n) |
| Heapsort | O(n log n) | O(n log n) | O(n log n) | No | O(1) |
| Insertion sort | O(n) | O(n²) | O(n²) | Yes | O(1) |

- Use \`sorted()\` in Python for everyday work: it's Timsort, stable and fast.
- A very_long_identifier_name_that_should_wrap_or_scroll_gracefully_without_breaking_the_layout_of_the_message_column is handled too.
- Links like [the Python docs](https://docs.python.org/3/howto/sorting.html) open in your browser.

---

Want me to walk through **merge sort** next?`;

const REASONING = `The user wants an overview of sorting algorithms. I should explain quicksort step by step, show the recurrence, give a short Python example, and compare the common algorithms in a table. I'll keep it concise but complete.`;

const LONG_USER = Array.from({ length: 30 }, (_, i) => `Line ${i + 1}: here is some pasted text from a document that goes on for a while to test how long messages look.`).join("\n");

function msg(id, parent, role, content, extra = {}) {
  return { id, parent, children: [], role, content, created: Date.now(), ...extra };
}

function buildChat(id, title, updated, nodes, opts = {}) {
  const messages = { root: { id: "root", children: [], sel: null } };
  for (const n of nodes) {
    messages[n.id] = n;
    messages[n.parent].children.push(n.id);
    messages[n.parent].sel = n.id;
  }
  if (opts.sel) for (const [p, c] of Object.entries(opts.sel)) messages[p].sel = c;
  return { id, title, created: updated, updated, messages, pinned: !!opts.pinned, modelName: "Qwen3 0.6B" };
}

async function seed(page) {
  const now = Date.now();
  const day = 86400000;
  const showcase = buildChat("showcase", "Sorting algorithms explained", now, [
    msg("u1", "root", "user", "Explain quicksort and compare it with other sorting algorithms. Include a code example.", { attachments: [{ type: "image", name: "diagram.png", dataUrl: imgData }, { type: "text", name: "notes_from_class_about_algorithms.md", text: "# notes", size: 2048, code: false }] }),
    msg("a1", "u1", "assistant", "Quicksort is a divide-and-conquer algorithm.", { model: "Gemma 3 270m It", status: "done", stats: { tps: 21.4, n: 120 } }),
    msg("a2", "u1", "assistant", RICH, { model: "Qwen3 0.6B", reasoning: REASONING, thinkMs: 6400, status: "done", stats: { tps: 9.6, n: 512, ttft: 1800, promptN: 40, cached: 12 } }),
    msg("u2", "a2", "user", LONG_USER),
    msg("a3", "u2", "assistant", "", { model: "Qwen3 0.6B", status: "done", error: "Lost contact with the engine. It may have run out of memory. Reload the model from the model menu and try again." }),
    msg("u3", "a3", "user", "Write a long story."),
    msg("a4", "u3", "assistant", "Once upon a time, in a kingdom of sorted arrays, there lived a pivot named Quinn…", { model: "Qwen3 0.6B", status: "done", finish: "length", stats: { tps: 10.2, n: 2048 } }),
  ], { sel: { u1: "a2" } });
  // A chat with a generated picture, an audio clip and a video.
  const upload = (b64, ext) =>
    page.evaluate(async ([b64, ext]) => {
      const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      return (await (await fetch("/api/media?ext=" + ext, { method: "POST", body: bin })).json()).id;
    }, [b64, ext]);
  const png = fs.readFileSync(pngPath).toString("base64");
  const pic = await upload(png, "png");
  const wav = Buffer.alloc(44 + 16000);
  wav.write("RIFF", 0); wav.writeUInt32LE(36 + 16000, 4); wav.write("WAVEfmt ", 8); wav.writeUInt32LE(16, 16); wav.writeUInt16LE(1, 20); wav.writeUInt16LE(1, 22);
  wav.writeUInt32LE(16000, 24); wav.writeUInt32LE(32000, 28); wav.writeUInt16LE(2, 32); wav.writeUInt16LE(16, 34); wav.write("data", 36); wav.writeUInt32LE(16000, 40);
  const audio = await upload(wav.toString("base64"), "wav");
  const pictures = buildChat("pictures", "Cozy cabin picture", now - 0.05 * day, [
    msg("p1", "root", "user", "Draw a cozy cabin in a snowy forest at night with warm light in the windows"),
    msg("p2", "p1", "assistant", "", { model: "SD Turbo", status: "done", image: { status: "done", media: pic, width: 512, height: 512, prompt: "a cozy cabin in a snowy forest at night with warm light in the windows", seconds: 12.4, model: "SD Turbo", seed: 7 } }),
    msg("p3", "p2", "user", "Here's the sound of the wind and a clip of the walk there. What do you think?", { attachments: [
      { type: "audio", name: "wind-recording-from-the-cabin-trip.wav", media: audio, duration: 92, size: 2400000 },
      { type: "video", name: "walk.mp4", duration: 31, frames: [0, 1, 2, 3].map((i) => ({ media: pic, t: i * 8 })), size: 8000000 },
      { type: "text", name: "trip-plan.pdf", text: "--- Page 1 ---\nPlan", size: 120000, doc: "pdf" },
    ] }),
    msg("p4", "p3", "assistant", "The recording sounds calm, and the video shows a lovely snowy path.", { model: "Qwen3 0.6B", status: "done", stats: { tps: 9.1, n: 40 } }),
  ]);
  const chats = [showcase, pictures];
  const titles = [
    "Plan a weekend trip to the coast with a tight budget and a dog",
    "Fix my Python import error",
    "Recipe ideas for leftover rice",
    "Explain the difference between TCP and UDP in simple words for a beginner",
    "Draft a polite email to my landlord",
    "Workout plan",
    "Summarize this article about renewable energy policy in Europe and its effects",
    "What is a GGUF file",
    "Birthday message for grandma",
    "SQL query help",
    "Learn Spanish basics",
    "Resume bullet points for a junior developer role at a startup",
  ];
  const offsets = [0.1, 0.3, 1, 1.2, 2, 4, 6, 9, 15, 25, 45, 400];
  titles.forEach((t, i) => {
    chats.push(buildChat("c" + i, t, now - offsets[i] * day, [msg("x" + i, "root", "user", t), msg("y" + i, "x" + i, "assistant", "Sure, here you go.", { status: "done", model: "Qwen3 0.6B" })], { pinned: i === 4 || i === 9 }));
  });
  for (const c of chats) {
    await page.evaluate(async (c) => {
      await fetch("/api/chats/" + c.id, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });
    }, c);
  }
}

// Finds overlapping, clipped and overflowing elements on the current screen.
async function audit(page, label) {
  const found = await page.evaluate(() => {
    const out = [];
    const vis = (el) => {
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return r.width > 0 && r.height > 0 && cs.visibility !== "hidden" && cs.display !== "none" && Number(cs.opacity) > 0.05;
    };
    const name = (el) => (el.getAttribute("aria-label") || el.className || el.tagName).toString().slice(0, 50) + (el.textContent ? ` "${el.textContent.trim().slice(0, 30)}"` : "");
    // 1) the page itself must never scroll sideways
    if (document.documentElement.scrollWidth > innerWidth + 1) out.push(`page scrolls horizontally (${document.documentElement.scrollWidth} > ${innerWidth})`);
    // 2) fixed layers must stay inside the window
    for (const el of document.querySelectorAll(".menu, .modal, .toast, .tooltip")) {
      if (!vis(el)) continue;
      const r = el.getBoundingClientRect();
      if (r.left < -1 || r.top < -1 || r.right > innerWidth + 1 || r.bottom > innerHeight + 1) out.push(`${name(el)} goes outside the window`);
    }
    // 3) text that spills out of its box (not ellipsized / not scrollable)
    for (const el of document.querySelectorAll("button, .nav-item, .chat-title, .mm-name, .mm-meta, .row-title, .row-desc, .profile-name, .profile-sub, .ms-name, .badge, .status-pill, .hero-title, .lc-name, .lc-sub, .search-item, .settings-tab, .menu-label, .chip-name, .att-name, .model-tag, .stats, .footnote, h1, h2, h3")) {
      if (!vis(el)) continue;
      const cs = getComputedStyle(el);
      const clips = cs.overflow !== "visible" || cs.textOverflow === "ellipsis";
      if (!clips && el.scrollWidth > el.clientWidth + 2 && el.clientWidth > 0) out.push(`text spills out of ${name(el)} (${el.scrollWidth} > ${el.clientWidth})`);
    }
    // 4) interactive things that overlap each other
    // The "scroll to latest" button floats over messages by design (as in
    // ChatGPT), and on narrow windows the open sidebar is a layer over the page.
    const sel = "button:not([disabled]), a[href], input, textarea, select, .chip, .bubble, .composer, .status-pill, .model-tag, .stats, .versions";
    const mobileSidebar = document.querySelector(".sidebar.open");
    const els = [...document.querySelectorAll(sel)].filter((e) => vis(e) && !e.closest(".menu, .modal, .overlay, .toasts, .tooltip, .scroll-down") && !(mobileSidebar && !mobileSidebar.contains(e)));
    const layered = [...document.querySelectorAll(".menu, .overlay, .toasts .toast, .tooltip")].filter(vis);
    // Clip each element to its scrolling ancestors: content scrolled out of
    // view isn't on screen, so it can't visibly overlap anything.
    const clipped = (e) => {
      const r = e.getBoundingClientRect();
      let l = r.left, t = r.top, rt = r.right, b = r.bottom;
      for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) {
        const cs = getComputedStyle(p);
        if (cs.overflow !== "visible" || cs.overflowY !== "visible" || cs.overflowX !== "visible") {
          const pr = p.getBoundingClientRect();
          l = Math.max(l, pr.left); t = Math.max(t, pr.top); rt = Math.min(rt, pr.right); b = Math.min(b, pr.bottom);
        }
      }
      return { left: l, top: t, right: rt, bottom: b };
    };
    const rects = els.map((e) => [e, clipped(e)]).filter(([, r]) => r.right - r.left > 1 && r.bottom - r.top > 1);
    for (let i = 0; i < rects.length; i++) {
      for (let j = i + 1; j < rects.length; j++) {
        const [a, ra] = rects[i], [b, rb] = rects[j];
        if (a.contains(b) || b.contains(a)) continue;
        // A sidebar row's "…" button sits over the faded end of its title.
        if (a.closest(".chat-item") && a.closest(".chat-item") === b.closest(".chat-item")) continue;
        const ix = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left);
        const iy = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top);
        if (ix > 2 && iy > 2) out.push(`${name(a)} overlaps ${name(b)}`);
      }
    }
    // 5) toasts must not cover the message box
    const comp = document.querySelector(".composer");
    for (const t of document.querySelectorAll(".toast")) {
      if (!comp || !vis(t)) continue;
      const a = t.getBoundingClientRect(), b = comp.getBoundingClientRect();
      if (Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 0 && Math.min(a.right, b.right) - Math.max(a.left, b.left) > 0) out.push("a toast covers the message box");
    }
    void layered;
    return [...new Set(out)];
  });
  for (const f of found) issues.push(`[${label}] ${f}`);
}

(async () => {
  const browser = await chromium.launch();
  const sizes = [
    ["laptop", 1366, 768],
    ["fullhd", 1920, 1080],
    ["small", 1024, 640],
    ["narrow", 420, 860],
  ];
  // First-run screens: welcome, loading, a model that fails to load.
  {
    const ctx = await browser.newContext({ viewport: { width: 1366, height: 768 }, deviceScaleFactor: 1 });
    const page = await ctx.newPage();
    await page.goto(url);
    await page.waitForSelector(".center-card", { timeout: 30000 });
    await page.waitForTimeout(300);
    await audit(page, "welcome-empty");
    await page.screenshot({ path: path.join(outDir, "00-welcome-empty-light.png") });
    await page.evaluate(() => (document.documentElement.dataset.theme = "dark"));
    await page.screenshot({ path: path.join(outDir, "00-welcome-empty-dark.png") });
    await page.evaluate(() => (document.documentElement.dataset.theme = "light"));
    await page.getByRole("button", { name: "Add model files" }).click();
    await page.waitForSelector(".load-card", { timeout: 20000 });
    await page.waitForTimeout(150);
    await audit(page, "loading");
    await page.screenshot({ path: path.join(outDir, "00-loading.png") });
    await page.waitForSelector(".hero-title", { timeout: 120000 });
    await page.locator(".model-switch").click();
    await page.locator(".model-menu .mm-row", { hasText: /broken/i }).click();
    await page.waitForSelector(".load-card[role=alert]", { timeout: 60000 });
    await page.waitForTimeout(300);
    await audit(page, "load-error");
    await page.screenshot({ path: path.join(outDir, "00-load-error.png") });
    await page.locator(".model-switch").click();
    await page.locator(".model-menu .mm-row", { hasText: /Qwen3/ }).click();
    await page.waitForSelector(".hero-title", { timeout: 120000 });
    await ctx.close();
  }
  let seeded = false;
  for (const theme of ["light", "dark"]) {
    for (const [sizeName, w, hgt] of sizes) {
      const ctx = await browser.newContext({ viewport: { width: w, height: hgt }, colorScheme: theme, deviceScaleFactor: 1 });
      const page = await ctx.newPage();
      page.on("pageerror", (e) => issues.push(`[${theme}-${sizeName}] page error: ${e.message}`));
      await page.goto(url);
      await page.waitForSelector(".hero-title, .center-card, .load-card", { timeout: 60000 });
      if (!seeded) {
        await seed(page);
        seeded = true;
      }
      await page.evaluate((t) => fetch("/api/settings").then((r) => r.json()).then((s) => fetch("/api/settings", { method: "PUT", body: JSON.stringify({ ...s, theme: t }) })), theme);
      await page.reload();
      await page.waitForSelector(".hero-title, .center-card", { timeout: 60000 });
      await page.waitForTimeout(400);
      const tag = `${theme}-${sizeName}`;
      const shot = async (n) => {
        await page.waitForTimeout(250);
        await audit(page, `${tag} ${n}`);
        await page.screenshot({ path: path.join(outDir, `${tag}-${n}.png`) });
      };
      await shot("01-home");
      if (sizeName === "narrow") {
        await page.getByRole("button", { name: "Open sidebar" }).click();
        await shot("02-sidebar-open");
        await page.locator(".chat-link", { hasText: "Sorting algorithms" }).click();
      } else {
        await page.locator(".chat-link", { hasText: "Sorting algorithms" }).click();
      }
      await page.waitForTimeout(500);
      await shot("03-chat-top");
      await page.locator(".thinking-head").first().click();
      await page.waitForTimeout(200);
      await shot("04-thinking-open");
      await page.locator("#content").evaluate((c) => (c.scrollTop = c.scrollHeight / 2));
      await shot("05-chat-middle");
      await page.locator("#content").evaluate((c) => (c.scrollTop = c.scrollHeight));
      await shot("06-chat-bottom");
      // composer with attachments and a few lines
      await page.locator('.composer input[type="file"]').setInputFiles({ name: "budget-2026.csv", mimeType: "text/csv", buffer: Buffer.from("a,b\n1,2\n") });
      await page.locator("#composer-input").fill("Line one of a draft\nLine two\nLine three");
      await shot("07-composer-filled");
      await page.locator("#composer-input").fill("");
      // the picture chat
      if (sizeName === "narrow") await page.getByRole("button", { name: "Open sidebar" }).click();
      await page.locator(".chat-link", { hasText: "Cozy cabin" }).click();
      await page.waitForSelector(".gen-image");
      await page.locator("#content").evaluate((c) => (c.scrollTop = 0));
      await shot("07b-picture");
      await page.locator("#content").evaluate((c) => (c.scrollTop = c.scrollHeight));
      await shot("07c-audio-video");
      // image mode in the message box
      await page.getByRole("button", { name: "Add photos and files" }).click();
      await page.locator(".menu .menu-item", { hasText: "Create image" }).click().catch(() => {});
      await shot("07d-image-mode");
      await page.locator(".image-pill .pill-x").click().catch(() => {});
      if (sizeName === "narrow") {
        await ctx.close();
        continue;
      }
      // menus
      await page.locator(".model-switch").click();
      await page.waitForSelector(".model-menu");
      await shot("08-model-menu");
      await page.keyboard.press("Escape");
      await page.locator(".turn.assistant.latest").hover();
      await page.getByRole("button", { name: "Try again" }).last().click();
      await shot("09-retry-menu");
      await page.keyboard.press("Escape");
      const item = page.locator(".chat-item").nth(2);
      await item.hover();
      await item.locator(".chat-more").click();
      await shot("10-chat-menu");
      await page.keyboard.press("Escape");
      await page.locator(".profile-btn").click();
      await shot("11-profile-menu");
      await page.keyboard.press("Escape");
      // tooltip
      await page.locator(".turn.assistant.latest").hover();
      await page.locator('.turn.assistant.latest button[aria-label="Read aloud"]').hover();
      await page.waitForTimeout(700);
      await shot("12-tooltip");
      // modals
      for (const [tab, n] of [["General", "13"], ["Personalization", "14"], ["Model & performance", "15"], ["Pictures", "15b"], ["Speech", "16"], ["Data controls", "17"], ["About", "18"]]) {
        if (tab === "General") await page.keyboard.press("Control+,");
        await page.getByRole("tab", { name: tab }).click();
        await shot(`${n}-settings-${tab.split(" ")[0].toLowerCase()}`);
      }
      await page.keyboard.press("Escape");
      await page.keyboard.press("Control+k");
      await page.waitForSelector(".search-modal");
      await shot("19-search");
      await page.keyboard.type("sort");
      await page.waitForTimeout(400);
      await shot("20-search-results");
      await page.keyboard.press("Escape");
      await page.getByRole("button", { name: "Models", exact: true }).click();
      await page.waitForSelector(".models-modal .mgr-row");
      await shot("21-models");
      await page.getByRole("tab", { name: "Image models" }).click();
      await page.waitForSelector(".models-modal .img-row, .models-modal .img-empty");
      await shot("21b-image-models");
      await page.keyboard.press("Escape");
      await page.keyboard.press("Control+/");
      await shot("22-shortcuts");
      await page.keyboard.press("Escape");
      // confirm dialog
      const it2 = page.locator(".chat-item").nth(3);
      await it2.hover();
      await it2.locator(".chat-more").click();
      await page.getByRole("menuitem", { name: "Delete" }).click();
      await shot("23-confirm");
      await page.getByRole("button", { name: "Cancel" }).click();
      // toast + composer
      await page.evaluate(() => document.querySelector(".code-copy")?.click());
      await page.locator(".turn.assistant").first().hover();
      await page.locator('.turn.assistant button[aria-label="Copy"]').first().click();
      await shot("24-toast-or-copied");
      // edit mode
      await page.locator(".turn.user").first().hover();
      await page.locator('.turn.user button[aria-label="Edit message"]').first().click();
      await shot("25-edit");
      await page.keyboard.press("Escape");
      // collapsed sidebar
      await page.keyboard.press("Control+Shift+s");
      await page.waitForTimeout(300);
      await shot("26-collapsed");
      await page.keyboard.press("Control+Shift+s");
      // new chat + temporary
      await page.keyboard.press("Control+Shift+o");
      await shot("27-new-chat");
      await page.locator('button[aria-label="Turn on temporary chat"]').click();
      await shot("28-temporary");
      await page.locator('button[aria-label="Turn off temporary chat"]').click();
      await ctx.close();
    }
  }
  await browser.close();
  fs.writeFileSync(path.join(outDir, "issues.txt"), issues.join("\n"));
  console.log(issues.length ? issues.join("\n") : "no layout issues found");
  console.log(`\n${issues.length} issue(s)`);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
