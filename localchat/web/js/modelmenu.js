// Choosing and loading models: the header's model menu and the welcome,
// loading and error screens shown before a chat starts.
import { S, actions, emit } from "./state.js";
import { api } from "./api.js";
import { h, formatBytes, formatCtx, formatParams } from "./util.js";
import { icon } from "./icons.js";
import { openMenu, closeMenu, toast } from "./ui.js";
import { LOGO } from "./sidebar.js";

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

function modelRow(v, { onClick, current }) {
  return h(
    "button",
    { type: "button", class: "mm-row" + (v.missing ? " missing" : ""), onclick: onClick, disabled: v.missing, "aria-label": `${modelLabel(v)}${v.missing ? " (file not found)" : ""}` },
    h("span", { html: icon("box", 20), style: "color:var(--text-3)" }),
    h(
      "span",
      { class: "mm-body" },
      h("div", { class: "mm-name", text: modelLabel(v) }),
      h("div", { class: "mm-meta", text: v.missing ? "File not found" : metaLine(v) }),
      v.missing ? null : badges(v, v),
    ),
    current ? h("span", { class: "mm-check", html: icon("check", 18) }) : null,
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

/** Opens the model dropdown under (or beside) anchor. */
export async function openModelMenu(anchor, side = "bottom") {
  await refreshModels();
  const cur = S.model?.entry;
  const items = [];
  if (cur) {
    const st = S.engine;
    const status =
      st.status === "loading" ? `Loading… ${Math.round((st.progress || 0) * 100)}%` : st.status === "error" ? "Couldn't load" : st.status === "ready" ? "Ready" : "Not loaded";
    items.push({
      custom: h(
        "div",
        { class: "mm-current" },
        h("div", { class: "mm-title" }, h("span", { text: modelLabel(cur) })),
        h("div", { class: "mm-meta", text: metaLine(cur, { ctx: S.model?.profile?.ctx }) }),
        h("div", { class: "mm-meta", text: status + (S.engine.memory ? ` · using ${formatBytes(S.engine.memory.modelBytes + S.engine.memory.contextBytes + S.engine.memory.computeBytes)}` : "") }),
      ),
    });
  }
  const others = S.models.filter((m) => m.path !== cur?.path);
  if (others.length) {
    items.push({ heading: "On this PC" });
    items.push({ custom: h("div", { class: "mm-list" }, others.map((v) => modelRow(v, { onClick: () => (closeMenu(), loadModel(v.path)) }))) });
  }
  items.push({ divider: true });
  items.push({ label: "Browse for a model file…", icon: "folder-open", onClick: pickAndLoad });
  items.push({ label: "Find models on this PC", icon: "text-search", onClick: () => scan(true) });
  items.push({ label: "Add a models folder…", icon: "folder-plus", onClick: addFolder });
  if (cur && S.engine.status !== "idle") items.push({ label: "Unload model", icon: "x", hint: "Frees the memory it uses", onClick: unload });
  items.push({ label: "Model & performance settings", icon: "sliders-horizontal", onClick: () => actions.openSettings("model") });
  openMenu(anchor, items, { className: "model-menu", side, align: "start", width: 380 });
}

export async function pickAndLoad() {
  try {
    const r = await api.pickModel();
    if (r?.cancelled) return;
    if (r?.entry) loadModel(r.entry.path);
  } catch (e) {
    toast(e.message, { kind: "error", timeout: 6000 });
  }
}

export async function loadModel(path) {
  if (S.generating) actions.stop?.();
  try {
    const r = await api.load(path);
    S.model = r;
    S.props = null;
    actions.updateSettingsSilently?.((s) => (s.lastModel = path));
    emit("model");
  } catch (e) {
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
  const stage = S.engine.stage === "mmproj_model" ? "Loading the image add-on…" : pct > 0 ? "Loading into memory…" : "Starting the engine…";
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
    h("h1", { text: known.length ? "Choose a model to start" : "Welcome to LocalChat" }),
    h("p", { text: known.length ? "Pick one of your models below. It runs entirely on this PC, so your chats stay private." : "Chat with AI models that run entirely on this PC. Choose a GGUF model file you've downloaded to begin." }),
    h(
      "div",
      { class: "actions" },
      h("button", { type: "button", class: "btn btn-primary", onclick: pickAndLoad }, h("span", { html: icon("folder-open", 18) }), "Browse for a model file"),
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
    { type: "button", class: "model-switch", "aria-haspopup": "menu", "aria-expanded": "false", "aria-label": "Choose model", onclick: (ev) => openModelMenu(ev.currentTarget) },
    h("span", { class: "ms-name", text: e ? modelLabel(e) : "LocalChat" }),
    e && st !== "ready" ? null : e ? null : h("span", { class: "ms-sub", text: "Choose a model" }),
    h("span", { class: "ms-chev", html: icon("chevron-down", 18) }),
  );
  return btn;
}

export function statusPill() {
  const st = S.engine;
  if (st.status === "loading") {
    const pct = Math.round((st.progress || 0) * 100);
    return h("span", { class: "status-pill", role: "status" }, h("span", { text: "Loading" }), h("span", { class: "bar" }, h("i", { style: `width:${Math.max(pct, 4)}%` })), h("span", { text: pct ? pct + "%" : "" }));
  }
  if (st.status === "error") return h("span", { class: "status-pill", style: "color:var(--danger)", text: "Model didn't load" });
  return null;
}
