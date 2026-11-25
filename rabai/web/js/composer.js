// The message box: typing, attachments, the Think toggle, send and stop.
import { S, caps, emit, on, updateSettings } from "./state.js";
import { send, stop, thinkingEnabled, fileKind } from "./chat.js";
import { h, $, formatBytes, mod } from "./util.js";
import { icon } from "./icons.js";
import { openMenu, toast } from "./ui.js";
import { readAttachment, isAttachError, kindOf, mediaUrl, mediaDataUrl } from "./attach.js";
import { imagesReady, defaultImageModel, SIZES } from "./images.js";

const PASTE_TO_FILE = 10000;

let el, ta, chips, sendBtn, thinkPill, imagePill, plusBtn, fileInput;
let attachments = [];
let imageMode = null; // { size } while "Create image" is on

export function composerEl() {
  return el;
}

export function focusComposer() {
  ta?.focus({ preventScroll: true });
}

export function setComposerText(text) {
  ta.value = text;
  autosize();
  update();
  focusComposer();
}

export function buildComposer() {
  ta = h("textarea", {
    id: "composer-input",
    rows: 1,
    placeholder: "Ask anything",
    "aria-label": "Message",
    spellcheck: true,
    oninput: () => {
      autosize();
      update();
    },
    onkeydown: onKey,
    onpaste: onPaste,
  });
  chips = h("div", { class: "chips", hidden: true });
  fileInput = h("input", { type: "file", multiple: true, hidden: true, onchange: () => addFiles([...fileInput.files]).finally(() => (fileInput.value = "")) });
  plusBtn = h("button", { type: "button", class: "icon-btn", "aria-label": "Add photos and files", "data-tip": "Add photos and files", html: icon("plus", 20), onclick: openPlusMenu });
  thinkPill = h("button", { type: "button", class: "tool-pill", hidden: true, onclick: toggleThink });
  imagePill = h("div", { class: "tool-pill image-pill on", hidden: true });
  sendBtn = h("button", { type: "button", class: "send-btn", "aria-label": "Send message", "data-tip": "Send", onclick: onSendClick });
  el = h(
    "div",
    { class: "composer" },
    chips,
    h("div", { class: "composer-input-row" }, ta),
    h("div", { class: "composer-row" }, plusBtn, thinkPill, imagePill, h("div", { class: "spacer" }), sendBtn),
    fileInput,
  );
  installDragDrop();
  on("generating", update);
  on("caps", update);
  on("settings", update);
  on("engine", update);
  on("images", update);
  on("edit-picture", async ({ media, prompt }) => {
    try {
      const dataUrl = await mediaDataUrl(media);
      attachments = attachments.filter((a) => a.type !== "image");
      attachments.push({ type: "image", name: "Picture to edit", dataUrl, fromPrompt: prompt });
      setImageMode(true);
      renderChips();
      ta.value = "";
      ta.placeholder = "Describe the changes";
      autosize();
      update();
      focusComposer();
    } catch {
      toast("Couldn't open that picture.");
    }
  });
  update();
  return el;
}

/** Turns "Create image" mode on or off. */
export function setImageMode(on, size) {
  if (on && !imagesReady()) {
    emit("need-image-model");
    return;
  }
  imageMode = on ? { size: size || imageMode?.size || S.settings.images.size || "square" } : null;
  update();
  focusComposer();
}

function renderImagePill() {
  imagePill.hidden = !imageMode;
  if (!imageMode) return;
  const size = SIZES.find((x) => x.id === imageMode.size) || SIZES[0];
  imagePill.replaceChildren(
    h("button", { type: "button", class: "pill-main", "aria-label": "Picture shape: " + size.label, "data-tip": "Picture shape", onclick: (e) => openMenu(e.currentTarget, [{ heading: "Shape" }, ...SIZES.map((x) => ({ label: x.label, hint: x.hint, icon: x.icon, checked: x.id === imageMode.size, onClick: () => { imageMode.size = x.id; updateSettings((st) => (st.images.size = x.id)); renderImagePill(); } }))], { side: "top" }) },
      h("span", { html: icon("image", 18) }), h("span", { text: "Image" }), h("span", { class: "pill-sub", text: size.hint })),
    h("button", { type: "button", class: "pill-x", "aria-label": "Turn off image mode", "data-tip": "Turn off", html: icon("x", 14), onclick: () => setImageMode(false) }),
  );
}

function autosize() {
  ta.style.height = "auto";
  ta.style.height = Math.min(ta.scrollHeight, innerHeight * 0.4) + "px";
}

function canSend() {
  return (ta.value.trim().length > 0 || attachments.length > 0) && !attachments.some((a) => a.loading);
}

function update() {
  if (!el) return;
  const gen = !!S.generating;
  sendBtn.disabled = !gen && !canSend();
  sendBtn.innerHTML = gen ? '<span class="stop-square"></span>' : icon("arrow-up", 20);
  sendBtn.setAttribute("aria-label", gen ? "Stop generating" : "Send message");
  sendBtn.dataset.tip = gen ? "Stop" : "Send";
  if (gen) sendBtn.dataset.tipKbd = "Esc";
  else delete sendBtn.dataset.tipKbd;
  const c = caps();
  const ready = S.engine.status === "ready";
  thinkPill.hidden = !(ready && c.reasoning);
  const onNow = thinkingEnabled();
  thinkPill.classList.toggle("on", onNow);
  thinkPill.setAttribute("aria-pressed", String(onNow));
  const effort = S.settings.thinking.effort && c.effortLevels.includes(S.settings.thinking.effort) ? " · " + cap(S.settings.thinking.effort) : "";
  thinkPill.innerHTML = icon("lightbulb", 18) + `<span>Think${onNow ? effort : ""}</span>`;
  thinkPill.dataset.tip = onNow ? "The model thinks before answering. Click to turn off" : "Let the model think before answering";
  if (imageMode) thinkPill.hidden = true;
  renderImagePill();
  ta.placeholder = imageMode
    ? attachments.some((a) => a.type === "image") ? "Describe the changes" : "Describe or edit an image"
    : ready ? "Ask anything" : S.engine.status === "loading" ? "Loading the model…" : imagesReady() ? "Ask anything, or describe a picture" : "Choose a model to start chatting";
}

const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

function toggleThink(e) {
  const c = caps();
  if (c.effortLevels.length > 1 && thinkingEnabled()) {
    openMenu(e.currentTarget, [
      { heading: "Thinking effort" },
      ...c.effortLevels.filter((l) => l !== "none").map((l) => ({ label: cap(l), checked: S.settings.thinking.effort === l, onClick: () => updateSettings((s) => (s.thinking.effort = l)) })),
      { label: "Model default", checked: !S.settings.thinking.effort, onClick: () => updateSettings((s) => (s.thinking.effort = "")) },
      { divider: true },
      { label: "Turn off thinking", icon: "x", onClick: () => updateSettings((s) => (s.thinking.enabled = false)) },
    ], { side: "top" });
    return;
  }
  updateSettings((s) => (s.thinking.enabled = !s.thinking.enabled));
}

function openPlusMenu(e) {
  const c = caps();
  const can = [c.vision && "pictures", c.audio && "audio", c.vision && "video"].filter(Boolean);
  const im = defaultImageModel();
  openMenu(e.currentTarget, [
    { label: "Add photos & files", icon: "paperclip", hint: can.length ? `Documents, PDFs, ${can.join(", ")}` : "Documents, PDFs, code, pictures, audio, video", onClick: () => fileInput.click() },
    { label: "Create image", icon: "image", checked: !!imageMode, hint: im ? im.name : "Add an image model first", onClick: () => (imagesReady() || imageMode ? setImageMode(!imageMode) : emit("need-image-model")) },
    c.reasoning ? { label: "Think", icon: "lightbulb", checked: thinkingEnabled(), hint: "Reason step by step before answering", onClick: () => updateSettings((s) => (s.thinking.enabled = !s.thinking.enabled)) } : null,
  ], { side: "top" });
}

function onKey(e) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    const sendKey = S.settings.sendOnEnter || e.ctrlKey || e.metaKey;
    if (sendKey) {
      e.preventDefault();
      if (!S.generating) submit();
    }
  } else if (e.key === "Escape" && S.generating) {
    e.preventDefault();
    stop();
  } else if (e.key === "ArrowUp" && !ta.value) {
    emit("edit-last");
  }
}

function onSendClick() {
  if (S.generating) stop();
  else submit();
}

function submit() {
  if (!canSend()) return;
  const text = ta.value.trim();
  const atts = attachments.map(({ loading, ...a }) => a);
  if (send({ text, attachments: atts, image: imageMode ? { size: imageMode.size } : null })) {
    ta.value = "";
    attachments = [];
    renderChips();
    autosize();
    update();
  }
}

// ---------- attachments ----------

function renderChips() {
  chips.hidden = attachments.length === 0;
  chips.replaceChildren(
    ...attachments.map((a, i) => {
      const remove = h("button", { type: "button", class: "x", "aria-label": `Remove ${a.name}`, html: icon("x", 14), onclick: () => (attachments.splice(i, 1), renderChips(), update()) });
      if (a.type === "image") return h("div", { class: "chip image" }, h("img", { src: a.dataUrl, alt: a.name }), remove);
      if (a.type === "video" && a.frames?.length) return h("div", { class: "chip image video" }, h("img", { src: mediaUrl(a.frames[0].media), alt: a.name }), h("span", { class: "chip-badge", html: icon("film", 14) }), remove);
      const [kind, ico, cls] = a.loading ? ["Reading…", "loader-circle", "text"] : fileKind(a);
      return h(
        "div",
        { class: "chip" },
        h("div", { class: "att-ico " + cls + (a.loading ? " spin" : ""), html: icon(ico, 18) }),
        h("div", { style: "min-width:0" }, h("div", { class: "chip-name", text: a.name }), h("div", { class: "chip-kind", text: a.loading ? kind : a.type === "text" ? `${kind} · ${formatBytes(a.size || a.text?.length || 0)}` : kind })),
        remove,
      );
    }),
  );
}

async function addFiles(files) {
  const maxImageDim = (S.hw?.physical || 4) <= 4 ? 896 : 1280;
  const notes = new Set();
  await Promise.all(
    files.map(async (f) => {
      const kind = kindOf(f);
      const placeholder = { type: "loading", name: f.name, loading: true, size: f.size };
      if (kind && kind !== "image" && kind !== "text") {
        attachments.push(placeholder);
        renderChips();
        update();
      }
      try {
        const a = await readAttachment(f, { maxImageDim });
        const i = attachments.indexOf(placeholder);
        if (i >= 0) attachments.splice(i, 1, a);
        else attachments.push(a);
        const c = caps();
        const ready = S.engine.status === "ready";
        if (a.type === "image" && ready && !c.vision && !imageMode) notes.add("This model can't see pictures. It will be told one was attached; load a vision model to have it look at them.");
        if (a.type === "audio" && ready && !c.audio) notes.add("This model can't listen to audio. Load a model that understands audio (for example Qwen2.5-Omni, Voxtral or Ultravox with its mmproj file) to use recordings.");
        if (a.type === "video" && ready && !c.vision) notes.add("This model can't see video. Load a vision model to have it watch the frames.");
        if (a.cut) notes.add(`${a.name} is long; only the first part was added.`);
      } catch (e) {
        const i = attachments.indexOf(placeholder);
        if (i >= 0) attachments.splice(i, 1);
        toast(isAttachError(e) ? e.message : `Couldn't read ${f.name}.`);
      }
      renderChips();
      update();
    }),
  );
  notes.forEach((n) => toast(n, { timeout: 6000 }));
  focusComposer();
}

function onPaste(e) {
  const items = [...(e.clipboardData?.items || [])];
  const files = items.filter((i) => i.kind === "file").map((i) => i.getAsFile()).filter(Boolean);
  if (files.length) {
    e.preventDefault();
    addFiles(files);
    return;
  }
  const text = e.clipboardData?.getData("text/plain") || "";
  if (text.length > PASTE_TO_FILE) {
    e.preventDefault();
    attachments.push({ type: "text", name: "Pasted text.txt", text, size: text.length, code: false });
    renderChips();
    update();
    toast("Long text was added as an attachment.");
  }
}

function installDragDrop() {
  let depth = 0;
  let overlay = null;
  const show = (v) => {
    if (v && !overlay) {
      overlay = h("div", { class: "drop-overlay" }, h("div", { class: "drop-card" }, h("div", { html: icon("files", 40) }), h("h3", { text: "Add anything" }), h("p", { text: "Drop files here to add them to the conversation" })));
      document.body.append(overlay);
    } else if (!v && overlay) {
      overlay.remove();
      overlay = null;
    }
  };
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes("Files");
  // GGUF and safetensors files are models, not attachments.
  document.addEventListener("dragenter", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth++;
    show(true);
  });
  document.addEventListener("dragover", (e) => hasFiles(e) && e.preventDefault());
  document.addEventListener("dragleave", (e) => {
    if (!hasFiles(e)) return;
    depth = Math.max(0, depth - 1);
    if (!depth) show(false);
  });
  document.addEventListener("drop", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = 0;
    show(false);
    const files = [...e.dataTransfer.files];
    if (files.some((f) => /\.(gguf|safetensors|sft|ckpt)$/i.test(f.name))) {
      toast("To add a model, use “Add model files” in the model menu.");
      return;
    }
    addFiles(files);
  });
}

export function composerShortcutsHint() {
  return `${mod}+Shift+O`;
}
