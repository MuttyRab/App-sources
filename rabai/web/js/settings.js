// Settings dialog (ChatGPT-style tabs), keyboard shortcuts and archived chats.
import { S, ACCENTS, PERSONALITIES, TRAITS, updateSettings, actions, caps, DEFAULT_SETTINGS } from "./state.js";
import { api } from "./api.js";
import { h, formatBytes, formatCtx, mod } from "./util.js";
import { icon } from "./icons.js";
import { openModal, confirmDialog, toast } from "./ui.js";
import { metaLine, modelLabel, gpuSummary } from "./modelmenu.js";
import { refreshImages, setDefaultImage } from "./images.js";

const TABS = [
  ["general", "General", "settings"],
  ["personalization", "Personalization", "user-round-pen"],
  ["model", "Model & performance", "gauge"],
  ["pictures", "Pictures", "image"],
  ["speech", "Speech", "audio-lines"],
  ["data", "Data controls", "database"],
  ["about", "About", "info"],
];

function row(title, desc, control) {
  return h("div", { class: "row" }, h("div", { class: "row-text" }, h("div", { class: "row-title", text: title }), desc ? h("div", { class: "row-desc", text: desc }) : null), control);
}

function toggle(get, set, label) {
  const b = h("button", { type: "button", class: "switch", role: "switch", "aria-label": label, "aria-checked": String(!!get()) });
  b.addEventListener("click", () => {
    const v = !get();
    set(v);
    b.setAttribute("aria-checked", String(v));
  });
  return b;
}

function select(options, value, onChange, label) {
  const s = h("select", { class: "select", "aria-label": label, onchange: () => onChange(s.value) });
  for (const [v, l] of options) s.append(h("option", { value: v, text: l, selected: String(v) === String(value) }));
  return s;
}

function seg(options, value, onChange, label) {
  const wrap = h("div", { class: "seg", role: "group", "aria-label": label });
  for (const [v, l] of options) {
    const b = h("button", { type: "button", "aria-pressed": String(v === value), text: l });
    b.addEventListener("click", () => {
      wrap.querySelectorAll("button").forEach((x) => x.setAttribute("aria-pressed", "false"));
      b.setAttribute("aria-pressed", "true");
      onChange(v);
    });
    wrap.append(b);
  }
  return wrap;
}

function slider({ min, max, step, value, def, fmt = (v) => v, onChange, label }) {
  const val = h("span", { class: "slider-val" });
  const input = h("input", { type: "range", min, max, step, "aria-label": label });
  const reset = h("button", { type: "button", class: "link-btn", text: "Default" });
  const paint = (v) => {
    const isDef = v === null || v === undefined;
    input.value = isDef ? def : v;
    val.textContent = isDef ? "Model" : fmt(Number(v));
    reset.hidden = isDef;
  };
  input.addEventListener("input", () => {
    onChange(Number(input.value));
    paint(Number(input.value));
  });
  reset.addEventListener("click", () => {
    onChange(null);
    paint(null);
  });
  paint(value);
  return h("div", { class: "slider-row" }, reset, input, val);
}

let reloadNeeded = false;
function markReload(panel) {
  reloadNeeded = true;
  panel.querySelector(".reload-row")?.removeAttribute("hidden");
}

export function openSettings(tab = "general") {
  reloadNeeded = false;
  openModal({
    title: "Settings",
    className: "settings",
    content: (body) => {
      const nav = h("div", { class: "settings-nav", role: "tablist", "aria-orientation": "vertical" });
      const panel = h("div", { class: "settings-panel", role: "tabpanel" });
      const show = (id) => {
        nav.querySelectorAll(".settings-tab").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.tab === id)));
        panel.replaceChildren(...PANELS[id](panel));
        panel.scrollTop = 0;
      };
      for (const [id, label, ic] of TABS) {
        nav.append(h("button", { type: "button", class: "settings-tab", role: "tab", "data-tab": id, onclick: () => show(id) }, h("span", { html: icon(ic, 18) }), label));
      }
      nav.addEventListener("keydown", (e) => {
        if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
        const tabs = [...nav.children];
        const i = tabs.indexOf(document.activeElement);
        const n = tabs[(i + (e.key === "ArrowDown" ? 1 : -1) + tabs.length) % tabs.length];
        n.focus();
        n.click();
        e.preventDefault();
      });
      body.append(h("div", { class: "settings-layout" }, nav, panel));
      show(TABS.some((t) => t[0] === tab) ? tab : "general");
    },
  });
}

const set = (fn) => updateSettings(fn);

const PANELS = {
  general: () => [
    h("h3", { text: "General" }),
    row("Theme", null, select([["system", "System"], ["dark", "Dark"], ["light", "Light"]], S.settings.theme, (v) => set((s) => (s.theme = v)), "Theme")),
    row("Accent color", "Used for links, highlights and selection", select(Object.entries(ACCENTS).map(([k, a]) => [k, a.label]), S.settings.accent, (v) => set((s) => (s.accent = v)), "Accent color")),
    row("Send with Enter", `When off, press ${mod}+Enter to send and Enter for a new line`, toggle(() => S.settings.sendOnEnter, (v) => set((s) => (s.sendOnEnter = v)), "Send with Enter")),
    row("Show thinking as it happens", "For models that think before answering", toggle(() => S.settings.showThinking, (v) => set((s) => (s.showThinking = v)), "Show thinking")),
    row("Chat titles", "Model-written titles cost a few seconds on slow PCs", select([["auto", "Automatic"], ["model", "Written by the model"], ["simple", "First message"]], S.settings.titles, (v) => set((s) => (s.titles = v)), "Chat titles")),
    row("Load the last model at startup", null, toggle(() => S.settings.autoLoad, (v) => set((s) => (s.autoLoad = v)), "Load last model")),
    row("Show reply speed", "Tokens per second under each reply", toggle(() => S.settings.showStats, (v) => set((s) => (s.showStats = v)), "Show reply speed")),
    row("Reduce motion", "Turns off animations", toggle(() => S.settings.reduceMotion, (v) => set((s) => (s.reduceMotion = v)), "Reduce motion")),
  ],

  personalization: () => {
    const c = S.settings.custom;
    const field = (label, key, multiline, placeholder) => {
      const el = multiline
        ? h("textarea", { class: "textarea", value: c[key] || "", placeholder, maxLength: 1500, "aria-label": label })
        : h("input", { class: "input", value: c[key] || "", placeholder, maxLength: 120, "aria-label": label });
      el.addEventListener("input", () => set((s) => (s.custom[key] = el.value)));
      return [h("label", { class: "field-label", text: label }), el];
    };
    const traits = field("What traits should RabAI have?", "traits", true, "Describe or select traits");
    const chips = h(
      "div",
      { class: "trait-chips" },
      TRAITS.map((t) =>
        h("button", {
          type: "button",
          text: "+ " + t,
          onclick: () => {
            const ta = traits[1];
            ta.value = (ta.value.trim() ? ta.value.trim().replace(/[.,]?$/, ", ") : "") + t.toLowerCase();
            ta.dispatchEvent(new Event("input"));
          },
        }),
      ),
    );
    const sys = h("textarea", { class: "textarea", value: S.settings.systemPrompt, placeholder: "Leave empty to build it from the options above", "aria-label": "Custom system prompt" });
    sys.addEventListener("input", () => set((s) => (s.systemPrompt = sys.value)));
    return [
      h("h3", { text: "Personalization" }),
      row("Base style and tone", "How RabAI writes its answers", select(Object.entries(PERSONALITIES).map(([k, p]) => [k, `${p.label} — ${p.hint}`]), S.settings.personality, (v) => set((s) => (s.personality = v)), "Base style and tone")),
      row("Custom instructions", "Share what RabAI should know about you", toggle(() => c.enabled, (v) => set((s) => (s.custom.enabled = v)), "Custom instructions")),
      h("div", { class: "row block" }, ...field("What should RabAI call you?", "name", false, "Nickname"), ...field("What do you do?", "job", false, "Student, engineer, teacher…"), ...traits, chips, ...field("Anything else RabAI should know about you?", "about", true, "Interests, values or preferences to keep in mind")),
      h("div", { class: "row block" }, h("label", { class: "field-label", text: "Advanced: custom system prompt" }), sys, h("div", { class: "row-desc", text: "Replaces everything above. The model reads it at the start of every chat." })),
    ];
  },

  model: (panel) => {
    const m = S.model;
    const prof = m?.profile || {};
    const hw = S.hw || {};
    const c = caps();
    const out = [h("h3", { text: "Model & performance" })];
    if (m?.entry) {
      out.push(
        row(modelLabel(m.entry), metaLine(m.entry, { ctx: prof.ctx }) + (prof.fit ? ` · ${prof.fit === "fits" ? "fits in memory" : prof.fit === "tight" ? "tight on memory" : "too big for this PC"}` : ""), h("button", { type: "button", class: "btn btn-secondary sm", text: "Reveal file", onclick: () => api.reveal(m.entry.path).catch((e) => toast(e.message)) })),
      );
      if (prof.notes?.length) out.push(h("div", { class: "notice" }, h("span", { html: icon("info", 18) }), h("div", {}, prof.notes.map((n) => h("div", { text: n })))));
    } else {
      out.push(h("div", { class: "notice" }, h("span", { html: icon("info", 18) }), h("div", { text: "No model is loaded. These settings apply to the next model you load." })));
    }
    const perf = S.settings.perf;
    const ctxMax = m?.entry?.ctxTrain || 131072;
    const ctxOpts = [[0, `Automatic${prof.ctx ? ` (${formatCtx(prof.ctx)})` : ""}`], ...[2048, 4096, 8192, 16384, 32768, 65536, 131072].filter((x) => x <= ctxMax).map((x) => [x, `${formatCtx(x)} tokens`])];
    const threadOpts = [[0, `Automatic${prof.threads ? ` (${prof.threads})` : ""}`], ...Array.from({ length: Math.max(1, hw.logical || 8) }, (_, i) => [i + 1, String(i + 1)])];
    out.push(
      row("Speed or memory", "Faster uses a shorter context; longer memory remembers more of the chat but needs more RAM", seg([["fast", "Faster"], ["balanced", "Balanced"], ["long", "Longer memory"]], perf.preset, (v) => (set((s) => (s.perf.preset = v)), markReload(panel)), "Speed or memory")),
      row("Context size", "How much of the conversation the model can see at once", select(ctxOpts, perf.ctx, (v) => (set((s) => (s.perf.ctx = Number(v))), markReload(panel)), "Context size")),
      row("CPU threads", hw.perfCores && hw.perfCores < hw.physical ? `Your CPU has ${hw.perfCores} performance and ${hw.physical - hw.perfCores} efficiency cores` : `Your CPU has ${hw.physical || "?"} cores`, select(threadOpts, perf.threads, (v) => (set((s) => (s.perf.threads = Number(v))), markReload(panel)), "CPU threads")),
      row("Compress conversation memory", "Halves the memory the context uses, with little effect on quality", select([["auto", "Automatic"], ["f16", "Off"], ["q8_0", "On"]], perf.kvCache, (v) => (set((s) => (s.perf.kvCache = v)), markReload(panel)), "Compress conversation memory")),
      row(
        "Graphics card",
        gpuSummary(),
        h(
          "div",
          { class: "row-controls" },
          seg([["auto", "Automatic"], ["on", "Always"], ["off", "Never"]], perf.gpu || "auto", (v) => (set((s) => (s.perf.gpu = v)), markReload(panel)), "Graphics card"),
          S.gpu?.best ? h("button", { type: "button", class: "btn btn-ghost sm", text: "Test again", "data-tip": "Measure every model on the graphics card again (for example after a driver update)", onclick: async () => { await api.retestGPU(); toast("Each model will be tested again the next time it's used."); markReload(panel); } }) : null,
        ),
      ),
      row("Free memory when idle", "Unloads the model after a quiet period; it reloads when you send a message", select([[0, "Never"], [5, "After 5 minutes"], [15, "After 15 minutes"], [30, "After 30 minutes"], [60, "After 1 hour"]], S.settings.idleUnloadMin, (v) => (set((s) => (s.idleUnloadMin = Number(v))), markReload(panel)), "Free memory when idle")),
      h(
        "div",
        { class: "row reload-row", hidden: !reloadNeeded },
        h("div", { class: "row-text" }, h("div", { class: "row-title", text: "Reload the model to apply these changes" })),
        h("button", { type: "button", class: "btn btn-primary sm", text: "Reload model", disabled: !m?.entry, onclick: () => m?.entry && actions.loadModel(m.entry.path) }),
      ),
    );
    if (c.reasoning || !m) {
      out.push(
        row("Thinking", "Reasoning models think before answering. Turning it off makes replies faster", toggle(() => S.settings.thinking.enabled, (v) => set((s) => (s.thinking.enabled = v)), "Thinking")),
        row("Thinking limit", "Caps how long the model thinks, useful on slow PCs", select([[0, "No limit"], [512, "512 tokens"], [1024, "1K tokens"], [2048, "2K tokens"], [4096, "4K tokens"], [8192, "8K tokens"]], S.settings.thinking.budget, (v) => set((s) => (s.thinking.budget = Number(v))), "Thinking limit")),
      );
    }
    const sp = S.settings.sampling;
    const rec = prof.sampling || {};
    const fmt2 = (v) => v.toFixed(2);
    out.push(
      h("h3", { text: "Response style", style: "margin-top:24px" }),
      h("div", { class: "row-desc", style: "margin:10px 0 4px", text: Object.keys(rec).length ? "Defaults follow this model's recommended settings." : "Defaults follow the model's built-in settings." }),
      row("Temperature", "Higher is more creative, lower is more focused", slider({ min: 0, max: 2, step: 0.05, value: sp.temperature, def: rec.temperature ?? 0.8, fmt: fmt2, label: "Temperature", onChange: (v) => set((s) => (s.sampling.temperature = v)) })),
      row("Top P", null, slider({ min: 0, max: 1, step: 0.01, value: sp.top_p, def: rec.top_p ?? 0.95, fmt: fmt2, label: "Top P", onChange: (v) => set((s) => (s.sampling.top_p = v)) })),
      row("Top K", null, slider({ min: 0, max: 200, step: 1, value: sp.top_k, def: rec.top_k ?? 40, label: "Top K", onChange: (v) => set((s) => (s.sampling.top_k = v)) })),
      row("Min P", null, slider({ min: 0, max: 0.5, step: 0.01, value: sp.min_p, def: rec.min_p ?? 0.05, fmt: fmt2, label: "Min P", onChange: (v) => set((s) => (s.sampling.min_p = v)) })),
      row("Repeat penalty", "Raise it if the model repeats itself", slider({ min: 1, max: 1.5, step: 0.01, value: sp.repeat_penalty, def: rec.repeat_penalty ?? 1, fmt: fmt2, label: "Repeat penalty", onChange: (v) => set((s) => (s.sampling.repeat_penalty = v)) })),
      row("Maximum reply length", null, select([[null, "No limit"], [256, "256 tokens"], [512, "512 tokens"], [1024, "1K tokens"], [2048, "2K tokens"], [4096, "4K tokens"]], sp.max_tokens, (v) => set((s) => (s.sampling.max_tokens = v === "null" ? null : Number(v))), "Maximum reply length")),
      h("div", { class: "row" }, h("div"), h("button", { type: "button", class: "btn btn-ghost sm", text: "Reset response style", onclick: () => { set((s) => (s.sampling = structuredClone(DEFAULT_SETTINGS.sampling))); panel.replaceChildren(...PANELS.model(panel)); } })),
    );
    const learned = S.models.find((x) => x.path === m?.entry?.path)?.learned;
    out.push(
      h("h3", { text: "This PC", style: "margin-top:24px" }),
      h(
        "dl",
        { class: "hw-grid" },
        h("dt", { text: "Processor" }), h("dd", { text: hw.cpuName || "Unknown" }),
        h("dt", { text: "Cores" }), h("dd", { text: `${hw.physical || "?"} cores, ${hw.logical || "?"} threads${hw.perfCores && hw.perfCores < hw.physical ? ` (${hw.perfCores} performance)` : ""}` }),
        h("dt", { text: "Memory" }), h("dd", { text: `${formatBytes(hw.totalRam)} total, ${formatBytes(hw.availRam)} free` }),
        h("dt", { text: "Instruction sets" }), h("dd", { text: [hw.avx && "AVX", hw.avx2 && "AVX2", hw.fma && "FMA", hw.avx512 && "AVX-512"].filter(Boolean).join(", ") || "Basic (SSE)" }),
        h("dt", { text: "Graphics card" }), h("dd", { text: S.gpu?.best ? `${S.gpu.best.desc}${S.gpu.best.totalMiB ? `, ${formatBytes(S.gpu.best.totalMiB * 1048576)}` : ""}${S.gpu.best.integrated ? " (built into the processor)" : ""}` : S.gpu?.checked ? "None usable" : "Checking…" }),
        h("dt", { text: "Engine" }), h("dd", { text: S.engineInfo || "llama.cpp" }),
        prof.threads ? [h("dt", { text: "Current settings" }), h("dd", { text: `${prof.accel === "gpu" || prof.accel === "gpu_safe" ? "Graphics card" + (prof.accel === "gpu_safe" ? " (safe mode)" : "") + ", " : prof.accel === "cpu" ? "Processor, " : ""}${prof.threads} threads, ${formatCtx(prof.ctx)} context, ${prof.cacheK === "q8_0" ? "compressed" : "full"} memory` })] : null,
        learned?.accel?.reason ? [h("dt", { text: "Graphics card test" }), h("dd", { text: learned.accel.reason })] : null,
        learned?.tokensPerSec ? [h("dt", { text: "Measured speed" }), h("dd", { text: `${learned.tokensPerSec.toFixed(1)} tokens/s with this model` })] : null,
        hw.onBattery ? [h("dt", { text: "Power" }), h("dd", { text: "On battery: plug in for full speed" })] : null,
      ),
    );
    return out;
  },

  pictures: (panel) => {
    const out = [h("h3", { text: "Pictures" })];
    const im = S.images || { models: [] };
    if (!im.available) {
      out.push(h("div", { class: "notice" }, h("span", { html: icon("info", 18) }), h("div", { text: "This version of RabAI has no image engine." })));
      return out;
    }
    const ready = (im.models || []).filter((m) => !m.missingFile);
    out.push(
      row(
        "Image model",
        ready.length ? "Used when you ask for a picture" : "Add an image model (for example SD-Turbo, SDXL Turbo or FLUX) to make pictures",
        ready.length
          ? select(ready.map((m) => [m.path, m.name + (m.ready ? "" : " (needs files)")]), im.default, (v) => setDefaultImage(v).then(() => panel.replaceChildren(...PANELS.pictures(panel))), "Image model")
          : h("button", { type: "button", class: "btn btn-secondary sm", text: "Add image model", onclick: () => actions.openModelsManager?.("images") }),
      ),
      row("Make pictures from chat", "When you ask for a picture (\u201cdraw a cat in space\u201d), RabAI makes it with the image model. Chat models that can use tools also decide by themselves", toggle(() => S.settings.images.auto !== false, (v) => set((s) => (s.images.auto = v)), "Make pictures from chat")),
      row("Picture shape", "For new pictures, unless you ask for another", seg([["square", "Square"], ["portrait", "Portrait"], ["landscape", "Landscape"]], S.settings.images.size || "square", (v) => set((s) => (s.images.size = v)), "Picture shape")),
      row("Speed on the processor", "Automatic uses a tiny decoder when pictures are made without a graphics card: about 3× faster, with nearly the same look", seg([["auto", "Automatic"], ["fast", "Fastest"], ["best", "Best quality"]], S.settings.images.quality || "auto", (v) => set((s) => (s.images.quality = v)), "Speed on the processor")),
      h("div", { class: "row" }, h("div", { class: "row-text" }, h("div", { class: "row-title", text: "Image models" }), h("div", { class: "row-desc", text: "Add models, choose missing files, see how fast each one is" })), h("button", { type: "button", class: "btn btn-secondary sm", text: "Manage", onclick: () => actions.openModelsManager?.("images") })),
    );
    return out;
  },

  speech: () => {
    const synth = window.speechSynthesis;
    const voices = synth?.getVoices() || [];
    const out = [h("h3", { text: "Speech" })];
    if (!synth) {
      out.push(h("p", { class: "row-desc", text: "Read aloud isn't available on this PC." }));
      return out;
    }
    const voiceSel = select([["", "System default"], ...voices.map((v) => [v.name, `${v.name} (${v.lang})`])], S.settings.voice.name, (v) => set((s) => (s.voice.name = v)), "Voice");
    if (!voices.length) synth.addEventListener?.("voiceschanged", () => voiceSel.replaceWith(select([["", "System default"], ...synth.getVoices().map((v) => [v.name, `${v.name} (${v.lang})`])], S.settings.voice.name, (v) => set((s) => (s.voice.name = v)), "Voice")), { once: true });
    out.push(
      row("Voice", "Voices installed in Windows", voiceSel),
      row("Speed", null, slider({ min: 0.5, max: 2, step: 0.1, value: S.settings.voice.rate === 1 ? null : S.settings.voice.rate, def: 1, fmt: (v) => v.toFixed(1) + "×", label: "Speech speed", onChange: (v) => set((s) => (s.voice.rate = v ?? 1)) })),
      row("Try it", null, h("button", { type: "button", class: "btn btn-secondary sm", onclick: () => { synth.cancel(); const u = new SpeechSynthesisUtterance("Hi! This is how I sound when I read replies aloud."); const v = synth.getVoices().find((x) => x.name === S.settings.voice.name); if (v) u.voice = v; u.rate = S.settings.voice.rate || 1; synth.speak(u); } }, h("span", { html: icon("volume-2", 16) }), "Play sample")),
    );
    return out;
  },

  data: (panel) => {
    const archived = S.chats.filter((c) => c.archived);
    const importInput = h("input", { type: "file", accept: ".json,application/json", hidden: true, onchange: () => importChats(importInput.files[0]).finally(() => (importInput.value = "")) });
    return [
      h("h3", { text: "Data controls" }),
      h("div", { class: "notice" }, h("span", { html: icon("hard-drive", 18) }), h("div", { text: "Your chats, settings and models never leave this PC. RabAI makes no internet connections." })),
      row("Archived chats", archived.length ? `${archived.length} archived` : "None yet", h("button", { type: "button", class: "btn btn-secondary sm", text: "Manage", onclick: () => openArchived() })),
      row("Export all chats", "Saves every chat to one JSON file", h("button", { type: "button", class: "btn btn-secondary sm", text: "Export", onclick: exportAll })),
      row("Import chats", "From a RabAI export file", h("span", {}, h("button", { type: "button", class: "btn btn-secondary sm", text: "Import", onclick: () => importInput.click() }), importInput)),
      row("Open logs folder", "Engine and app logs, useful when something goes wrong", h("button", { type: "button", class: "btn btn-secondary sm", text: "Open", onclick: () => api.reveal("logs").catch((e) => toast(e.message)) })),
      row("Delete all chats", null, h("button", {
        type: "button",
        class: "btn btn-danger sm",
        text: "Delete all",
        onclick: async () => {
          if (await confirmDialog({ title: "Delete all chats?", message: "This permanently deletes every chat, including archived ones.", confirmLabel: "Delete all", danger: true })) {
            await actions.deleteAllChats();
            panel.replaceChildren(...PANELS.data(panel));
          }
        },
      })),
    ];
  },

  about: () => [
    h("h3", { text: "About" }),
    row("RabAI", `Version ${S.version || "1.0"}`, null),
    row("AI engine", S.engineInfo || "llama.cpp", null),
    h("p", { class: "row-desc", style: "margin-top:16px;line-height:1.6", text: "RabAI runs GGUF language models on your own computer with llama.cpp. Nothing you type is sent anywhere. It is an independent project and is not affiliated with OpenAI." }),
    h("p", { class: "row-desc", style: "line-height:1.6", text: "Open-source components: llama.cpp (MIT), marked (MIT), DOMPurify (Apache-2.0/MPL-2.0), highlight.js (BSD-3-Clause), KaTeX (MIT), Lucide icons (ISC), go-webview2 (MIT)." }),
  ],
};

async function exportAll() {
  try {
    const chats = [];
    for (const c of S.chats) chats.push(await api.chat(c.id));
    const r = await api.saveFile(`RabAI export ${new Date().toISOString().slice(0, 10)}.json`, "json", JSON.stringify({ app: "RabAI", version: 1, chats }, null, 1));
    if (!r.cancelled) toast("Exported " + chats.length + " chats");
  } catch (e) {
    toast(e.message, { kind: "error" });
  }
}

async function importChats(file) {
  if (!file) return;
  try {
    const data = JSON.parse(await file.text());
    const list = Array.isArray(data) ? data : data.chats;
    if (!Array.isArray(list)) throw new Error("This file isn't a RabAI export.");
    let n = 0;
    for (const c of list) {
      if (!c?.id || !c.messages) continue;
      await api.saveChat(c);
      n++;
    }
    await actions.reloadChats();
    toast(`Imported ${n} chat${n === 1 ? "" : "s"}`);
  } catch (e) {
    toast("Couldn't import: " + e.message, { kind: "error" });
  }
}

function openArchived() {
  openModal({
    title: "Archived chats",
    content: (body) => {
      const paint = () => {
        const list = S.chats.filter((c) => c.archived);
        body.replaceChildren(
          list.length
            ? h(
                "div",
                {},
                list.map((c) =>
                  h(
                    "div",
                    { class: "archived-row" },
                    h("span", { class: "t", text: c.title || "New chat" }),
                    h("button", { type: "button", class: "icon-btn sm", "aria-label": "Open", "data-tip": "Open", html: icon("external-link", 16), onclick: () => actions.openChat(c.id) }),
                    h("button", { type: "button", class: "icon-btn sm", "aria-label": "Unarchive", "data-tip": "Unarchive", html: icon("archive-restore", 16), onclick: async () => (await actions.setChatFlag(c.id, "archived", false), paint()) }),
                    h("button", { type: "button", class: "icon-btn sm", "aria-label": "Delete", "data-tip": "Delete", html: icon("trash-2", 16), onclick: async () => { if (await confirmDialog({ title: "Delete chat?", message: `This will delete “${c.title}”.`, confirmLabel: "Delete", danger: true })) { await actions.deleteChat(c.id); paint(); } } }),
                  ),
                ),
              )
            : h("p", { class: "row-desc", text: "You have no archived chats." }),
        );
      };
      paint();
    },
  });
}

export function openShortcuts() {
  const list = [
    ["New chat", [mod, "Shift", "O"]],
    ["Search chats", [mod, "K"]],
    ["Toggle sidebar", [mod, "Shift", "S"]],
    ["Focus the message box", ["Shift", "Esc"]],
    ["Stop generating", ["Esc"]],
    ["Copy last reply", [mod, "Shift", "C"]],
    ["Edit last message", ["↑"]],
    ["Delete chat", [mod, "Shift", "⌫"]],
    ["Settings", [mod, ","]],
    ["Keyboard shortcuts", [mod, "/"]],
  ];
  openModal({
    title: "Keyboard shortcuts",
    className: "modal-small",
    content: h("div", { class: "shortcuts" }, list.flatMap(([l, keys]) => [h("span", { text: l }), h("span", {}, keys.map((k) => h("kbd", { text: k })))])),
  });
}
