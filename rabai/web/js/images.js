// Making pictures in chat: image models, detecting picture requests, running
// jobs on the image engine and showing them like ChatGPT does.
import { S, emit, on } from "./state.js";
import { api } from "./api.js";
import { h, formatDuration } from "./util.js";
import { icon } from "./icons.js";
import { toast, openMenu } from "./ui.js";
import { mediaUrl } from "./attach.js";

export async function refreshImages() {
  try {
    const r = await api.images();
    S.images = { available: r.available, models: r.models || [], default: r.default || "", running: r.running || "" };
  } catch {
    S.images = { available: false, models: [], default: "" };
  }
  emit("images");
  return S.images;
}

/** The image model chats use, or null. */
export function defaultImageModel() {
  const im = S.images;
  if (!im?.models?.length) return null;
  return im.models.find((m) => m.path === im.default) || null;
}

/** True when pictures can be made right now. */
export function imagesReady() {
  const m = defaultImageModel();
  return !!(S.images?.available && m && m.ready);
}

export async function setDefaultImage(path) {
  await api.imageAction("default", path);
  await refreshImages();
}

// ---------- recognising picture requests ----------

const VERB = String.raw`(?:draw|paint|sketch|illustrate|render|generate|create|make|design|produce|imagine|show\s+me|give\s+me|i\s+(?:want|need|would\s+like)|i'd\s+like)`;
const NOUN = String.raw`(?:image|picture|pic|photo|photograph|illustration|drawing|painting|artwork|art|logo|icon|wallpaper|portrait|sketch|poster|render|cartoon|anime|meme|sticker|banner|thumbnail|avatar|scene|landscape|comic)`;
const REQUEST = new RegExp(String.raw`(?:^|\b)(?:${VERB})\b[^.?!\n]{0,60}?\b(?:an?\s+|the\s+|some\s+|me\s+(?:an?\s+)?)?(?:\w+\s+){0,3}?${NOUN}s?\b`, "i");
const LEADING = new RegExp(String.raw`^\s*(?:please\s+)?(?:can|could|would|will)?\s*(?:you\s+)?(?:please\s+)?(?:draw|paint|sketch|illustrate|render)\b`, "i");
const NOT_PICTURE = /\b(how (?:do|can|to|would)|explain|what is|what's|why|(?:write|make|create|generate) (?:a |an |the |some )?(?:code|function|program|script|class|table|list|chart|graph|plot|diagram in)|in python|javascript|css|svg code|ascii|matplotlib|plot (?:in|with)|chart of|table of|spreadsheet|regex)\b/i;

/** Guesses whether a message asks for a picture. */
export function isImageRequest(text) {
  const t = (text || "").trim();
  if (!t || t.length > 600) return false;
  if (NOT_PICTURE.test(t)) return false;
  return LEADING.test(t) || REQUEST.test(t);
}

/** Turns "Can you draw me a cat wearing a hat?" into "a cat wearing a hat". */
export function promptFromRequest(text) {
  let t = (text || "").trim();
  t = t.replace(/^\s*(?:hey|hi|ok|okay)[,!]?\s+/i, "");
  t = t.replace(/^\s*(?:please\s+)?(?:can|could|would|will)\s+you\s+(?:please\s+)?/i, "");
  t = t.replace(/^\s*please\s+/i, "");
  t = t.replace(new RegExp(String.raw`^(?:${VERB})\s+(?:(?:to\s+)?(?:see\s+|have\s+|get\s+)?)?(?:me\s+|us\s+)?`, "i"), "");
  // "a picture of a cat" → "a cat"; "a logo for my bakery" keeps its noun.
  // "a picture of a cat" → "a cat". Style words ("a watercolor painting of…",
  // "a realistic photo of…") are kept: they matter to the image model.
  t = t.replace(/^(?:(?:an?|the|some)\s+)?(?:image|picture|pic)s?\s+(?:of|showing|depicting|that shows)\s+/i, "");
  t = t.replace(/\s*(?:please|thanks|thank you)[.!]*\s*$/i, "").replace(/[?!.]+$/, "").trim();
  return t || (text || "").trim();
}

/** Words that mean "change the last picture" rather than "make a new one". */
export function looksLikeFollowUp(text) {
  return /\b(it|that|this one|the same|again|instead|now make|make it|change|more|less|but|another version|redo|try again)\b/i.test(text || "") && (text || "").length < 160;
}

export const SIZES = [
  { id: "square", label: "Square", hint: "1:1", icon: "square" },
  { id: "portrait", label: "Portrait", hint: "2:3", icon: "rectangle-vertical" },
  { id: "landscape", label: "Landscape", hint: "3:2", icon: "rectangle-horizontal" },
];

/** Guesses a picture shape from words in the request. */
export function sizeFromText(text, fallback = "square") {
  const t = (text || "").toLowerCase();
  if (/\b(portrait|vertical|tall|phone wallpaper|poster|9:16|2:3|3:4)\b/.test(t)) return "portrait";
  if (/\b(landscape|wide|horizontal|banner|desktop wallpaper|panorama|16:9|3:2|4:3|thumbnail)\b/.test(t)) return "landscape";
  return fallback;
}

// ---------- the image tool for chat models ----------

export const IMAGE_TOOL = {
  type: "function",
  function: {
    name: "generate_image",
    description:
      "Create a picture with the local image model and show it to the user. Call this whenever the user asks you to draw, paint, generate, create, make, design or show an image, photo, picture, illustration, logo, icon, poster, wallpaper or artwork, including changes to a picture you made earlier.",
    parameters: {
      type: "object",
      properties: {
        prompt: { type: "string", description: "A detailed visual description of the whole picture in English: subject, setting, style, lighting, colors and composition. For a change to an earlier picture, describe the complete new picture." },
        size: { type: "string", enum: ["square", "portrait", "landscape"], description: "Picture shape." },
      },
      required: ["prompt"],
    },
  },
};

/** Parses a generate_image call's arguments. */
export function parseImageCall(call) {
  if (!call || call.name !== "generate_image") return null;
  try {
    const a = JSON.parse(call.arguments || "{}");
    const prompt = typeof a.prompt === "string" ? a.prompt.trim() : "";
    if (!prompt) return null;
    return { prompt, size: ["square", "portrait", "landscape"].includes(a.size) ? a.size : "square" };
  } catch {
    return null;
  }
}

// ---------- jobs ----------

const jobs = new Map(); // jobId -> handler

on("image-event", (ev) => jobs.get(ev.jobId)?.(ev));

/**
 * Runs a picture job and keeps m.image up to date. onUpdate is called after
 * every change. Resolves when the job ends; never throws.
 */
export function runImageJob(m, req, onUpdate, signal) {
  return new Promise((resolve) => {
    let jobId = null;
    let ended = false;
    const img = (m.image = { status: "running", stage: "preparing", prompt: req.prompt, size: req.size || "square", fraction: 0, started: Date.now(), model: defaultImageModel()?.name || "" });
    const finish = () => {
      if (ended) return;
      ended = true;
      if (jobId) jobs.delete(jobId);
      signal?.removeEventListener("abort", onAbort);
      delete img.preview;
      onUpdate();
      resolve(img);
    };
    const onAbort = () => {
      if (jobId) api.cancelImage(jobId);
      img.status = "cancelled";
      finish();
    };
    signal?.addEventListener("abort", onAbort);
    api
      .generateImage({ prompt: req.prompt, size: req.size, init: req.init, refs: req.refs, strength: req.strength, seed: req.seed })
      .then((r) => {
        if (ended) {
          api.cancelImage(r.jobId);
          return;
        }
        jobId = r.jobId;
        img.model = r.model || img.model;
        jobs.set(jobId, (ev) => {
          if (ended) return;
          if (ev.status === "running") {
            img.stage = ev.stage;
            img.detail = ev.detail || "";
            img.fraction = ev.fraction || 0;
            img.step = ev.step;
            img.steps = ev.steps;
            if (ev.preview) img.preview = ev.preview;
            if (ev.device) img.device = ev.device;
            onUpdate();
          } else if (ev.status === "done") {
            Object.assign(img, { status: "done", media: ev.media, width: ev.width, height: ev.height, seconds: ev.seconds, seed: ev.seed, model: ev.model || img.model, device: ev.device || "" });
            finish();
          } else if (ev.status === "cancelled") {
            img.status = "cancelled";
            finish();
          } else if (ev.status === "error") {
            Object.assign(img, { status: "error", error: ev.error || "The picture couldn't be made.", errorDetail: ev.errorDetail || "" });
            finish();
          }
        });
        if (signal?.aborted) onAbort();
      })
      .catch((e) => {
        Object.assign(img, { status: "error", error: e.message || "The picture couldn't be made." });
        finish();
      });
  });
}

// ---------- the picture card ----------

function stageText(img) {
  if (img.stage === "tuning") return img.detail || "Testing your graphics card";
  if (img.stage === "sampling" && img.steps) return `Creating image · step ${img.step} of ${img.steps}`;
  if (img.stage === "decoding") return "Finishing up";
  if (img.stage === "saving") return "Saving";
  if (img.stage === "conditioning") return "Reading your prompt";
  if (img.stage === "loading" || img.stage === "preparing") return "Loading the image model";
  return "Creating image";
}

function aspect(img) {
  if (img.width && img.height) return `${img.width} / ${img.height}`;
  return { portrait: "2 / 3", landscape: "3 / 2" }[img.size] || "1 / 1";
}

/** Builds the picture card for an assistant message. */
export function imageCard(m, handlers) {
  const img = m.image;
  if (!img) return null;
  if (img.status === "running") {
    const secs = Math.round((Date.now() - (img.started || Date.now())) / 1000);
    const pct = Math.round((img.fraction || 0) * 100);
    const card = h(
      "div",
      { class: "gen-card running", style: `aspect-ratio:${aspect(img)}`, role: "status", "aria-label": stageText(img) },
      img.preview ? h("img", { class: "gen-preview", src: img.preview, alt: "", style: `filter: blur(${Math.max(0, 14 - 12 * (img.fraction || 0))}px)` }) : h("div", { class: "gen-shimmer" }),
      h(
        "div",
        { class: "gen-overlay" },
        h("span", { class: "shimmer gen-stage", text: stageText(img) }),
        h("span", { class: "gen-meta", text: [pct && img.stage === "sampling" ? pct + "%" : "", secs >= 2 ? formatDuration(secs * 1000) : "", img.device ? "GPU" : ""].filter(Boolean).join(" · ") }),
      ),
    );
    return card;
  }
  if (img.status === "done" && img.media) {
    const src = mediaUrl(img.media);
    const pic = h("img", { class: "gen-image", src, alt: img.prompt || "Generated picture", width: img.width, height: img.height, loading: "lazy", onclick: () => emit("lightbox", src) });
    const tools = h(
      "div",
      { class: "gen-tools" },
      h("button", { type: "button", class: "gen-tool", "aria-label": "Download picture", "data-tip": "Download", html: icon("download", 18), onclick: () => handlers.download?.(m) }),
      h("button", { type: "button", class: "gen-tool", "aria-label": "Open picture", "data-tip": "Open", html: icon("maximize-2", 18), onclick: () => emit("lightbox", src) }),
    );
    return h("figure", { class: "gen-done" }, h("div", { class: "gen-frame", style: `aspect-ratio:${aspect(img)}` }, pic, tools));
  }
  if (img.status === "cancelled") {
    return h("div", { class: "notice" }, h("span", { html: icon("info", 18) }), h("div", { text: "You stopped this picture." }));
  }
  if (img.status === "error") {
    return h(
      "div",
      { class: "notice error", role: "alert" },
      h("span", { html: icon("circle-alert", 18) }),
      h(
        "div",
        {},
        h("div", { text: img.error }),
        h(
          "div",
          { class: "notice-actions" },
          h("button", { type: "button", class: "btn btn-secondary sm", text: "Try again", onclick: () => handlers.retry?.(m) }),
          img.errorDetail ? h("button", { type: "button", class: "btn btn-ghost sm", text: "Show details", onclick: () => handlers.details?.(img.errorDetail) }) : null,
          /needs:|image model is set up|no image engine/i.test(img.error) ? h("button", { type: "button", class: "btn btn-ghost sm", text: "Image models", onclick: () => emit("open-image-models") }) : null,
        ),
      ),
    );
  }
  return null;
}

/** A caption under a finished picture. */
export function imageCaption(img) {
  if (img?.status !== "done") return "";
  const parts = [];
  if (img.seconds) parts.push(`Made in ${formatDuration(img.seconds * 1000)}`);
  if (img.model) parts.push(img.model);
  if (img.device) parts.push("graphics card");
  return parts.join(" · ");
}

/** Model menu rows for picking the image model. */
export function imageModelMenuItems(onPick) {
  const im = S.images;
  if (!im?.models?.length) return [];
  return im.models
    .filter((m) => !m.missingFile)
    .map((m) => ({
      label: m.name,
      hint: [m.kind, m.ready ? "" : "needs files"].filter(Boolean).join(" · "),
      checked: m.path === im.default,
      onClick: () => onPick(m),
    }));
}

export { openMenu, toast };
