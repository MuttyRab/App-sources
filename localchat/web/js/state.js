// App-wide state, settings defaults and theme handling.
import { api } from "./api.js";
import { debounce } from "./util.js";

export const DEFAULT_SETTINGS = {
  theme: "system", // system | light | dark
  accent: "default",
  sendOnEnter: true,
  showThinking: true,
  titles: "auto", // auto | model | simple
  autoLoad: true,
  reduceMotion: false,
  showStats: true,
  lastModel: "",
  custom: { enabled: true, name: "", job: "", traits: "", about: "" },
  personality: "default",
  systemPrompt: "",
  sampling: { temperature: null, top_p: null, top_k: null, min_p: null, repeat_penalty: null, max_tokens: null },
  thinking: { enabled: true, effort: "", budget: 0 },
  perf: { preset: "balanced", ctx: 0, threads: 0, kvCache: "auto" },
  idleUnloadMin: 0,
  voice: { name: "", rate: 1 },
  sidebarCollapsed: false,
  seenWelcome: false,
};

export const ACCENTS = {
  default: { label: "Default", light: "#0d0d0d", dark: "#ffffff", link: null },
  blue: { label: "Blue", light: "#0169cc", dark: "#48aaff" },
  green: { label: "Green", light: "#008635", dark: "#40c977" },
  yellow: { label: "Yellow", light: "#b27f00", dark: "#ffd240" },
  pink: { label: "Pink", light: "#c8379b", dark: "#ff6bc6" },
  orange: { label: "Orange", light: "#d35a00", dark: "#ff9e4c" },
  purple: { label: "Purple", light: "#8045e5", dark: "#b18cff" },
};

export const PERSONALITIES = {
  default: { label: "Default", hint: "Balanced and helpful", prompt: "" },
  professional: { label: "Professional", hint: "Polished and precise", prompt: "Keep a polished, professional tone. Be precise and well organized." },
  friendly: { label: "Friendly", hint: "Warm and chatty", prompt: "Be warm, friendly and conversational, like a helpful friend." },
  candid: { label: "Candid", hint: "Direct and encouraging", prompt: "Be direct and candid. Give honest opinions and push back when something is wrong." },
  quirky: { label: "Quirky", hint: "Playful and imaginative", prompt: "Be playful and imaginative, with light humor where it fits." },
  efficient: { label: "Efficient", hint: "Concise and plain", prompt: "Be concise. Answer in as few words as needed, with no filler." },
  nerdy: { label: "Nerdy", hint: "Exploratory and enthusiastic", prompt: "Be enthusiastic about ideas, explain the why behind things and mention interesting details." },
  cynical: { label: "Cynical", hint: "Critical and sarcastic", prompt: "Be dry and a little sarcastic, but still genuinely helpful and accurate." },
};

export const TRAITS = ["Chatty", "Witty", "Straight shooting", "Encouraging", "Gen Z", "Skeptical", "Traditional", "Forward thinking", "Poetic"];

/** The single mutable store. Modules read it directly and call notify(). */
export const S = {
  settings: structuredClone(DEFAULT_SETTINGS),
  chats: [], // metadata, newest first
  chat: null, // the open chat (full), or null for a new chat
  temporary: false,
  engine: { status: "idle" },
  model: null, // { entry, profile, prefs } from the backend
  props: null, // engine /props once ready
  hw: null,
  version: "",
  engineInfo: "",
  generating: null, // { chatId, msgId, controller, completionId }
  models: [],
};

const listeners = new Map();

export function on(event, fn) {
  if (!listeners.has(event)) listeners.set(event, new Set());
  listeners.get(event).add(fn);
  return () => listeners.get(event).delete(fn);
}

export function emit(event, data) {
  listeners.get(event)?.forEach((fn) => {
    try {
      fn(data);
    } catch (e) {
      console.error(e);
    }
  });
}

function merge(def, val) {
  if (val == null || typeof def !== "object" || Array.isArray(def)) return val ?? def;
  const out = { ...def };
  for (const k of Object.keys(val)) out[k] = k in def ? merge(def[k], val[k]) : val[k];
  return out;
}

export async function loadSettings() {
  try {
    const s = await api.settings();
    S.settings = merge(structuredClone(DEFAULT_SETTINGS), s || {});
  } catch {
    S.settings = structuredClone(DEFAULT_SETTINGS);
  }
  applyTheme();
}

const persist = debounce(() => api.saveSettings(S.settings).catch(() => {}), 300);

/** Updates settings with a mutator and saves them. */
export function updateSettings(mutator) {
  mutator(S.settings);
  persist();
  applyTheme();
  emit("settings");
}

const darkQuery = matchMedia("(prefers-color-scheme: dark)");
darkQuery.addEventListener?.("change", () => applyTheme());

export function isDark() {
  const t = S.settings.theme;
  return t === "dark" || (t === "system" && darkQuery.matches);
}

let lastThemeKey = "";
export function applyTheme() {
  const root = document.documentElement;
  const dark = isDark();
  root.dataset.theme = dark ? "dark" : "light";
  const acc = ACCENTS[S.settings.accent] || ACCENTS.default;
  root.dataset.accent = S.settings.accent || "default";
  root.style.setProperty("--accent", dark ? acc.dark : acc.light);
  root.dataset.motion = S.settings.reduceMotion ? "reduce" : "";
  const key = dark + "";
  if (key !== lastThemeKey) {
    lastThemeKey = key;
    api.theme(dark, dark ? "#212121" : "#ffffff");
  }
}

/** Builds the system prompt from custom instructions and personality. */
export function systemPrompt() {
  const st = S.settings;
  if (st.systemPrompt?.trim()) return st.systemPrompt.trim();
  const parts = [];
  const today = new Date().toLocaleDateString(undefined, { weekday: "long", year: "numeric", month: "long", day: "numeric" });
  parts.push(`You are a helpful assistant running locally on the user's computer. Today is ${today}. Format answers with Markdown when it helps readability.`);
  const p = PERSONALITIES[st.personality];
  if (p?.prompt) parts.push(p.prompt);
  const c = st.custom;
  if (c.enabled) {
    if (c.name?.trim()) parts.push(`The user's name is ${c.name.trim()}.`);
    if (c.job?.trim()) parts.push(`The user's occupation: ${c.job.trim()}.`);
    if (c.traits?.trim()) parts.push(`Traits the user wants you to have: ${c.traits.trim()}`);
    if (c.about?.trim()) parts.push(`More about the user: ${c.about.trim()}`);
  }
  return parts.join("\n\n");
}

/** Capabilities of the loaded model, merging the backend profile and /props. */
export function caps() {
  const prof = S.model?.profile || {};
  const tc = S.props?.chat_template_caps || {};
  const reasoning = !!(prof.reasoning || tc.supports_reasoning_effort);
  const vision = !!(S.props?.modalities?.vision ?? prof.vision);
  const effortLevels = Array.isArray(tc.reasoning_effort_levels) ? tc.reasoning_effort_levels : [];
  const nCtx = S.props?.default_generation_settings?.n_ctx || prof.ctx || 4096;
  return { reasoning, vision, effortLevels, nCtx };
}

/** Cross-module commands, filled in by app.js (avoids import cycles). */
export const actions = {};
