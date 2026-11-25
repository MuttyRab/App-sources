// Conversation: the message tree, rendering, sending and streaming replies.
import { S, caps, emit, systemPrompt, updateSettings } from "./state.js";
import { api, streamChat } from "./api.js";
import { h, $, uid, copyText, plainText, estimateTokens, formatDuration, clamp } from "./util.js";
import { renderInto, resetRender } from "./markdown.js";
import { icon } from "./icons.js";
import { toast, openMenu } from "./ui.js";

// ---------- tree ----------

export function newChat() {
  const now = Date.now();
  return {
    id: uid("c"),
    title: "",
    created: now,
    updated: now,
    messages: { root: { id: "root", children: [], sel: null } },
    model: S.model?.entry?.path || "",
    modelName: S.model?.entry?.name || "",
  };
}

/** The visible conversation: root's selected descendants, in order. */
export function activePath(chat = S.chat) {
  const out = [];
  if (!chat) return out;
  let node = chat.messages.root;
  while (node) {
    const next = node.sel || node.children[node.children.length - 1];
    node = next ? chat.messages[next] : null;
    if (node) out.push(node);
  }
  return out;
}

function addMessage(chat, parentId, msg) {
  msg.id ||= uid("m");
  msg.parent = parentId;
  msg.children = [];
  msg.created ||= Date.now();
  chat.messages[msg.id] = msg;
  const p = chat.messages[parentId];
  p.children.push(msg.id);
  p.sel = msg.id;
  return msg;
}

function siblings(chat, msg) {
  return chat.messages[msg.parent]?.children || [msg.id];
}

// ---------- saving ----------

let saveTimer = null;
export function saveChat(chat = S.chat, immediate = false) {
  if (!chat || chat.temporary) return;
  chat.updated = Date.now();
  const meta = { id: chat.id, title: chat.title || "New chat", created: chat.created, updated: chat.updated, pinned: chat.pinned, archived: chat.archived, modelName: chat.modelName };
  const i = S.chats.findIndex((c) => c.id === chat.id);
  if (i >= 0) S.chats[i] = { ...S.chats[i], ...meta };
  else S.chats.unshift(meta);
  S.chats.sort((a, b) => b.updated - a.updated);
  emit("chats");
  clearTimeout(saveTimer);
  const doSave = () => api.saveChat(serialize(chat)).catch((e) => toast("Couldn't save the chat: " + e.message, { kind: "error" }));
  if (immediate) doSave();
  else saveTimer = setTimeout(doSave, 400);
}

function serialize(chat) {
  const { temporary, ...rest } = chat;
  return { ...rest, title: chat.title || "New chat" };
}

// ---------- rendering ----------

const turnEls = new Map(); // message id -> element

function thread() {
  return $("#thread");
}

export function renderThread() {
  const el = thread();
  turnEls.clear();
  const path = activePath();
  const frag = document.createDocumentFragment();
  const trimmedAt = S.chat?.trimmedBefore;
  path.forEach((m, i) => {
    if (trimmedAt && m.id === trimmedAt) frag.append(h("div", { class: "trim-divider", text: "Earlier messages are outside the model's memory" }));
    frag.append(renderTurn(m, i === path.length - 1));
  });
  el.replaceChildren(frag);
}

function renderTurn(m, isLast) {
  const el = m.role === "user" ? renderUser(m) : renderAssistant(m, isLast);
  turnEls.set(m.id, el);
  return el;
}

function attachmentsView(atts = []) {
  if (!atts.length) return null;
  return h(
    "div",
    { class: "attachments" },
    atts.map((a) =>
      a.type === "image"
        ? h("img", { class: "att-thumb", src: a.dataUrl, alt: a.name || "Image", onclick: () => emit("lightbox", a.dataUrl) })
        : h(
            "div",
            { class: "att-file" },
            h("div", { class: "att-ico " + (a.code ? "code" : "text"), html: icon(a.code ? "file-code" : "file-text", 20) }),
            h("div", { class: "att-meta" }, h("div", { class: "att-name", text: a.name }), h("div", { class: "att-kind", text: a.code ? "Code" : "Document" })),
          ),
    ),
  );
}

function versionNav(m) {
  const sibs = siblings(S.chat, m);
  if (sibs.length < 2) return null;
  const i = sibs.indexOf(m.id);
  const go = (d) => {
    if (S.generating) return toast("Wait for the reply to finish, or stop it first.");
    S.chat.messages[m.parent].sel = sibs[i + d];
    saveChat();
    renderThread();
  };
  return h(
    "div",
    { class: "versions" },
    h("button", { type: "button", class: "icon-btn", "aria-label": "Previous version", "data-tip": "Previous", disabled: i === 0, html: icon("chevron-left", 16), onclick: () => go(-1) }),
    h("span", { text: `${i + 1}/${sibs.length}` }),
    h("button", { type: "button", class: "icon-btn", "aria-label": "Next version", "data-tip": "Next", disabled: i === sibs.length - 1, html: icon("chevron-right", 16), onclick: () => go(1) }),
  );
}

function actionBtn(name, label, onclick, extra = {}) {
  return h("button", { type: "button", class: "icon-btn", "aria-label": label, "data-tip": label, html: icon(name, 18), onclick, ...extra });
}

function renderUser(m) {
  const el = h("div", { class: "turn user", "data-id": m.id });
  el.append(
    ...[attachmentsView(m.attachments),
    m.content ? h("div", { class: "bubble", text: m.content }) : null,
    h(
      "div",
      { class: "msg-actions" },
      actionBtn("copy", "Copy", (e) => copyWithFeedback(e.currentTarget, m.content)),
      actionBtn("pencil", "Edit message", () => startEdit(m)),
      versionNav(m),
    )].filter(Boolean),
  );
  return el;
}

function startEdit(m) {
  if (S.generating) return toast("Wait for the reply to finish, or stop it first.");
  const el = turnEls.get(m.id);
  if (!el) return;
  const ta = h("textarea", { value: m.content, "aria-label": "Edit message", rows: 1 });
  const grow = () => {
    ta.style.height = "auto";
    ta.style.height = ta.scrollHeight + "px";
  };
  ta.addEventListener("input", grow);
  const submit = () => {
    const text = ta.value.trim();
    if (!text && !m.attachments?.length) return;
    editAndResend(m, text);
  };
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && S.settings.sendOnEnter && !e.isComposing) {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape") renderThread();
  });
  el.replaceChildren(
    h(
      "div",
      { class: "edit-box" },
      ta,
      h(
        "div",
        { class: "edit-actions" },
        h("button", { type: "button", class: "btn btn-secondary sm", text: "Cancel", onclick: () => renderThread() }),
        h("button", { type: "button", class: "btn btn-primary sm", text: "Send", onclick: submit }),
      ),
    ),
  );
  requestAnimationFrame(() => {
    grow();
    ta.focus();
    ta.setSelectionRange(ta.value.length, ta.value.length);
  });
}

function thinkingView(m, el) {
  const streaming = m.status === "streaming" && !m.content;
  const has = m.reasoning?.trim() || (m.status === "streaming" && m.thinkStart && !m.content);
  if (!has) return null;
  const open = el?._thinkOpen ?? (streaming && S.settings.showThinking);
  const label = streaming ? "Thinking" : m.thinkMs ? `Thought for ${formatDuration(m.thinkMs)}` : "Thoughts";
  const body = h("div", { class: "thinking-body" });
  const md = h("div", { class: "markdown" });
  body.append(md);
  const wrap = h(
    "div",
    { class: "thinking" + (open ? " open" : "") },
    h(
      "div",
      {},
      h(
        "button",
        {
          type: "button",
          class: "thinking-head",
          "aria-expanded": String(!!open),
          onclick: (e) => {
            const w = e.currentTarget.closest(".thinking");
            const o = !w.classList.contains("open");
            w.classList.toggle("open", o);
            e.currentTarget.setAttribute("aria-expanded", String(o));
            if (el) el._thinkOpen = o;
            if (o) renderInto(md, m.reasoning || "");
          },
        },
        h("span", { class: streaming ? "shimmer" : "", text: label }),
        h("span", { class: "chev", html: icon("chevron-right", 16) }),
      ),
      streaming && S.generating?.completionId
        ? h("button", { type: "button", class: "answer-now", text: "Answer now", "data-tip": "Stop thinking and answer", onclick: answerNow })
        : null,
    ),
    body,
  );
  if (open) renderInto(md, m.reasoning || "", streaming);
  wrap._md = md;
  return wrap;
}

function renderAssistant(m, isLast) {
  const el = h("div", { class: "turn assistant" + (isLast ? " latest" : ""), "data-id": m.id });
  fillAssistant(el, m, isLast);
  return el;
}

function fillAssistant(el, m, isLast) {
  const body = h("div", { class: "assistant-body" });
  const think = thinkingView(m, el);
  if (think) body.append(think);
  if (m.status === "streaming" && m.progress && !m.content && !m.reasoning) {
    const pct = Math.round(m.progress * 100);
    body.append(h("div", { class: "progress-note" }, h("span", { class: "shimmer", text: "Reading" }), h("div", { class: "bar" }, h("i", { style: `width:${pct}%` })), h("span", { text: pct + "%" })));
  } else if (m.status === "streaming" && !m.content && !m.reasoning) {
    body.append(h("div", { class: "dots", "aria-label": "Waiting for the reply" }, h("span", { class: "dot" })));
  }
  const md = h("div", { class: "markdown" });
  if (m.content) renderInto(md, m.content, m.status === "streaming");
  body.append(md);
  if (m.error) {
    body.append(
      h(
        "div",
        { class: "notice error", role: "alert" },
        h("span", { html: icon("circle-alert", 18) }),
        h("div", {}, h("div", { text: m.error }), h("div", { class: "notice-actions" }, h("button", { type: "button", class: "btn btn-secondary sm", text: "Try again", onclick: () => regenerate(m) }))),
      ),
    );
  } else if (m.finish === "length" && m.status !== "streaming") {
    body.append(
      h(
        "div",
        { class: "notice" },
        h("span", { html: icon("info", 18) }),
        h("div", {}, h("div", { text: "The reply hit the length limit." }), h("div", { class: "notice-actions" }, h("button", { type: "button", class: "btn btn-secondary sm", text: "Continue generating", onclick: () => continueGenerating(m) }))),
      ),
    );
  } else if (m.stopped && !m.content) {
    body.append(h("div", { class: "notice" }, h("span", { html: icon("info", 18) }), h("div", { text: "You stopped this reply." })));
  }
  el.replaceChildren(body);
  el._md = md;
  if (m.status !== "streaming") el.append(assistantActions(m));
  el.classList.toggle("latest", !!isLast);
}

function assistantActions(m) {
  const stats = m.stats && S.settings.showStats ? statsText(m.stats) : "";
  return h(
    "div",
    { class: "msg-actions" },
    actionBtn("copy", "Copy", (e) => copyWithFeedback(e.currentTarget, m.content)),
    actionBtn("volume-2", "Read aloud", (e) => readAloud(m, e.currentTarget)),
    actionBtn("refresh-cw", "Try again", () => regenerate(m)),
    actionBtn("ellipsis", "More actions", (e) =>
      openMenu(e.currentTarget, [
        { label: "Branch in new chat", icon: "split", onClick: () => branch(m) },
        { label: "Copy as plain text", icon: "file-text", onClick: () => copyText(plainText(m.content)).then(() => toast("Copied")) },
        m.reasoning ? { label: "Copy thoughts", icon: "brain", onClick: () => copyText(m.reasoning).then(() => toast("Copied")) } : null,
      ], { side: "top" }),
    ),
    versionNav(m),
    stats ? h("span", { class: "stats", text: stats, "data-tip": statsTip(m.stats) }) : null,
  );
}

function statsText(s) {
  if (!s?.tps) return "";
  return `${s.tps.toFixed(s.tps < 10 ? 1 : 0)} tokens/s`;
}

function statsTip(s) {
  const parts = [];
  if (s.n) parts.push(`${s.n} tokens`);
  if (s.ttft) parts.push(`first token after ${(s.ttft / 1000).toFixed(1)}s`);
  if (s.promptN) parts.push(`read ${s.promptN} new prompt tokens`);
  if (s.cached) parts.push(`${s.cached} reused from cache`);
  return parts.join(" · ");
}

async function copyWithFeedback(btn, text) {
  await copyText(text || "");
  btn.innerHTML = icon("check", 18);
  setTimeout(() => (btn.innerHTML = icon("copy", 18)), 1500);
}

// ---------- read aloud ----------

let speaking = null;
function readAloud(m, btn) {
  const synth = window.speechSynthesis;
  if (!synth) return toast("Read aloud isn't available on this PC.");
  if (speaking) {
    synth.cancel();
    const was = speaking;
    speaking = null;
    was.btn.innerHTML = icon("volume-2", 18);
    if (was.id === m.id) return;
  }
  const u = new SpeechSynthesisUtterance(plainText(m.content));
  const v = synth.getVoices().find((x) => x.name === S.settings.voice.name);
  if (v) u.voice = v;
  u.rate = S.settings.voice.rate || 1;
  u.onend = u.onerror = () => {
    if (speaking?.id === m.id) {
      speaking = null;
      btn.innerHTML = icon("volume-2", 18);
    }
  };
  speaking = { id: m.id, btn };
  btn.innerHTML = icon("square", 16);
  btn.setAttribute("aria-label", "Stop reading");
  synth.speak(u);
}

// ---------- scrolling ----------

let stick = true;
export function initScroll() {
  const c = $("#content");
  c.addEventListener("scroll", () => {
    stick = c.scrollHeight - c.scrollTop - c.clientHeight < 80;
    emit("scroll", stick);
  }, { passive: true });
}

export function scrollToBottom(force = false) {
  const c = $("#content");
  if (force || stick) {
    c.scrollTop = c.scrollHeight;
    stick = true;
  }
}

// ---------- context management ----------

function toApiMessage(m, vision) {
  if (m.role === "assistant") return { role: "assistant", content: m.content || "" };
  let text = m.content || "";
  const files = (m.attachments || []).filter((a) => a.type === "text");
  if (files.length) {
    const blocks = files.map((f) => {
      const fence = f.text.includes("```") ? "~~~~" : "```";
      return `[File: ${f.name}]\n${fence}\n${f.text}\n${fence}`;
    });
    text = blocks.join("\n\n") + (text ? "\n\n" + text : "");
  }
  const imgs = (m.attachments || []).filter((a) => a.type === "image");
  if (imgs.length && vision) {
    return { role: "user", content: [...imgs.map((i) => ({ type: "image_url", image_url: { url: i.dataUrl } })), { type: "text", text: text || "Describe this image." }] };
  }
  return { role: "user", content: text };
}

function msgTokens(m) {
  if (typeof m.content === "string") return estimateTokens(m.content) + 6;
  return m.content.reduce((n, p) => n + (p.type === "text" ? estimateTokens(p.text) : 700), 6);
}

/** Drops the oldest turns until the conversation fits the model's context. */
async function fitContext(history, sys, reasoning) {
  const { nCtx } = caps();
  const reserve = Math.round(clamp(nCtx * (reasoning ? 0.35 : 0.25), 512, reasoning ? 8192 : 4096));
  const budget = nCtx - reserve;
  const all = () => (sys ? [{ role: "system", content: sys }] : []).concat(history.map((x) => x.api));
  let est = all().reduce((n, m) => n + msgTokens(m), 0);
  if (est <= budget * 0.8) return { history, dropped: 0 };
  let exact = est;
  try {
    exact = (await api.countTokens({ messages: all() })).input_tokens || est;
  } catch {}
  const ratio = exact / Math.max(est, 1);
  let dropped = 0;
  while (exact > budget && history.length > 1) {
    const d = history.shift();
    exact -= msgTokens(d.api) * ratio;
    dropped++;
    // Keep the conversation starting with a user turn.
    while (history.length > 1 && history[0].msg.role !== "user") {
      exact -= msgTokens(history.shift().api) * ratio;
      dropped++;
    }
  }
  if (exact > nCtx - 256) {
    const e = new Error(`This message is too long for the model's memory (about ${Math.round(exact)} tokens, limit ${nCtx}). Shorten it, or raise the context size in Settings → Model & performance.`);
    e.tooLong = true;
    throw e;
  }
  return { history, dropped };
}

// ---------- sending ----------

function samplingParams(thinkOn) {
  const prof = S.model?.profile || {};
  const rec = (!thinkOn && prof.samplingNoThink) || prof.sampling || {};
  const out = {};
  for (const [k, v] of Object.entries(rec)) out[k] = v;
  for (const [k, v] of Object.entries(S.settings.sampling)) {
    if (v !== null && v !== "" && v !== undefined && !Number.isNaN(Number(v))) out[k] = Number(v);
  }
  if (out.max_tokens != null && out.max_tokens <= 0) delete out.max_tokens;
  return out;
}

export function thinkingEnabled() {
  return caps().reasoning && S.settings.thinking.enabled;
}

function engineReady() {
  return S.engine.status === "ready";
}

/** Sends a new user message from the composer. Returns false if not sent. */
export function send({ text, attachments = [] }) {
  if (S.generating) return false;
  if (!engineReady()) {
    if (S.engine.status === "loading") toast("The model is still loading. Your message will be ready to send in a moment.");
    else emit("need-model");
    return false;
  }
  if (!S.chat) {
    S.chat = newChat();
    if (S.temporary) S.chat.temporary = true;
    emit("chat-opened");
  }
  const chat = S.chat;
  const path = activePath(chat);
  const parentId = path.length ? path[path.length - 1].id : "root";
  const user = addMessage(chat, parentId, { role: "user", content: text, attachments });
  const asst = addMessage(chat, user.id, { role: "assistant", content: "", reasoning: "", status: "streaming", model: S.model?.entry?.name });
  chat.model = S.model?.entry?.path || chat.model;
  chat.modelName = S.model?.entry?.name || chat.modelName;
  emit("thread-changed");
  renderThread();
  scrollToBottom(true);
  generate(chat, asst);
  return true;
}

export function regenerate(m) {
  if (S.generating) return;
  if (!engineReady()) return emit("need-model");
  const chat = S.chat;
  const asst = addMessage(chat, m.parent, { role: "assistant", content: "", reasoning: "", status: "streaming", model: S.model?.entry?.name });
  renderThread();
  scrollToBottom(true);
  generate(chat, asst);
}

function editAndResend(m, text) {
  if (!engineReady()) return emit("need-model");
  const chat = S.chat;
  const user = addMessage(chat, m.parent, { role: "user", content: text, attachments: m.attachments || [] });
  const asst = addMessage(chat, user.id, { role: "assistant", content: "", reasoning: "", status: "streaming", model: S.model?.entry?.name });
  renderThread();
  scrollToBottom(true);
  generate(chat, asst);
}

function continueGenerating(m) {
  if (S.generating || !engineReady()) return;
  m.status = "streaming";
  m.finish = null;
  rerender(m);
  generate(S.chat, m, { prefill: true });
}

function branch(m) {
  const src = S.chat;
  const path = activePath(src);
  const idx = path.findIndex((x) => x.id === m.id);
  const chat = newChat();
  chat.title = (src.title || "New chat") + " (branch)";
  chat.model = src.model;
  chat.modelName = src.modelName;
  let parent = "root";
  for (const x of path.slice(0, idx + 1)) {
    const copy = structuredClone({ ...x, children: [], sel: null });
    delete copy.id;
    parent = addMessage(chat, parent, copy).id;
  }
  S.chat = chat;
  saveChat(chat, true);
  emit("chat-opened");
  toast("Branched into a new chat");
}

export function stop() {
  if (!S.generating) return;
  S.generating.stopped = true;
  S.generating.controller.abort();
}

function answerNow() {
  const g = S.generating;
  if (!g?.completionId) return;
  api.control(g.completionId, "reasoning_end").catch(() => {});
}

function rerender(m) {
  const el = turnEls.get(m.id);
  if (el) fillAssistant(el, m, true);
}

// Adaptive render throttle: slower machines render less often.
let renderCost = 8;
function makeScheduler(m) {
  let pending = false;
  let last = 0;
  return function schedule() {
    if (pending) return;
    pending = true;
    const wait = Math.max(0, clamp(renderCost * 4, 32, 250) - (performance.now() - last));
    setTimeout(() => requestAnimationFrame(() => {
      pending = false;
      last = performance.now();
      const t0 = performance.now();
      paintStreaming(m);
      renderCost = renderCost * 0.7 + (performance.now() - t0) * 0.3;
    }), wait);
  };
}

function paintStreaming(m) {
  const el = turnEls.get(m.id);
  if (!el || m.status !== "streaming") return;
  const hasThink = m.reasoning?.trim();
  const needsRebuild = !el._md || (hasThink && !el.querySelector(".thinking")) || (m.content && el.querySelector(".dots, .progress-note")) || (!m.content && !hasThink);
  if (needsRebuild) {
    fillAssistant(el, m, true);
  } else {
    const think = el.querySelector(".thinking");
    if (think) {
      const head = think.querySelector(".thinking-head span");
      if (m.content && m.thinkMs && head?.classList.contains("shimmer")) {
        fillAssistant(el, m, true); // thinking just finished: swap the header
      } else if (think.classList.contains("open") && think._md) {
        renderInto(think._md, m.reasoning || "", !m.content);
      }
    }
    if (m.content) renderInto(el._md, m.content, true);
  }
  placeCaret(el._md);
  scrollToBottom();
}

function placeCaret(md) {
  md?.querySelector(".caret")?.remove();
  if (!md?.lastElementChild) return;
  let target = md.lastElementChild;
  while (target.lastElementChild && !["PRE", "TABLE", "DIV"].includes(target.lastElementChild.tagName) && target.lastElementChild.tagName !== "BR") target = target.lastElementChild;
  if (target.classList?.contains("md-block")) target = target.lastElementChild || target;
  if (target && !["PRE", "TABLE"].includes(target.tagName) && !target.closest?.(".code-block, .table-wrap, .katex")) target.append(h("span", { class: "caret", "aria-hidden": "true" }));
}

async function generate(chat, m, opts = {}) {
  const think = thinkingEnabled();
  const vision = caps().vision;
  const path = activePath(chat);
  const idx = path.findIndex((x) => x.id === m.id);
  let history = path.slice(0, opts.prefill ? idx + 1 : idx).map((msg) => ({ msg, api: toApiMessage(msg, vision) }));
  if (opts.prefill) history[history.length - 1].api.content = m.content;
  const sys = systemPrompt();
  const controller = new AbortController();
  S.generating = { chatId: chat.id, msgId: m.id, controller, completionId: null };
  emit("generating", true);
  const sched = makeScheduler(m);
  const t0 = performance.now();
  let tFirst = 0, tThinkStart = 0;
  m.error = null;
  m.stopped = false;
  try {
    const fit = await fitContext(history, sys, think);
    if (fit.dropped) {
      chat.trimmedBefore = fit.history[0]?.msg.id;
      toast("Older messages were left out so the conversation fits the model's memory.");
    }
    const body = { messages: (sys ? [{ role: "system", content: sys }] : []).concat(fit.history.map((x) => x.api)), return_progress: true, cache_prompt: true, ...samplingParams(think) };
    if (caps().reasoning) {
      if (think) {
        body.chat_template_kwargs = { enable_thinking: true };
        body.reasoning_control = true;
        if (S.settings.thinking.effort && caps().effortLevels.includes(S.settings.thinking.effort)) body.reasoning_effort = S.settings.thinking.effort;
        if (S.settings.thinking.budget > 0) body.thinking_budget_tokens = S.settings.thinking.budget;
      } else {
        body.chat_template_kwargs = { enable_thinking: false };
        body.reasoning_effort = "none";
      }
    }
    const res = await streamChat(body, controller.signal, {
      onId: (id) => {
        S.generating && (S.generating.completionId = id);
      },
      onProgress: (p) => {
        const work = p.total - p.cache;
        if (work > 64 && !m.content && !m.reasoning) {
          m.progress = clamp((p.processed - p.cache) / Math.max(work, 1), 0, 1);
          sched();
        }
      },
      onDelta: (d) => {
        if (d.reasoning) {
          if (!tThinkStart) tThinkStart = performance.now();
          m.thinkStart ||= Date.now();
          m.reasoning += d.reasoning;
        }
        if (d.content) {
          if (!tFirst) tFirst = performance.now();
          if (tThinkStart && !m.thinkMs) m.thinkMs = performance.now() - tThinkStart;
          m.content += d.content;
        }
        m.progress = 0;
        sched();
      },
    });
    if (tThinkStart && !m.thinkMs) m.thinkMs = performance.now() - tThinkStart;
    m.finish = res.finishReason;
    const t = res.timings;
    if (t) {
      m.stats = { tps: t.predicted_per_second, n: t.predicted_n, promptN: t.prompt_n, cached: t.cache_n, ttft: Math.round((tFirst || performance.now()) - t0) };
      if (t.predicted_n >= 16 && S.model?.entry?.path) api.learn(S.model.entry.path, t.predicted_per_second, t.predicted_n);
    }
    if (res.usage) chat.lastTokens = res.usage.total_tokens;
  } catch (e) {
    if (controller.signal.aborted || S.generating?.stopped) {
      m.stopped = true;
    } else {
      m.error = friendlyError(e);
    }
  } finally {
    if (tThinkStart && !m.thinkMs) m.thinkMs = performance.now() - tThinkStart;
    m.status = "done";
    m.progress = 0;
    delete m.thinkStart;
    S.generating = null;
    emit("generating", false);
    if (S.chat === chat) {
      rerender(m);
      scrollToBottom();
    }
    if (!m.content && !m.reasoning && m.stopped) {
      // Nothing came back: keep the turn so the user can retry from it.
    }
    saveChat(chat, true);
    maybeTitle(chat);
  }
}

function friendlyError(e) {
  if (e.tooLong) return e.message;
  if (e.status === 503) return "No model is loaded. Choose a model and try again.";
  const msg = e.message || String(e);
  if (/exceed.*context|context size|too many tokens/i.test(msg)) return "This conversation is too long for the model's memory. Start a new chat, or raise the context size in Settings → Model & performance.";
  if (/failed to fetch|networkerror|load failed/i.test(msg)) return "Lost contact with the engine. It may have run out of memory. Reload the model from the model menu and try again.";
  if (/image|mtmd|multimodal/i.test(msg)) return "This model couldn't read the image: " + msg;
  return "Something went wrong: " + msg;
}

// ---------- titles ----------

function simpleTitle(text) {
  const t = plainText(text).replace(/\s+/g, " ").trim();
  if (!t) return "New chat";
  if (t.length <= 48) return t.charAt(0).toUpperCase() + t.slice(1);
  const cut = t.slice(0, 48);
  return (cut.slice(0, cut.lastIndexOf(" ") > 24 ? cut.lastIndexOf(" ") : 48) + "…").replace(/^./, (c) => c.toUpperCase());
}

async function maybeTitle(chat) {
  if (chat.title || chat.temporary) return;
  const path = activePath(chat);
  const first = path.find((x) => x.role === "user");
  const answer = path.find((x) => x.role === "assistant" && x.content);
  if (!first || !answer) return;
  chat.title = simpleTitle(first.content || first.attachments?.[0]?.name || "");
  saveChat(chat, true);
  const mode = S.settings.titles;
  const useModel = mode === "model" || (mode === "auto" && S.model?.profile?.modelTitles);
  if (!useModel || !engineReady() || S.generating) return;
  try {
    const body = {
      messages: [
        { role: "system", content: "You write very short titles for chats." },
        { role: "user", content: `Write a title of 2 to 6 words for a chat that starts with this message. Reply with the title only, no quotes or punctuation at the end.\n\nMessage: ${(first.content || "").slice(0, 600)}` },
      ],
      max_tokens: 24,
      temperature: 0.3,
      stream: false,
      cache_prompt: false,
    };
    if (caps().reasoning) {
      body.chat_template_kwargs = { enable_thinking: false };
      body.reasoning_effort = "none";
    }
    const res = await fetch("/v1/chat/completions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) return;
    const j = await res.json();
    let title = (j.choices?.[0]?.message?.content || "").replace(/<think>[\s\S]*?<\/think>/g, "").split("\n").map((s) => s.trim()).find(Boolean) || "";
    title = title.replace(/^["'*#\s]+|["'*.\s]+$/g, "").replace(/^title:\s*/i, "");
    if (title && title.length <= 80 && S.chats.find((c) => c.id === chat.id)?.title === chat.title) {
      chat.title = title;
      saveChat(chat, true);
    }
  } catch {}
}

export function openLatestActions() {
  return $(".turn.assistant.latest .msg-actions");
}

export function updateSettingsThinking(on) {
  updateSettings((s) => (s.thinking.enabled = on));
}

/** Copies the last assistant reply (keyboard shortcut). */
export function copyLastReply() {
  const last = activePath().filter((m) => m.role === "assistant" && m.content).pop();
  if (!last) return;
  copyText(last.content).then(() => toast("Copied the last reply"));
}

export function resetThreadDom() {
  for (const el of turnEls.values()) if (el._md) resetRender(el._md);
  turnEls.clear();
}
