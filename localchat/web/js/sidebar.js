// Sidebar: new chat, search, history (pinned + grouped by date) and the
// profile menu. Also the Ctrl+K search dialog.
import { S, on, updateSettings, actions } from "./state.js";
import { api } from "./api.js";
import { h, $, dateGroup, debounce, mod, formatBytes } from "./util.js";
import { icon } from "./icons.js";
import { openMenu, openModal, confirmDialog, toast } from "./ui.js";

const LOGO = `<svg class="logo" viewBox="0 0 32 32" aria-hidden="true"><path d="M16 4.5C9.6 4.5 4.5 9.3 4.5 15.3c0 3.1 1.4 5.9 3.6 7.9l-.8 4.4 4.6-2.2c1.3.4 2.6.6 4.1.6 6.4 0 11.5-4.8 11.5-10.8S22.4 4.5 16 4.5z" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linejoin="round"/><path d="M16 9.6l1.5 4.1 4.1 1.6-4.1 1.6-1.5 4.1-1.5-4.1-4.1-1.6 4.1-1.6z" fill="currentColor"/></svg>`;
export { LOGO };

let root, historyEl, renaming = null;

export function initSidebar() {
  root = $("#sidebar");
  render();
  on("chats", renderHistory);
  on("chat-opened", renderHistory);
  on("generating", renderHistory);
  on("hw", renderFoot);
  const mq = matchMedia("(max-width: 767px)");
  const sync = () => {
    root.classList.toggle("collapsed", !mq.matches && S.settings.sidebarCollapsed);
    if (!mq.matches) closeMobile();
    updateToggleLabels();
  };
  mq.addEventListener("change", sync);
  on("settings", sync);
  sync();
  $("#scrim").addEventListener("click", closeMobile);
}

export function isMobile() {
  return matchMedia("(max-width: 767px)").matches;
}

export function toggleSidebar() {
  if (isMobile()) {
    root.classList.contains("open") ? closeMobile() : openMobile();
  } else {
    updateSettings((s) => (s.sidebarCollapsed = !s.sidebarCollapsed));
  }
}

function openMobile() {
  root.classList.add("open");
  $("#scrim").hidden = false;
}

export function closeMobile() {
  root.classList.remove("open");
  $("#scrim").hidden = true;
}

function updateToggleLabels() {
  const collapsed = root.classList.contains("collapsed");
  const b = root.querySelector(".brand-btn");
  if (b) {
    b.setAttribute("aria-label", collapsed ? "Open sidebar" : "LocalChat");
    b.dataset.tip = collapsed ? "Open sidebar" : "";
  }
}

function render() {
  const brand = h("button", {
    type: "button",
    class: "brand-btn",
    "aria-label": "LocalChat",
    "data-tip-side": "right",
    html: LOGO + icon("panel-left", 20, "toggle-icon"),
    onclick: () => (root.classList.contains("collapsed") ? toggleSidebar() : actions.newChat()),
  });
  const close = h("button", { type: "button", class: "icon-btn sidebar-close", "aria-label": "Close sidebar", "data-tip": "Close sidebar", "data-tip-kbd": `${mod}+Shift+S`, html: icon("panel-left", 20), onclick: toggleSidebar });
  historyEl = h("nav", { class: "history", "aria-label": "Chat history" });
  root.replaceChildren(
    h("div", { class: "sidebar-head" }, brand, close),
    h(
      "div",
      { class: "nav" },
      navItem("square-pen", "New chat", () => actions.newChat(), `${mod}+Shift+O`),
      navItem("search", "Search chats", () => openSearch(), `${mod}+K`),
      navItem("box", "Models", (e) => actions.openModelMenu(e.currentTarget, "right")),
    ),
    historyEl,
    h("div", { class: "sidebar-foot", id: "sidebar-foot" }),
  );
  renderHistory();
  renderFoot();
}

function navItem(ic, label, onclick, kbd) {
  return h(
    "button",
    { type: "button", class: "nav-item", onclick: (e) => (onclick(e), isMobile() && label !== "Models" && closeMobile()), "aria-label": label, "data-tip-side": "right" },
    h("span", { html: icon(ic, 20) }),
    h("span", { class: "nav-label", text: label }),
    kbd ? h("span", { class: "kbd-hint", text: kbd }) : null,
  );
}

function renderFoot() {
  const foot = $("#sidebar-foot");
  if (!foot) return;
  const hw = S.hw;
  const sub = hw ? `${formatBytes(hw.totalRam)} RAM · ${hw.physical} cores` : "Runs on this PC";
  const name = S.settings.custom?.name?.trim();
  foot.replaceChildren(
    h(
      "button",
      {
        type: "button",
        class: "profile-btn",
        "aria-label": "Settings and more",
        onclick: (e) =>
          openMenu(e.currentTarget, [
            { heading: hw?.cpuName || "This PC" },
            { label: "Settings", icon: "settings", kbd: `${mod}+,`, onClick: () => actions.openSettings() },
            { label: "Model & performance", icon: "gauge", onClick: () => actions.openSettings("model") },
            { label: "Keyboard shortcuts", icon: "keyboard", kbd: `${mod}+/`, onClick: () => actions.openShortcuts() },
            { divider: true },
            { label: "Open logs folder", icon: "folder-open", onClick: () => api.reveal("logs").catch((err) => toast(err.message)) },
            { label: "About LocalChat", icon: "info", onClick: () => actions.openSettings("about") },
          ], { side: "top", width: 260 }),
      },
      h("span", { class: "avatar", html: name ? "" : icon("monitor", 16), text: name ? name.charAt(0).toUpperCase() : null }),
      h("span", { class: "profile-text" }, h("div", { class: "profile-name", text: name || "This PC" }), h("div", { class: "profile-sub", text: sub })),
    ),
  );
}

export function renderHistory() {
  if (!historyEl) return;
  const list = S.chats.filter((c) => !c.archived);
  if (!list.length) {
    historyEl.replaceChildren(h("div", { class: "history-empty", text: "Your chats will appear here." }));
    return;
  }
  const pinned = list.filter((c) => c.pinned);
  const rest = list.filter((c) => !c.pinned);
  const groups = [];
  if (pinned.length) groups.push(["Pinned", pinned]);
  const byLabel = new Map();
  for (const c of rest) {
    const g = dateGroup(c.updated);
    if (!byLabel.has(g)) byLabel.set(g, []);
    byLabel.get(g).push(c);
  }
  groups.push(...byLabel);
  historyEl.replaceChildren(
    ...groups.map(([label, items]) =>
      h("div", { class: "history-group" }, h("div", { class: "history-label", text: label }), items.map(chatItem)),
    ),
  );
}

function chatItem(c) {
  const active = S.chat?.id === c.id;
  const live = S.generating?.chatId === c.id;
  if (renaming === c.id) {
    const input = h("input", { class: "chat-rename", value: c.title, "aria-label": "Chat name" });
    const finish = (save) => {
      if (renaming !== c.id) return;
      renaming = null;
      const v = input.value.trim();
      if (save && v && v !== c.title) actions.renameChat(c.id, v);
      else renderHistory();
    };
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") finish(true);
      if (e.key === "Escape") finish(false);
    });
    input.addEventListener("blur", () => finish(true));
    requestAnimationFrame(() => (input.focus(), input.select()));
    return h("div", { class: "chat-item active" }, input);
  }
  const item = h(
    "div",
    { class: "chat-item" + (active ? " active" : "") },
    h(
      "button",
      { type: "button", class: "chat-link", "aria-current": active ? "page" : null, onclick: () => (actions.openChat(c.id), isMobile() && closeMobile()), ondblclick: () => startRename(c.id) },
      h("span", { class: "chat-title", text: c.title || "New chat" }),
      live ? h("span", { class: "live-dot", "aria-label": "Replying" }) : null,
    ),
    h("button", {
      type: "button",
      class: "icon-btn sm chat-more",
      "aria-label": `Options for ${c.title || "chat"}`,
      "aria-haspopup": "menu",
      html: icon("ellipsis", 18),
      onclick: (e) => {
        e.stopPropagation();
        item.classList.add("menu-open");
        openMenu(e.currentTarget, chatMenuItems(c), { onClose: () => item.classList.remove("menu-open") });
      },
    }),
  );
  return item;
}

export function chatMenuItems(c) {
  return [
    { label: c.pinned ? "Unpin" : "Pin", icon: c.pinned ? "pin-off" : "pin", onClick: () => actions.setChatFlag(c.id, "pinned", !c.pinned) },
    { label: "Rename", icon: "pencil", onClick: () => startRename(c.id) },
    { label: "Export as Markdown", icon: "download", onClick: () => actions.exportChat(c.id) },
    { label: c.archived ? "Unarchive" : "Archive", icon: c.archived ? "archive-restore" : "archive", onClick: () => actions.setChatFlag(c.id, "archived", !c.archived) },
    { divider: true },
    { label: "Delete", icon: "trash-2", danger: true, onClick: () => confirmDelete(c) },
  ];
}

export function startRename(id) {
  if (root.classList.contains("collapsed") && !isMobile()) {
    actions.promptRename(id);
    return;
  }
  renaming = id;
  renderHistory();
}

export async function confirmDelete(c) {
  const ok = await confirmDialog({ title: "Delete chat?", message: `This will delete “${c.title || "New chat"}”.`, confirmLabel: "Delete", danger: true });
  if (ok) actions.deleteChat(c.id);
}

// ---------- search ----------

export function openSearch() {
  let sel = 0;
  let items = [];
  const input = h("input", { type: "text", placeholder: "Search chats…", "aria-label": "Search chats", autofocus: true, autocomplete: "off" });
  const results = h("div", { class: "search-results", role: "listbox", "aria-label": "Results" });
  const m = openModal({
    label: "Search chats",
    className: "search-modal",
    content: (body, close) => {
      body.append(
        h("div", { class: "search-head" }, h("span", { html: icon("search", 20), style: "color:var(--text-3)" }), input, h("button", { type: "button", class: "icon-btn", "aria-label": "Close", html: icon("x", 20), onclick: close })),
        results,
      );
    },
  });
  const choose = (it) => {
    m.close();
    it.action();
  };
  const paint = (list, grouped) => {
    items = [{ title: "New chat", icon: "square-pen", action: () => actions.newChat() }, ...list];
    sel = Math.min(sel, items.length - 1);
    const nodes = [];
    let lastGroup = null;
    items.forEach((it, i) => {
      if (grouped && it.group && it.group !== lastGroup) {
        lastGroup = it.group;
        nodes.push(h("div", { class: "search-group", text: it.group }));
      }
      nodes.push(
        h(
          "button",
          { type: "button", class: "search-item" + (i === sel ? " sel" : ""), role: "option", "aria-selected": String(i === sel), onclick: () => choose(it), onmousemove: () => { if (sel !== i) { sel = i; highlight(); } } },
          h("span", { html: icon(it.icon || "message-square-dashed", 20), style: "color:var(--text-2)" }),
          h("span", { class: "si-body" }, h("div", { class: "si-title", text: it.title }), it.snippet ? h("div", { class: "si-snip", text: it.snippet }) : null),
        ),
      );
    });
    if (list.length === 0 && input.value.trim()) nodes.push(h("div", { class: "history-empty", text: "No chats match your search." }));
    results.replaceChildren(...nodes);
  };
  const highlight = () => results.querySelectorAll(".search-item").forEach((b, i) => {
    b.classList.toggle("sel", i === sel);
    b.setAttribute("aria-selected", String(i === sel));
  });
  const recent = () =>
    paint(
      S.chats.filter((c) => !c.archived).slice(0, 60).map((c) => ({ title: c.title || "New chat", group: dateGroup(c.updated), icon: "message-square-dashed", action: () => actions.openChat(c.id) })),
      true,
    );
  const doSearch = debounce(async () => {
    const q = input.value.trim();
    if (!q) return recent();
    try {
      const hits = await api.searchChats(q);
      sel = hits.length ? 1 : 0;
      paint((hits || []).map((c) => ({ title: c.title || "New chat", snippet: c.snippet, icon: "text-search", action: () => actions.openChat(c.id) })), false);
    } catch {}
  }, 150);
  input.addEventListener("input", doSearch);
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      sel = Math.min(items.length - 1, sel + 1);
      highlight();
      results.querySelectorAll(".search-item")[sel]?.scrollIntoView({ block: "nearest" });
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      sel = Math.max(0, sel - 1);
      highlight();
      results.querySelectorAll(".search-item")[sel]?.scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter") {
      e.preventDefault();
      items[sel] && choose(items[sel]);
    }
  });
  recent();
}
