// Choosing and loading models: the header's model menu and the welcome,
// loading and error screens shown before a chat starts.
import { S, actions, emit } from "./state.js";
import { api } from "./api.js";
import { h, formatBytes, formatCtx, formatParams } from "./util.js";
import { icon } from "./icons.js";
import { openMenu, openModal, closeMenu, toast } from "./ui.js";
import { LOGO } from "./sidebar.js";
import { refreshImages, setDefaultImage } from "./images.js";

const SPEED = { fast: "Fast", good: "Good speed", slow: "Slow", very_slow: "Very slow" };
const FIT = { fits: "Fits in memory", tight: "Tight on memory", too_big: "Too big for this PC" };

export function modelLabel(entry) {
  if (!entry) return "";
  return entry.name || entry.fileName || "Model";
}

export function metaLine(e, extra = {}) {
  const parts = [];
  const p = e.sizeLabel || formatParams(e.params);
  if (p) parts.push(p);
  if (e.paramsActive && e.params && e.paramsActive < e.params * 0.7) parts.push(`${formatParams(e.paramsActive)} active`);
  if (e.quant) parts.push(e.quant);
  if (e.size) parts.push(formatBytes(e.size));
  if (extra.ctx) parts.push(`${formatCtx(extra.ctx)} context`);
  return parts.join(" · ");
}

export function badges(e, v = {}) {
  const b = [];
  if (e.reasoning) b.push(h("span", { class: "badge", text: "Thinking" }));
  if (e.mmproj) b.push(h("span", { class: "badge", text: "Vision" }));
  if (e.source === "ollama") b.push(h("span", { class: "badge", text: "Ollama" }));
  if (v.fit === "too_big") b.push(h("span", { class: "badge too_big", text: "Too big" }));
  else if (v.speed) b.push(h("span", { class: "badge " + v.speed, text: SPEED[v.speed] || v.speed, "data-tip": v.estTps ? `About ${v.estTps < 10 ? v.estTps.toFixed(1) : Math.round(v.estTps)} tokens per second on this PC${v.learned?.tokensPerSec ? " (measured)" : " (estimate)"}` : null }));
  return h("span", { class: "badges" }, b);
}

function description(v) {
  const parts = [];
  if (v.reasoning) parts.push("Thinking");
  if (v.mmproj) parts.push("Vision");
  const p = v.sizeLabel || formatParams(v.params);
  if (p) parts.push(p);
  if (v.quant) parts.push(v.quant);
  if (v.source === "ollama") parts.push("Ollama");
  return parts.join(" · ");
}

function speedText(v) {
  if (v.missing) return "File not found";
  if (v.fit === "too_big" && !v.device) return "Too big for this PC";
  if (v.device && v.speed) return `${SPEED[v.speed]} on the graphics card`;
  return v.speed ? `${SPEED[v.speed]} on this PC` : "";
}

/** "About 9 seconds per picture" for an image model. */
function pictureSpeed(m) {
  if (!m.estSecs) return "";
  const s = Math.round(m.estSecs);
  return s < 90 ? `about ${s}s per picture` : `about ${Math.round(s / 60)} min per picture`;
}

function imageDescription(m) {
  if (m.missingFile) return "File not found";
  if (!m.ready) return `${m.kind} · needs ${m.missing.map((x) => x.label).join(", ")}`;
  return [m.kind, formatBytes(m.size), pictureSpeed(m), m.device ? "graphics card" : ""].filter(Boolean).join(" · ");
}

const byName = (a, b) => modelLabel(a).localeCompare(modelLabel(b), undefined, { numeric: true, sensitivity: "base" });

function modelRow(v, { onClick, current, status }) {
  let right = null;
  if (current && status === "loading") right = h("span", { class: "mm-status", text: `${Math.round((S.engine.progress || 0) * 100)}%` });
  else if (current && status === "error") right = h("span", { class: "mm-status error", html: icon("circle-alert", 18), "data-tip": "Didn't load" });
  else if (current) right = h("span", { class: "mm-check", html: icon("check", 18) });
  const speed = speedText(v);
  return h(
    "button",
    { type: "button", class: "mm-row" + (v.missing ? " missing" : "") + (current ? " current" : ""), onclick: onClick, disabled: v.missing, "aria-current": current ? "true" : null },
    h(
      "span",
      { class: "mm-body" },
      h("div", { class: "mm-name", text: modelLabel(v) }),
      h("div", { class: "mm-meta" }, h("span", { text: description(v) }), speed ? h("span", { class: "mm-speed " + (v.fit === "too_big" ? "too_big" : v.speed || ""), text: (description(v) ? " · " : "") + speed }) : null),
    ),
    right,
  );
}

export async function refreshModels() {
  try {
    const r = await api.models();
    S.models = r.models || [];
    S.modelFolders = r.folders || [];
  } catch {}
  return S.models;
}

/** Opens the ChatGPT-style model dropdown under (or beside) anchor. */
export async function openModelMenu(anchor, side = "bottom") {
  await refreshModels();
  const curPath = S.model?.entry?.path;
  const list = S.models.filter((m) => !m.missing).sort(byName);
  const items = [{ heading: "Models" }];
  if (!list.length) items.push({ custom: h("div", { class: "mm-empty", text: "No models yet. Add your GGUF files to get started." }) });
  else
    items.push({
      custom: h(
        "div",
        { class: "mm-list" },
        list.map((v) =>
          modelRow(v, {
            current: v.path === curPath,
            status: v.path === curPath ? S.engine.status : "",
            onClick: () => {
              closeMenu();
              if (v.path === curPath && (S.engine.status === "ready" || S.engine.status === "loading")) return;
              switchModel(v.path);
            },
          }),
        ),
      ),
    });
  await refreshImages();
  const imgs = (S.images?.models || []).filter((m) => !m.missingFile);
  if (imgs.length) {
    items.push({ heading: "Image model" });
    items.push({
      custom: h(
        "div",
        { class: "mm-list" },
        imgs.map((m) =>
          h(
            "button",
            {
              type: "button",
              class: "mm-row" + (m.path === S.images.default ? " current" : ""),
              "aria-current": m.path === S.images.default ? "true" : null,
              onclick: () => {
                closeMenu();
                if (!m.ready) return openModelsManager("images");
                setDefaultImage(m.path).then(() => toast(`Pictures will be made with ${m.name}`));
              },
            },
            h("span", { class: "mm-icon", html: icon("image", 18) }),
            h("span", { class: "mm-body" }, h("div", { class: "mm-name", text: m.name }), h("div", { class: "mm-meta" + (m.ready ? "" : " warn"), text: imageDescription(m) })),
            m.path === S.images.default ? h("span", { class: "mm-check", html: icon("check", 18) }) : null,
          ),
        ),
      ),
    });
  }
  items.push({ divider: true });
  items.push({ label: "Add models…", icon: "plus", hint: "Chat models (.gguf) or image models", onClick: () => addModels() });
  items.push({ label: "Find models on this PC", icon: "text-search", onClick: () => scan(true) });
  items.push({ label: "Manage models", icon: "sliders-horizontal", onClick: openModelsManager });
  if (curPath && S.engine.status !== "idle") items.push({ label: "Unload model", icon: "x", hint: "Frees the memory it uses", onClick: unload });
  openMenu(anchor, items, { className: "model-menu", side, align: "start", width: 360, role: "dialog", label: "Models" });
}

/** Switches the active model; an open chat simply continues with it. */
export function switchModel(path) {
  const v = S.models.find((m) => m.path === path);
  const midChat = actions.hasMessages?.();
  loadModel(path);
  if (midChat && v) toast(`Switching to ${modelLabel(v)}. Keep chatting: your next message is sent as soon as it's ready.`);
}

/** Lets the user pick several GGUF files and adds them to the model list. */
export async function addModels() {
  try {
    const r = await api.addModels();
    if (r?.cancelled) return [];
    await refreshModels();
    const added = r.added || [];
    const addedImages = r.addedImages || [];
    const failed = r.failed || [];
    if (failed.length) {
      const name = failed[0].path.split(/[\\/]/).pop();
      toast(failed.length === 1 ? `${name}: ${failed[0].error}` : `${failed.length} files couldn't be added. ${name}: ${failed[0].error}`, { kind: "error", timeout: 9000 });
    }
    if (added.length) toast(added.length === 1 ? `Added ${modelLabel(added[0])}` : `Added ${added.length} models`);
    if (addedImages.length) {
      await refreshImages();
      const needs = addedImages.find((m) => !m.ready);
      if (needs) {
        toast(`${needs.name} needs ${needs.missing.map((x) => x.label).join(", ")}. Put the files in the same folder, or choose them in Models.`, { timeout: 9000, action: { label: "Choose files", onClick: () => openModelsManager("images") } });
      } else {
        toast(addedImages.length === 1 ? `Added the image model ${addedImages[0].name}. Ask for a picture to try it.` : `Added ${addedImages.length} image models`);
      }
    }
    if (added.length && (!S.model || S.engine.status === "idle")) loadModel(added[0].path);
    emit("models");
    return added;
  } catch (e) {
    toast(e.message, { kind: "error" });
    return [];
  }
}

const SLOT_ORDER = ["model", "diffusion_model", "clip_l", "clip_g", "t5xxl", "llm", "llm_vision", "vae", "tae", "tokenizer", "embeddings_connectors", "uncond_diffusion_model", "control_net"];
const SLOT_NAMES = { model: "Model", diffusion_model: "Diffusion model", clip_l: "CLIP-L text encoder", clip_g: "CLIP-G text encoder", t5xxl: "T5-XXL text encoder", llm: "Text encoder", llm_vision: "Vision projector", vae: "VAE", tae: "Tiny VAE", tokenizer: "Tokenizer", embeddings_connectors: "Text connector", uncond_diffusion_model: "Unconditional model", control_net: "ControlNet" };
const fileName = (p) => p.split(/[\\/]/).pop();

/** GPU summary for the Models window and settings. */
export function gpuSummary() {
  const g = S.gpu;
  if (!g?.checked) return "Checking for a graphics card…";
  if (!g.best) return "No usable graphics card found, so models run on the processor. RabAI is tuned for that.";
  const mode = S.settings.perf.gpu || "auto";
  if (mode === "off") return `${g.best.desc} found, but the graphics card is turned off in Settings.`;
  return `${g.best.desc}${g.best.totalMiB ? ` (${(g.best.totalMiB / 1024).toFixed(g.best.totalMiB < 10240 ? 1 : 0)} GB)` : ""}: each model is tested once and uses it only if it's faster and gives correct results.`;
}

function imageModelsPane(body, repaint) {
  const im = S.images || { models: [] };
  const list = im.models || [];
  const head = h(
    "div",
    { class: "mgr-actions" },
    h("button", { type: "button", class: "btn btn-primary sm", onclick: () => addModels().then(repaint) }, h("span", { html: icon("plus", 16) }), "Add image models"),
  );
  if (!im.available) return [h("p", { class: "row-desc", text: "This version of RabAI has no image engine." })];
  const rows = list.length
    ? h(
        "div",
        { class: "mgr-list" },
        list.map((m) => {
          const isDefault = m.path === im.default;
          const parts = SLOT_ORDER.filter((s) => m.parts?.[s]).map((s) => [s, m.parts[s]]);
          const missing = m.missing || [];
          return h(
            "div",
            { class: "mgr-row img-row" + (m.missingFile ? " missing" : "") },
            h("div", { class: "mm-icon lg", html: icon("image", 20) }),
            h(
              "div",
              { class: "mgr-body" },
              h("div", { class: "mm-name", text: m.name }),
              h("div", { class: "mm-meta", text: m.missingFile ? "File not found" : [m.kind, formatBytes(m.size), pictureSpeed(m)].filter(Boolean).join(" · ") }),
              missing.length
                ? h(
                    "div",
                    { class: "img-missing" },
                    missing.map((x) =>
                      h(
                        "div",
                        { class: "img-need" },
                        h("span", { html: icon("triangle-alert", 16) }),
                        h("span", { text: `Needs ${x.label}` }),
                        h("button", { type: "button", class: "btn btn-secondary xs", text: "Choose file…", onclick: () => pickPart(m, x.slot).then(repaint) }),
                      ),
                    ),
                    h("button", { type: "button", class: "link-btn", text: "Look again in the model's folder", onclick: () => api.imageAction("refresh", m.path).then(repaint) }),
                  )
                : null,
              parts.length > 1
                ? h(
                    "details",
                    { class: "img-parts" },
                    h("summary", { text: `${parts.length} files` }),
                    parts.map(([slot, file]) =>
                      h(
                        "div",
                        { class: "img-part" },
                        h("span", { class: "k", text: SLOT_NAMES[slot] || slot }),
                        h("span", { class: "v", text: fileName(file), title: file }),
                        slot !== "model" && slot !== "diffusion_model" ? h("button", { type: "button", class: "link-btn", text: "Change", onclick: () => pickPart(m, slot).then(repaint) }) : null,
                      ),
                    ),
                  )
                : null,
              m.learned?.reason ? h("div", { class: "mgr-path", text: m.learned.reason }) : null,
            ),
            h(
              "div",
              { class: "mgr-buttons" },
              isDefault
                ? h("span", { class: "badge", text: "Default" })
                : h("button", { type: "button", class: "btn btn-secondary sm", text: "Use", disabled: !m.ready, "data-tip": m.ready ? "Use for pictures" : "Add the missing files first", onclick: () => setDefaultImage(m.path).then(repaint) }),
              h("button", { type: "button", class: "icon-btn sm", "aria-label": `Show ${m.name} in File Explorer`, "data-tip": "Show in folder", disabled: m.missingFile, html: icon("folder-open", 18), onclick: () => api.reveal(m.path).catch((e) => toast(e.message)) }),
              h("button", { type: "button", class: "icon-btn sm", "aria-label": `Remove ${m.name} from the list`, "data-tip": "Remove from list (keeps the files)", html: icon("x", 18), onclick: async () => { await api.imageAction("remove", m.path); await refreshImages(); repaint(); } }),
            ),
          );
        }),
      )
    : h(
        "div",
        { class: "img-empty" },
        h("p", { class: "row-desc", text: "No image models yet. Add a Stable Diffusion, SDXL, FLUX, Z-Image, Qwen Image or similar model file (.gguf or .safetensors). RabAI finds the text encoders and VAE it needs in the same or nearby folders." }),
        h("p", { class: "row-desc", text: "For older laptops, a small fast model such as SD-Turbo (about 2 GB) makes a picture in seconds to a minute." }),
      );
  return [head, rows];
}

async function pickPart(m, slot) {
  try {
    const r = await api.imageAction("part", m.path, { slot, pick: true });
    if (r?.cancelled) return;
    await refreshImages();
    if (r && !r.ready) toast(`Still needed: ${r.missing.map((x) => x.label).join(", ")}`);
    else toast(`${m.name} is ready to make pictures`);
  } catch (e) {
    toast(e.message, { kind: "error" });
  }
}

/** The "Models" window: every model RabAI knows, plus the folders it watches. */
export function openModelsManager(tab = "chat") {
  closeMenu();
  let current = tab === "images" ? "images" : "chat";
  openModal({
    title: "Models",
    className: "models-modal",
    content: (body) => {
      const tabs = () =>
        h(
          "div",
          { class: "seg mgr-tabs", role: "tablist", "aria-label": "Model kinds" },
          [["chat", "Chat models"], ["images", "Image models"]].map(([id, label]) =>
            h("button", { type: "button", role: "tab", class: "seg-btn" + (current === id ? " on" : ""), "aria-selected": String(current === id), text: label, onclick: () => ((current = id), paint()) }),
          ),
        );
      const gpuLine = () => h("div", { class: "gpu-line" }, h("span", { html: icon("cpu", 16) }), h("span", { text: gpuSummary() }));
      const paint = async () => {
        if (current === "images") {
          await refreshImages();
          body.replaceChildren(tabs(), gpuLine(), ...imageModelsPane(body, paint));
          return;
        }
        await refreshModels();
        const curPath = S.model?.entry?.path;
        const list = [...S.models].sort(byName);
        const actionsRow = h(
          "div",
          { class: "mgr-actions" },
          h("button", { type: "button", class: "btn btn-primary sm", onclick: () => addModels().then(paint) }, h("span", { html: icon("plus", 16) }), "Add models"),
          h("button", { type: "button", class: "btn btn-secondary sm", onclick: () => scan(true).then(paint) }, h("span", { html: icon("text-search", 16) }), "Find on this PC"),
          h("button", { type: "button", class: "btn btn-secondary sm", onclick: () => addFolder().then(paint) }, h("span", { html: icon("folder-plus", 16) }), "Add a folder"),
        );
        const rows = list.length
          ? h(
              "div",
              { class: "mgr-list" },
              list.map((v) => {
                const inUse = v.path === curPath && S.engine.status !== "idle";
                return h(
                  "div",
                  { class: "mgr-row" + (v.missing ? " missing" : "") },
                  h(
                    "div",
                    { class: "mgr-body" },
                    h("div", { class: "mm-name", text: modelLabel(v) }),
                    h("div", { class: "mm-meta", text: v.missing ? "File not found" : metaLine(v) }),
                    h("div", { class: "mgr-path", text: "\u200e" + v.path + "\u200e", title: v.path }),
                    v.missing ? null : badges(v, v),
                  ),
                  h(
                    "div",
                    { class: "mgr-buttons" },
                    inUse
                      ? h("span", { class: "badge", text: S.engine.status === "loading" ? "Loading" : S.engine.status === "error" ? "Didn't load" : "In use" })
                      : h("button", { type: "button", class: "btn btn-secondary sm", text: "Use", disabled: v.missing, onclick: () => (switchModel(v.path), setTimeout(paint, 300)) }),
                    h("button", { type: "button", class: "icon-btn sm", "aria-label": `Show ${modelLabel(v)} in File Explorer`, "data-tip": "Show in folder", disabled: v.missing, html: icon("folder-open", 18), onclick: () => api.reveal(v.path).catch((e) => toast(e.message)) }),
                    h("button", { type: "button", class: "icon-btn sm", "aria-label": `Remove ${modelLabel(v)} from the list`, "data-tip": "Remove from list (keeps the file)", disabled: inUse, html: icon("x", 18), onclick: async () => { await api.forget(v.path); paint(); } }),
                  ),
                );
              }),
            )
          : h("p", { class: "row-desc", text: "No models yet. Add your GGUF files, or let RabAI look for them." });
        const folders = S.modelFolders || [];
        body.replaceChildren(
          tabs(),
          gpuLine(),
          actionsRow,
          rows,
          h("h3", { class: "mgr-sub", text: "Folders" }),
          h("p", { class: "row-desc", text: "RabAI checks Downloads, Desktop, Documents and the LM Studio and Ollama folders, plus these:" }),
          folders.length
            ? h("div", {}, folders.map((f) => h("div", { class: "archived-row" }, h("span", { class: "t", text: f, title: f }), h("button", { type: "button", class: "icon-btn sm", "aria-label": `Stop watching ${f}`, "data-tip": "Remove folder", html: icon("x", 16), onclick: async () => { await api.removeFolder(f); paint(); } }))))
            : h("p", { class: "row-desc", text: "No extra folders." }),
        );
      };
      paint();
    },
  });
}

export async function loadModel(path) {
  // A reply in progress can't survive the engine restart; a message that is
  // only waiting for a model simply waits for the new one.
  if (S.generating && !S.generating.waiting) actions.stop?.();
  const before = S.engine;
  const beforeModel = S.model;
  // Show the switch right away instead of waiting for the engine's first event.
  const known = S.models.find((m) => m.path === path);
  if (known) {
    S.model = { entry: known, profile: {} };
    S.props = null;
    emit("model");
  }
  actions.setEngineState?.({ ...S.engine, status: "loading", progress: 0, stage: "starting", error: "", errorDetail: "", modelPath: path });
  try {
    const r = await api.load(path, S.settings.perf, S.settings.idleUnloadMin);
    S.model = r;
    S.props = null;
    actions.updateSettingsSilently?.((s) => (s.lastModel = path));
    emit("model");
  } catch (e) {
    S.model = beforeModel;
    actions.setEngineState?.(before);
    emit("model");
    toast(e.message, { kind: "error", timeout: 7000 });
  }
}

async function unload() {
  await api.unload().catch(() => {});
  S.model = null;
  S.props = null;
  emit("model");
}

export async function scan(showToast) {
  emit("scanning", true);
  try {
    const r = await api.scan();
    await refreshModels();
    if (showToast) toast(r.found?.length ? `Found ${r.found.length} model${r.found.length === 1 ? "" : "s"} on this PC.` : "No GGUF models were found in the usual folders.");
    return r.found || [];
  } catch (e) {
    toast(e.message, { kind: "error" });
    return [];
  } finally {
    emit("scanning", false);
  }
}

async function addFolder() {
  try {
    const r = await api.addFolder();
    if (r?.cancelled) return;
    await refreshModels();
    toast(`Found ${r.found?.length || 0} model${r.found?.length === 1 ? "" : "s"}.`);
  } catch (e) {
    toast(e.message, { kind: "error" });
  }
}

function hfLink() {
  const ram = S.hw?.totalRam || 8 * 2 ** 30;
  const maxB = ram <= 8.5 * 2 ** 30 ? 4 : ram <= 16.5 * 2 ** 30 ? 9 : 15;
  return `https://huggingface.co/models?apps=llama.cpp&num_parameters=min:0,max:${maxB}B&sort=trending`;
}

// ---------- screens shown in the empty chat area ----------

/** Returns the content to show above the composer when no chat is open. */
export function preChatView() {
  const st = S.engine.status;
  if (st === "loading" || (st === "idle" && S.pendingLoad)) return loadingCard();
  if (st === "error") return errorCard();
  if (st === "ready") return null;
  return welcome();
}

function loadingCard() {
  const e = S.model?.entry || {};
  const p = S.model?.profile || {};
  const pct = Math.round((S.engine.progress || 0) * 100);
  const stage = S.engine.stage === "mmproj_model" ? "Loading the image add-on…" : /^(Testing|Paused)/.test(S.engine.stage || "") ? S.engine.stage + "…" : pct > 0 ? "Loading into memory…" : "Starting the engine…";
  const notes = (p.notes || []).slice(0, 3);
  return h(
    "div",
    { class: "load-card", role: "status", "aria-live": "polite" },
    h("div", { class: "lc-name", text: modelLabel(e) || "Loading model" }),
    h("div", { class: "lc-sub", text: metaLine(e, { ctx: p.ctx }) }),
    h("div", { class: "lc-bar" + (pct > 0 ? "" : " indeterminate"), role: "progressbar", "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": pct > 0 ? String(pct) : null }, h("i", { style: pct > 0 ? `width:${pct}%` : "" })),
    h("div", { class: "lc-status" }, h("span", { text: stage }), h("span", { text: pct > 0 ? pct + "%" : "" })),
    notes.length ? h("ul", { class: "lc-notes" }, notes.map((n) => h("li", {}, h("span", { html: icon("info", 16) }), h("span", { text: n })))) : null,
  );
}

function errorCard() {
  const e = S.model?.entry;
  return h(
    "div",
    { class: "load-card", role: "alert" },
    h("div", { class: "lc-name", text: e ? `Couldn't load ${modelLabel(e)}` : "The engine stopped" }),
    h("p", { class: "lc-sub", style: "font-size:14px;color:var(--text-2);margin:8px 0 0", text: S.engine.error || "Something went wrong while loading the model." }),
    h(
      "div",
      { class: "notice-actions", style: "margin-top:14px" },
      e ? h("button", { type: "button", class: "btn btn-primary sm", text: "Try again", onclick: () => loadModel(e.path) }) : null,
      h("button", { type: "button", class: "btn btn-secondary sm", text: "Choose another model", onclick: (ev) => openModelMenu(ev.currentTarget) }),
    ),
    S.engine.errorDetail ? h("details", { class: "details" }, h("summary", { text: "Show details" }), h("pre", { text: S.engine.errorDetail })) : null,
  );
}

function welcome() {
  const known = S.models.filter((m) => !m.missing);
  const wrap = h("div", { class: "center-card" });
  wrap.append(
    h("div", { class: "big-logo", html: LOGO }),
    h("h1", { text: known.length ? "Choose a model to start" : "Welcome to RabAI" }),
    h("p", { text: known.length ? "Pick one of your models below. It runs entirely on this PC, so your chats stay private." : "Chat with AI models and make pictures, all on this PC. Choose a GGUF chat model or an image model you've downloaded to begin." }),
    h(
      "div",
      { class: "actions" },
      h("button", { type: "button", class: "btn btn-primary", onclick: () => addModels() }, h("span", { html: icon("plus", 18) }), "Add model files"),
      h("button", { type: "button", class: "btn btn-secondary", onclick: () => scan(true) }, h("span", { html: icon("text-search", 18) }), S.scanning ? "Searching…" : "Find models on this PC"),
    ),
  );
  if (known.length) {
    wrap.append(
      h("div", { class: "found-head", text: "Your models" }),
      h("div", { class: "found-list" }, known.slice(0, 8).map((v) => modelRow(v, { onClick: () => loadModel(v.path) }))),
    );
  } else {
    wrap.append(
      h(
        "p",
        { style: "margin-top:22px;font-size:13px;color:var(--text-3)" },
        "Need a model? ",
        h("a", { href: hfLink(), "data-external": "1", text: "Browse GGUF models that suit this PC" }),
        ". Small models (1–4B parameters, Q4_K_M) run best on older laptops.",
      ),
    );
  }
  return wrap;
}

/** The model button in the top bar. */
export function modelSwitch() {
  const e = S.model?.entry;
  const st = S.engine.status;
  const btn = h(
    "button",
    { type: "button", class: "model-switch", "aria-haspopup": "dialog", "aria-expanded": "false", "data-tip": "Choose a model", onclick: (ev) => openModelMenu(ev.currentTarget) },
    h("span", { class: "ms-name", text: e ? modelLabel(e) : "RabAI" }),
    e && st !== "ready" ? null : e ? null : h("span", { class: "ms-sub", text: "Choose a model" }),
    h("span", { class: "ms-chev", html: icon("chevron-down", 18) }),
  );
  return btn;
}

export function statusPill() {
  const st = S.engine;
  // In an empty chat the loading card already shows progress.
  if (!actions.hasMessages?.() && (st.status === "loading" || st.status === "error")) return null;
  if (st.status === "loading") {
    const pct = Math.round((st.progress || 0) * 100);
    return h("span", { class: "status-pill", role: "status" }, h("span", { text: "Loading" }), h("span", { class: "bar" }, h("i", { style: `width:${Math.max(pct, 4)}%` })), h("span", { text: pct ? pct + "%" : "" }));
  }
  if (st.status === "error") return h("span", { class: "status-pill", style: "color:var(--danger)", text: "Model didn't load" });
  return null;
}
