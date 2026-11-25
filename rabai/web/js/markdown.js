// Markdown rendering: GitHub-flavored Markdown, code highlighting, math, and
// an incremental renderer that only rebuilds the blocks that changed while a
// reply streams in (important on slow CPUs).
import { Marked } from "../vendor/marked.esm.js";
import DOMPurify from "../vendor/purify.es.mjs";
import { escapeHtml } from "./util.js";
import { icon } from "./icons.js";

const LANG_ALIASES = { js: "javascript", ts: "typescript", py: "python", sh: "bash", shell: "bash", zsh: "bash", ps1: "powershell", yml: "yaml", md: "markdown", "c++": "cpp", "c#": "csharp", cs: "csharp", rs: "rust", golang: "go", html: "xml", vue: "xml", jsonc: "json" };

class LRU {
  constructor(max) {
    this.max = max;
    this.map = new Map();
  }
  get(k) {
    const v = this.map.get(k);
    if (v !== undefined) {
      this.map.delete(k);
      this.map.set(k, v);
    }
    return v;
  }
  set(k, v) {
    this.map.set(k, v);
    if (this.map.size > this.max) this.map.delete(this.map.keys().next().value);
  }
}

const hlCache = new LRU(400);
const mathCache = new LRU(400);

function highlight(code, lang) {
  const hljs = window.hljs;
  const l = LANG_ALIASES[lang] || lang;
  const key = l + "\u0000" + code;
  const hit = hlCache.get(key);
  if (hit) return hit;
  let out;
  try {
    if (hljs && l && hljs.getLanguage(l)) out = hljs.highlight(code, { language: l, ignoreIllegals: true }).value;
    else if (hljs && code.length < 20000) out = hljs.highlightAuto(code).value;
  } catch {}
  out = out || escapeHtml(code);
  hlCache.set(key, out);
  return out;
}

function renderMath(tex, display) {
  const key = (display ? "D" : "I") + tex;
  const hit = mathCache.get(key);
  if (hit) return hit;
  let out;
  if (window.katex) {
    try {
      out = window.katex.renderToString(tex, { displayMode: display, throwOnError: false, output: "htmlAndMathml", strict: "ignore" });
    } catch {}
  }
  out = out || `<code class="math-fallback">${escapeHtml(tex)}</code>`;
  mathCache.set(key, out);
  return out;
}

// Streaming state for the current render pass.
let streamingLastCode = false;

const marked = new Marked({ gfm: true, breaks: true });

marked.use({
  extensions: [
    {
      name: "mathBlock",
      level: "block",
      start: (src) => src.match(/\$\$|\\\[/)?.index,
      tokenizer(src) {
        const m = /^\$\$([\s\S]+?)\$\$/.exec(src) || /^\\\[([\s\S]+?)\\\]/.exec(src);
        if (m) return { type: "mathBlock", raw: m[0], text: m[1].trim() };
      },
      renderer: (t) => `<div class="math-block">${renderMath(t.text, true)}</div>`,
    },
    {
      name: "mathInline",
      level: "inline",
      start: (src) => src.match(/\$|\\\(/)?.index,
      tokenizer(src) {
        let m = /^\\\(([\s\S]+?)\\\)/.exec(src);
        if (m) return { type: "mathInline", raw: m[0], text: m[1].trim() };
        m = /^\$\$([^$]+?)\$\$/.exec(src);
        if (m) return { type: "mathInline", raw: m[0], text: m[1].trim(), display: true };
        // $x$: no space just inside the dollars, and not followed by a digit
        // (so "$5 and $10" stays plain text).
        m = /^\$(?!\s)((?:\\\$|[^$\n])+?)(?<!\s)\$(?!\d)/.exec(src);
        if (m) return { type: "mathInline", raw: m[0], text: m[1] };
      },
      renderer: (t) => renderMath(t.text, !!t.display),
    },
  ],
  renderer: {
    code({ text, lang, raw }) {
      const language = (lang || "").trim().split(/\s+/)[0].toLowerCase();
      const open = streamingLastCode && !/(```|~~~)\s*$/.test(raw.trimEnd());
      const body = open ? escapeHtml(text) : highlight(text, language);
      const label = escapeHtml(language || "text");
      return `<div class="code-block"><div class="code-head"><span class="code-lang">${label}</span><button type="button" class="code-copy" aria-label="Copy code">${icon("copy", 16)}<span>Copy</span></button></div><pre><code class="hljs${language ? " language-" + escapeHtml(language) : ""}">${body}</code></pre></div>`;
    },
    html({ text }) {
      return escapeHtml(text);
    },
    table(token) {
      const head = token.header.map((c) => `<th${c.align ? ` style="text-align:${c.align}"` : ""}>${this.parser.parseInline(c.tokens)}</th>`).join("");
      const rows = token.rows
        .map((r) => "<tr>" + r.map((c) => `<td${c.align ? ` style="text-align:${c.align}"` : ""}>${this.parser.parseInline(c.tokens)}</td>`).join("") + "</tr>")
        .join("");
      return `<div class="table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${rows}</tbody></table></div>`;
    },
    link({ href, title, tokens }) {
      const text = this.parser.parseInline(tokens);
      const safe = /^(https?:|mailto:)/i.test(href || "") ? href : null;
      if (!safe) return text;
      return `<a href="${escapeHtml(safe)}" target="_blank" rel="noopener noreferrer"${title ? ` title="${escapeHtml(title)}"` : ""}>${text}</a>`;
    },
    image({ text }) {
      return escapeHtml(text ? `[image: ${text}]` : "");
    },
  },
});

const PURIFY = { ADD_ATTR: ["target"], FORBID_TAGS: ["style", "form", "input", "textarea", "select", "iframe"] };

function sanitize(html) {
  return DOMPurify.sanitize(html, PURIFY);
}

/** Renders Markdown to sanitized HTML in one go (for finished messages). */
export function renderMarkdown(text) {
  streamingLastCode = false;
  return sanitize(marked.parse(text || ""));
}

/**
 * Renders Markdown into el, reusing the DOM of top-level blocks that haven't
 * changed since the previous call. Returns the element.
 */
export function renderInto(el, text, streaming = false) {
  const tokens = marked.lexer(text || "");
  const prev = el._mdRaw || [];
  let same = 0;
  while (same < prev.length && same < tokens.length && prev[same] === tokens[same].raw && !(streaming && same === tokens.length - 1)) same++;
  // Drop blocks from the first changed one onward.
  while (el.children.length > same) el.lastElementChild.remove();
  for (let i = same; i < tokens.length; i++) {
    const t = tokens[i];
    if (t.type === "space") {
      el.append(blockEl(""));
      continue;
    }
    streamingLastCode = streaming && i === tokens.length - 1 && t.type === "code";
    const part = [t];
    part.links = tokens.links;
    el.append(blockEl(sanitize(marked.parser(part))));
  }
  streamingLastCode = false;
  el._mdRaw = tokens.map((t) => t.raw);
  return el;
}

function blockEl(html) {
  const d = document.createElement("div");
  d.className = "md-block";
  d.innerHTML = html;
  return d;
}

/** Resets the incremental cache (when a message is re-rendered from scratch). */
export function resetRender(el) {
  el._mdRaw = null;
  el.replaceChildren();
}
