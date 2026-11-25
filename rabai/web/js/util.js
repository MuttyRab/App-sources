// Small DOM and formatting helpers shared by every module.

/**
 * Creates an element. Props: class, text, html, on{Event}, attrs (aria-*,
 * data-*, etc.), and any other property assigned directly.
 */
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "text") el.textContent = v;
    else if (k === "html") el.innerHTML = v;
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k.startsWith("aria-") || k.startsWith("data-") || k === "role" || k === "for" || k === "tabindex") el.setAttribute(k, v === true ? "" : v);
    else el[k] = v;
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return el;
}

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

export function uid(prefix = "") {
  const a = new Uint8Array(9);
  crypto.getRandomValues(a);
  return prefix + [...a].map((b) => b.toString(36).padStart(2, "0")).join("").slice(0, 14);
}

export function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

export function debounce(fn, ms) {
  let t;
  return (...a) => {
    clearTimeout(t);
    t = setTimeout(() => fn(...a), ms);
  };
}

export function formatBytes(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  const v = n / 1024 ** i;
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}`;
}

export function formatCtx(n) {
  if (!n) return "";
  return n >= 1024 ? `${Math.round(n / 1024)}K` : String(n);
}

export function formatParams(p) {
  if (!p) return "";
  if (p >= 1e9) return `${(p / 1e9).toFixed(p >= 1e10 ? 0 : 1).replace(/\.0$/, "")}B`;
  return `${Math.round(p / 1e6)}M`;
}

export function formatDuration(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${s % 60}s`;
}

/** Groups timestamps the way ChatGPT's history does. */
export function dateGroup(ts, now = Date.now()) {
  const d = new Date(ts);
  const today = new Date(now);
  today.setHours(0, 0, 0, 0);
  const diff = (today - new Date(d.getFullYear(), d.getMonth(), d.getDate())) / 86400000;
  if (diff <= 0) return "Today";
  if (diff === 1) return "Yesterday";
  if (diff < 7) return "Previous 7 days";
  if (diff < 30) return "Previous 30 days";
  if (d.getFullYear() === today.getFullYear()) return d.toLocaleString(undefined, { month: "long" });
  return String(d.getFullYear());
}

/** Turns Markdown into readable plain text (for read aloud and titles). */
export function plainText(md) {
  return String(md || "")
    .replace(/```[\s\S]*?```/g, " (code) ")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/(\*\*|__)(.*?)\1/g, "$2")
    .replace(/(\*|_)(.*?)\1/g, "$2")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/^\s*>\s?/gm, "")
    .replace(/\$\$[\s\S]*?\$\$/g, " (formula) ")
    .replace(/\|/g, " ")
    .replace(/\s+\n/g, "\n")
    .trim();
}

/** Rough token estimate when the engine can't be asked (≈3.2 chars/token). */
export function estimateTokens(text) {
  return Math.ceil(String(text || "").length / 3.2);
}

export function clamp(v, lo, hi) {
  return Math.min(hi, Math.max(lo, v));
}

export function isMac() {
  return /Mac/.test(navigator.platform);
}

export const mod = isMac() ? "⌘" : "Ctrl";

/** Copies text to the clipboard, falling back to a hidden textarea. */
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = h("textarea", { value: text, style: "position:fixed;opacity:0" });
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

export function prefersReducedMotion() {
  return document.documentElement.dataset.motion === "reduce" || matchMedia("(prefers-reduced-motion: reduce)").matches;
}
