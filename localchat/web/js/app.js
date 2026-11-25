// Entry point: boots the app, wires modules together and renders the shell.
import { S, actions, emit, on, loadSettings, updateSettings, applyTheme } from "./state.js";
import { api, subscribeEvents } from "./api.js";
import { h, $, copyText, mod } from "./util.js";
import { icon } from "./icons.js";
import { toast, openMenu, openModal, closeMenu, promptDialog } from "./ui.js";
import { initSidebar, toggleSidebar, openSearch, chatMenuItems, confirmDelete, isMobile } from "./sidebar.js";
import { buildComposer, composerEl, focusComposer } from "./composer.js";
import { renderThread, activePath, newChat, saveChat, initScroll, scrollToBottom, stop, copyLastReply, resetThreadDom } from "./chat.js";
import { openModelMenu, loadModel, refreshModels, scan, preChatView, modelSwitch, statusPill } from "./modelmenu.js";
import { openSettings, openShortcuts } from "./settings.js";

const HEROES = ["What can I help with?", "What's on your mind today?", "Where should we begin?", "How can I help today?"];
let heroText = HEROES[0];

// ---------- actions ----------

Object.assign(actions, {
  newChat() {
    if (S.generating && S.chat?.id === S.generating.chatId) {
      // Let the reply keep going in the background; it saves when done.
    }
    S.chat = null;
    heroText = HEROES[Math.floor(Math.random() * HEROES.length)];
    resetThreadDom();
    renderAll();
    focusComposer();
  },
  async openChat(id) {
    if (S.chat?.id === id) return;
    try {
      const c = await api.chat(id);
      if (!c.messages?.root) c.messages = { root: { id: "root", children: [], sel: null }, ...(c.messages || {}) };
      S.chat = c;
      S.temporary = false;
      resetThreadDom();
      renderAll();
      scrollToBottom(true);
      focusComposer();
    } catch (e) {
      toast("Couldn't open the chat: " + e.message, { kind: "error" });
    }
  },
  async renameChat(id, title) {
    const meta = S.chats.find((c) => c.id === id);
    if (!meta) return;
    const c = S.chat?.id === id ? S.chat : await api.chat(id);
    c.title = title;
    meta.title = title;
    await api.saveChat(c).catch((e) => toast(e.message, { kind: "error" }));
    emit("chats");
    renderTopbar();
  },
  promptRename: async (id) => {
    const meta = S.chats.find((c) => c.id === id);
    const v = await promptDialog({ title: "Rename chat", value: meta?.title || "" });
    if (v) actions.renameChat(id, v);
  },
  async setChatFlag(id, flag, value) {
    const meta = S.chats.find((c) => c.id === id);
    if (!meta) return;
    const c = S.chat?.id === id ? S.chat : await api.chat(id);
    c[flag] = value;
    meta[flag] = value;
    await api.saveChat(c).catch((e) => toast(e.message, { kind: "error" }));
    emit("chats");
    if (flag === "archived" && value) {
      toast("Chat archived", { action: { label: "Undo", fn: () => actions.setChatFlag(id, "archived", false) } });
      if (S.chat?.id === id) actions.newChat();
    }
    renderTopbar();
  },
  async deleteChat(id) {
    if (S.generating?.chatId === id) stop();
    await api.deleteChat(id).catch((e) => toast(e.message, { kind: "error" }));
    S.chats = S.chats.filter((c) => c.id !== id);
    emit("chats");
    if (S.chat?.id === id) actions.newChat();
    toast("Chat deleted");
  },
  async deleteAllChats() {
    if (S.generating) stop();
    await api.deleteAllChats();
    S.chats = [];
    emit("chats");
    actions.newChat();
    toast("All chats deleted");
  },
  async exportChat(id) {
    try {
      const c = S.chat?.id === id ? S.chat : await api.chat(id);
      const md = chatToMarkdown(c);
      const name = (c.title || "Chat").replace(/[\\/:*?"<>|]+/g, " ").trim().slice(0, 80) || "Chat";
      const r = await api.saveFile(name + ".md", "md", md);
      if (!r.cancelled) toast("Exported");
    } catch (e) {
      toast(e.message, { kind: "error" });
    }
  },
  async reloadChats() {
    S.chats = await api.chats().catch(() => S.chats);
    emit("chats");
  },
  openModelMenu: (anchor, side) => openModelMenu(anchor || $(".model-switch"), side),
  openSettings: (tab) => (closeMenu(), openSettings(tab)),
  openShortcuts: () => openShortcuts(),
  loadModel: (path) => loadModel(path),
  stop: () => stop(),
  updateSettingsSilently: (fn) => updateSettings(fn),
  setEngineState: (st) => onEngine(st, true),
});

function chatToMarkdown(c) {
  const lines = [`# ${c.title || "Chat"}`, ""];
  const path = activePath(c);
  for (const m of path) {
    lines.push(m.role === "user" ? "## You" : `## ${m.model || "Assistant"}`, "");
    for (const a of m.attachments || []) lines.push(a.type === "image" ? `*[image: ${a.name}]*` : `*[file: ${a.name}]*`, "");
    if (m.content) lines.push(m.content, "");
  }
  return lines.join("\n");
}

// ---------- rendering ----------

function renderTopbar() {
  const bar = $("#topbar");
  const left = [];
  if (isMobile()) left.push(h("button", { type: "button", class: "icon-btn", "aria-label": "Open sidebar", html: icon("panel-left", 20), onclick: toggleSidebar }));
  left.push(modelSwitch());
  const pill = statusPill();
  if (pill) left.push(pill);
  const right = [];
  const hasMessages = activePath().length > 0;
  if (isMobile()) right.push(h("button", { type: "button", class: "icon-btn", "aria-label": "New chat", html: icon("square-pen", 20), onclick: () => actions.newChat() }));
  if (!hasMessages) {
    right.push(
      h("button", {
        type: "button",
        class: "icon-btn" + (S.temporary ? " temp-on" : ""),
        "aria-label": S.temporary ? "Turn off temporary chat" : "Turn on temporary chat",
        "aria-pressed": String(S.temporary),
        "data-tip": S.temporary ? "Turn off temporary chat" : "Turn on temporary chat",
        html: icon("message-square-dashed", 20),
        onclick: () => {
          S.temporary = !S.temporary;
          renderAll();
        },
      }),
    );
  } else if (S.chat && !S.chat.temporary) {
    const meta = S.chats.find((c) => c.id === S.chat.id) || S.chat;
    right.push(
      h("button", { type: "button", class: "pill-btn", "aria-label": "Export chat", "data-tip": "Export as Markdown", onclick: () => actions.exportChat(S.chat.id) }, h("span", { html: icon("upload", 18) }), h("span", { text: "Export" })),
      h("button", { type: "button", class: "icon-btn", "aria-label": "Chat options", "aria-haspopup": "menu", html: icon("ellipsis", 20), onclick: (e) => openMenu(e.currentTarget, chatMenuItems(meta), { align: "end" }) }),
    );
  } else if (S.chat?.temporary) {
    right.push(h("span", { class: "status-pill", text: "Temporary chat" }));
  }
  bar.replaceChildren(...left, h("div", { class: "spacer" }), ...right);
}

let heroEl = null;
function renderMain() {
  const main = $("#main");
  const content = $("#content");
  const dock = $("#dock");
  const empty = activePath().length === 0;
  main.classList.toggle("is-empty", empty);
  const comp = composerEl();
  const footnote = h("div", { class: "footnote", text: "Runs entirely on this PC. AI can make mistakes, so check important info." });
  if (empty) {
    const pre = preChatView();
    const title = S.temporary
      ? h("div", {}, h("h1", { class: "hero-title", text: "Temporary chat", style: "margin-bottom:8px" }), h("p", { style: "color:var(--text-2);text-align:center;margin:0 0 24px", text: "This chat won't appear in your history or be saved." }))
      : h("h1", { class: "hero-title", text: heroText });
    heroEl = h("div", { class: "hero" }, pre || title, pre && S.engine.status !== "idle" ? h("div", { style: "height:24px" }) : null, S.engine.status === "idle" && !S.model ? null : h("div", { class: "dock-inner" }, comp));
    content.replaceChildren(heroEl, $("#thread") || h("div", { class: "thread", id: "thread" }));
    dock.replaceChildren();
  } else {
    if (heroEl) {
      heroEl.remove();
      heroEl = null;
    }
    if (!$("#thread")) content.append(h("div", { class: "thread", id: "thread", role: "log", "aria-label": "Conversation" }));
    renderThread();
    dock.replaceChildren(h("div", { class: "dock-inner" }, scrollBtn(), comp, footnote));
  }
  document.title = S.chat?.title ? `${S.chat.title} – LocalChat` : "LocalChat";
}

let scrollButton;
function scrollBtn() {
  scrollButton ||= h("button", { type: "button", class: "scroll-down", hidden: true, "aria-label": "Scroll to the latest message", html: icon("arrow-down", 18), onclick: () => scrollToBottom(true) });
  return scrollButton;
}

function renderAll() {
  renderTopbar();
  renderMain();
  emit("chat-opened");
}

// ---------- engine events ----------

let lastStatus = "";
let lastSeq = -1;
async function onEngine(st, local = false) {
  // Ignore stale server events that arrive after a newer one.
  if (!local && typeof st.seq === "number") {
    if (st.seq <= lastSeq && st.status === S.engine.status) return;
    lastSeq = st.seq;
  }
  const prev = lastStatus;
  S.engine = st;
  lastStatus = st.status;
  emit("engine", st);
  if (st.status === "ready" && prev !== "ready") {
    try {
      S.props = await api.props();
    } catch {
      S.props = null;
    }
    try {
      const s = await api.state();
      if (s.model) S.model = s.model;
    } catch {}
    emit("caps");
    refreshModels();
  }
  if (st.status === "error" && prev !== "error" && activePath().length) {
    toast(st.error || "The model stopped.", { kind: "error", timeout: 8000, action: S.model?.entry ? { label: "Reload", fn: () => loadModel(S.model.entry.path) } : null });
  }
  if (prev !== st.status || st.status === "loading") {
    renderTopbar();
    if (activePath().length === 0) renderMain();
  }
}

// ---------- global handlers ----------

function installGlobalHandlers() {
  document.addEventListener("click", (e) => {
    const a = e.target.closest("a[href]");
    if (a && (a.target === "_blank" || a.dataset.external || /^https?:/.test(a.getAttribute("href")))) {
      e.preventDefault();
      api.openURL(a.href).catch((err) => toast(err.message));
      return;
    }
    const copy = e.target.closest(".code-copy");
    if (copy) {
      const code = copy.closest(".code-block")?.querySelector("code")?.textContent || "";
      copyText(code).then(() => {
        const label = copy.querySelector("span");
        copy.innerHTML = icon("check", 16) + "<span>Copied</span>";
        setTimeout(() => (copy.innerHTML = icon("copy", 16) + "<span>Copy</span>"), 1600);
        void label;
      });
    }
  });

  document.addEventListener("keydown", (e) => {
    const k = e.key.toLowerCase();
    const ctrl = e.ctrlKey || e.metaKey;
    if (ctrl && e.shiftKey && k === "o") {
      e.preventDefault();
      actions.newChat();
    } else if (ctrl && !e.shiftKey && k === "k") {
      e.preventDefault();
      closeMenu();
      openSearch();
    } else if (ctrl && e.shiftKey && k === "s") {
      e.preventDefault();
      toggleSidebar();
    } else if (ctrl && e.shiftKey && k === "c") {
      e.preventDefault();
      copyLastReply();
    } else if (ctrl && (k === "," )) {
      e.preventDefault();
      openSettings();
    } else if (ctrl && (k === "/" || k === "?")) {
      e.preventDefault();
      openShortcuts();
    } else if (ctrl && e.shiftKey && (k === "backspace" || k === "delete")) {
      if (S.chat && !S.chat.temporary) {
        e.preventDefault();
        confirmDelete(S.chats.find((c) => c.id === S.chat.id) || S.chat);
      }
    } else if (e.shiftKey && k === "escape") {
      e.preventDefault();
      focusComposer();
    } else if (k === "escape" && S.generating && !document.querySelector(".overlay, .menu")) {
      stop();
    } else if (!ctrl && !e.altKey && k.length === 1 && !e.target.closest("input, textarea, select, [contenteditable]") && !document.querySelector(".overlay")) {
      focusComposer(); // type anywhere to start writing
    }
  });

  on("lightbox", (src) => openModal({ label: "Image", className: "modal-image", content: h("img", { class: "lightbox", src, alt: "" }) }));
  on("need-model", () => {
    toast(S.engine.status === "error" ? "The model didn't load. Choose a model to continue." : "Choose a model to start chatting.");
    openModelMenu($(".model-switch"));
  });
  on("edit-last", () => {
    const btns = document.querySelectorAll('.turn.user button[aria-label="Edit message"]');
    btns[btns.length - 1]?.click();
  });
  on("scroll", (atBottom) => {
    if (scrollButton) scrollButton.hidden = atBottom;
  });
  on("thread-changed", () => {
    if ($("#main").classList.contains("is-empty")) renderAll();
  });
  on("chat-opened", () => renderTopbar());
  on("chats", () => {
    if (S.chat) document.title = (S.chats.find((c) => c.id === S.chat.id)?.title || "LocalChat") + " – LocalChat";
  });
  on("model", () => {
    renderTopbar();
    if (activePath().length === 0) renderMain();
  });
  on("scanning", (v) => {
    S.scanning = v;
    if (activePath().length === 0 && S.engine.status === "idle") renderMain();
  });
  on("generating", () => {
    if (!S.generating) renderTopbar();
  });
  on("settings", () => renderTopbar());
  matchMedia("(max-width: 767px)").addEventListener("change", () => renderTopbar());
}

// ---------- boot ----------

async function boot() {
  await loadSettings();
  installGlobalHandlers();
  initSidebar();
  buildComposer();
  initScroll();
  try {
    const st = await api.state();
    S.hw = st.hw;
    S.version = st.version;
    S.engineInfo = st.engineInfo;
    S.engine = st.engine || { status: "idle" };
    lastStatus = S.engine.status;
    S.model = st.model || null;
    emit("hw");
  } catch (e) {
    toast("Couldn't reach LocalChat's engine manager: " + e.message, { kind: "error" });
  }
  S.chats = (await api.chats().catch(() => [])) || [];
  emit("chats");
  await refreshModels();
  renderAll();
  subscribeEvents((ev) => {
    if (ev.type === "engine") onEngine(ev.data);
    else if (ev.type === "model") {
      S.model = ev.data;
      emit("model");
      emit("caps");
    }
  });
  if (S.engine.status === "ready") {
    S.props = await api.props().catch(() => null);
    emit("caps");
  }
  // Start-up behavior: reload the last model, or look for models on first run.
  if (S.engine.status === "idle") {
    const last = S.models.find((m) => m.path === S.settings.lastModel && !m.missing);
    if (S.settings.autoLoad && last) loadModel(last.path);
    else if (!S.models.length && !S.settings.seenWelcome) {
      updateSettings((s) => (s.seenWelcome = true));
      scan(false).then(() => renderMain());
    }
  }
  focusComposer();
  applyTheme();
}

boot();
