// The message box: typing, attachments, the Think toggle, send and stop.
import { S, caps, emit, on, updateSettings } from "./state.js";
import { send, stop, thinkingEnabled } from "./chat.js";
import { h, $, formatBytes, mod } from "./util.js";
import { icon } from "./icons.js";
import { openMenu, toast } from "./ui.js";

const TEXT_EXT = /\.(txt|md|markdown|csv|tsv|json|jsonl|xml|html?|css|js|mjs|cjs|ts|tsx|jsx|py|rb|go|rs|java|kt|c|h|cpp|hpp|cc|cs|php|sh|bash|ps1|bat|sql|yaml|yml|toml|ini|cfg|conf|log|tex|r|swift|lua|pl|dart|scala|vue|svelte|srt|vtt|rst|org)$/i;
const CODE_EXT = /\.(json|xml|html?|css|js|mjs|cjs|ts|tsx|jsx|py|rb|go|rs|java|kt|c|h|cpp|hpp|cc|cs|php|sh|bash|ps1|bat|sql|yaml|yml|toml|lua|pl|dart|scala|vue|svelte|swift|r)$/i;
const MAX_TEXT = 2 * 1024 * 1024;
const PASTE_TO_FILE = 10000;

let el, ta, chips, sendBtn, thinkPill, plusBtn, fileInput;
let attachments = [];

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
  sendBtn = h("button", { type: "button", class: "send-btn", "aria-label": "Send message", "data-tip": "Send", onclick: onSendClick });
  el = h(
    "div",
    { class: "composer" },
    chips,
    h("div", { class: "composer-input-row" }, ta),
    h("div", { class: "composer-row" }, plusBtn, thinkPill, h("div", { class: "spacer" }), sendBtn),
    fileInput,
  );
  installDragDrop();
  on("generating", update);
  on("caps", update);
  on("settings", update);
  on("engine", update);
  update();
  return el;
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
  ta.placeholder = ready ? "Ask anything" : S.engine.status === "loading" ? "Loading the model…" : "Choose a model to start chatting";
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
  openMenu(e.currentTarget, [
    { label: "Add photos & files", icon: "paperclip", hint: c.vision ? "Images, text and code files" : "Text and code files (this model can't see images)", onClick: () => fileInput.click() },
    c.reasoning ? { label: "Think", icon: "lightbulb", checked: thinkingEnabled(), hint: "Reason step by step before answering", onClick: () => updateSettings((s) => (s.thinking.enabled = !s.thinking.enabled)) } : null,
  ], { side: "top" });
}

function onKey(e) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    const sendKey = S.settings.sendOnEnter ? !e.ctrlKey : e.ctrlKey || e.metaKey;
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
  if (send({ text, attachments: atts })) {
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
      return h(
        "div",
        { class: "chip" },
        h("div", { class: "att-ico", html: icon(a.code ? "file-code" : "file-text", 18), style: a.code ? "background:#0285ff" : "" }),
        h("div", { style: "min-width:0" }, h("div", { class: "chip-name", text: a.name }), h("div", { class: "chip-kind", text: a.loading ? "Reading…" : formatBytes(a.size || a.text?.length || 0) })),
        remove,
      );
    }),
  );
}

async function addFiles(files) {
  for (const f of files) {
    if (/^image\//.test(f.type)) {
      if (!caps().vision) {
        toast(S.engine.status === "ready" ? "This model can't see images. Load a vision model (one with an mmproj file next to it) to send pictures." : "Load a model first.");
        continue;
      }
      try {
        attachments.push({ type: "image", name: f.name || "Image", dataUrl: await shrinkImage(f) });
      } catch {
        toast(`Couldn't read ${f.name}.`);
      }
    } else if (/\.pdf$/i.test(f.name) || f.type === "application/pdf") {
      toast("PDF files aren't supported yet. Copy the text you need and paste it here.");
    } else if (TEXT_EXT.test(f.name) || /^text\//.test(f.type) || f.type === "application/json") {
      if (f.size > MAX_TEXT) {
        toast(`${f.name} is too large (${formatBytes(f.size)}). The limit is 2 MB.`);
        continue;
      }
      attachments.push({ type: "text", name: f.name, text: await f.text(), size: f.size, code: CODE_EXT.test(f.name) });
    } else {
      toast(`${f.name} can't be attached. Use images, text or code files.`);
    }
  }
  renderChips();
  update();
  focusComposer();
}

/** Scales images down before sending: fewer image tokens, faster on CPU. */
async function shrinkImage(file) {
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise((res, rej) => {
      const i = new Image();
      i.onload = () => res(i);
      i.onerror = rej;
      i.src = url;
    });
    const maxDim = (S.hw?.physical || 4) <= 4 ? 896 : 1280;
    const scale = Math.min(1, maxDim / Math.max(img.naturalWidth, img.naturalHeight));
    const c = document.createElement("canvas");
    c.width = Math.max(1, Math.round(img.naturalWidth * scale));
    c.height = Math.max(1, Math.round(img.naturalHeight * scale));
    const ctx = c.getContext("2d");
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, c.width, c.height);
    ctx.drawImage(img, 0, 0, c.width, c.height);
    return c.toDataURL("image/jpeg", 0.86);
  } finally {
    URL.revokeObjectURL(url);
  }
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
    if (files.some((f) => /\.gguf$/i.test(f.name))) {
      toast("To load a model, use “Browse for a model file” in the model menu.");
      return;
    }
    addFiles(files);
  });
}

export function composerShortcutsHint() {
  return `${mod}+Shift+O`;
}
