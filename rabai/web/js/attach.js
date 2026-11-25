// Reading attachments: pictures, audio, video, PDFs, Office documents and
// text. Heavy media is converted in the browser (audio to 16 kHz WAV, video
// to a few frames plus its soundtrack) and stored by the backend, so chats
// stay small and any model gets something it can use.
import { api } from "./api.js";

export const TEXT_EXT = /\.(txt|md|markdown|csv|tsv|json|jsonl|xml|html?|css|js|mjs|cjs|ts|tsx|jsx|py|rb|go|rs|java|kt|c|h|cpp|hpp|cc|cs|php|sh|bash|ps1|bat|sql|yaml|yml|toml|ini|cfg|conf|log|tex|r|swift|lua|pl|dart|scala|vue|svelte|srt|vtt|rst|org|rtf|ipynb)$/i;
export const CODE_EXT = /\.(json|xml|html?|css|js|mjs|cjs|ts|tsx|jsx|py|rb|go|rs|java|kt|c|h|cpp|hpp|cc|cs|php|sh|bash|ps1|bat|sql|yaml|yml|toml|lua|pl|dart|scala|vue|svelte|swift|r|ipynb)$/i;
const AUDIO_EXT = /\.(mp3|wav|m4a|aac|ogg|oga|opus|flac|wma|weba)$/i;
const VIDEO_EXT = /\.(mp4|m4v|webm|mov|mkv|avi|wmv|3gp)$/i;
const OFFICE_EXT = /\.(docx|xlsx|pptx|odt|ods|odp)$/i;
const MAX_TEXT = 2 * 1024 * 1024;
const MAX_DOC_CHARS = 400_000;

/** What kind of attachment a file becomes, or "" if unsupported. */
export function kindOf(f) {
  const n = f.name || "";
  if (/^image\//.test(f.type) || /\.(png|jpe?g|webp|gif|bmp)$/i.test(n)) return "image";
  if (/^audio\//.test(f.type) || AUDIO_EXT.test(n)) return "audio";
  if (/^video\//.test(f.type) || VIDEO_EXT.test(n)) return "video";
  if (f.type === "application/pdf" || /\.pdf$/i.test(n)) return "pdf";
  if (OFFICE_EXT.test(n)) return "office";
  if (TEXT_EXT.test(n) || /^text\//.test(f.type) || f.type === "application/json") return "text";
  return "";
}

class AttachError extends Error {}

/** Turns a File into an attachment object. Throws AttachError with a message for people. */
export async function readAttachment(f, { maxImageDim = 1280 } = {}) {
  const kind = kindOf(f);
  switch (kind) {
    case "image":
      return { type: "image", name: f.name || "Image", dataUrl: await shrinkImage(f, maxImageDim) };
    case "audio":
      return readAudio(f);
    case "video":
      return readVideo(f);
    case "pdf":
      return readPdf(f);
    case "office":
      return readOffice(f);
    case "text": {
      if (f.size > MAX_TEXT) throw new AttachError(`${f.name} is too large (${Math.round(f.size / 1048576)} MB). The limit for text files is 2 MB.`);
      return { type: "text", name: f.name, text: await f.text(), size: f.size, code: CODE_EXT.test(f.name) };
    }
  }
  throw new AttachError(`${f.name} can't be attached. RabAI reads pictures, audio, video, PDFs, Word, Excel and PowerPoint files, and text or code.`);
}

// ---------- pictures ----------

/** Scales a picture down (fewer image tokens, faster on the processor). */
export async function shrinkImage(file, maxDim = 1280) {
  const url = URL.createObjectURL(file);
  try {
    const img = await loadImage(url);
    return drawScaled(img, img.naturalWidth, img.naturalHeight, maxDim);
  } catch {
    throw new AttachError(`Couldn't read the picture ${file.name}.`);
  } finally {
    URL.revokeObjectURL(url);
  }
}

function loadImage(src) {
  return new Promise((res, rej) => {
    const i = new Image();
    i.onload = () => res(i);
    i.onerror = rej;
    i.src = src;
  });
}

function scaledCanvas(source, w, h, maxDim) {
  const scale = Math.min(1, maxDim / Math.max(w, h));
  const c = document.createElement("canvas");
  c.width = Math.max(1, Math.round(w * scale));
  c.height = Math.max(1, Math.round(h * scale));
  const ctx = c.getContext("2d");
  ctx.fillStyle = "#fff";
  ctx.fillRect(0, 0, c.width, c.height);
  ctx.drawImage(source, 0, 0, c.width, c.height);
  return c;
}

function drawScaled(source, w, h, maxDim, quality = 0.86) {
  return scaledCanvas(source, w, h, maxDim).toDataURL("image/jpeg", quality);
}

// ---------- audio ----------

const SAMPLE_RATE = 16000;
const MAX_AUDIO_SEC = 15 * 60;

async function decodeAudio(buf) {
  const AC = window.AudioContext || window.webkitAudioContext;
  const ctx = new AC();
  try {
    return await ctx.decodeAudioData(buf);
  } finally {
    ctx.close?.();
  }
}

/** Resamples to 16 kHz mono and encodes 16-bit WAV. */
async function toWav(audio, maxSec = MAX_AUDIO_SEC) {
  const dur = Math.min(audio.duration, maxSec);
  const frames = Math.ceil(dur * SAMPLE_RATE);
  const off = new OfflineAudioContext(1, frames, SAMPLE_RATE);
  const src = off.createBufferSource();
  src.buffer = audio;
  src.connect(off.destination);
  src.start(0);
  const out = (await off.startRendering()).getChannelData(0);
  const data = new DataView(new ArrayBuffer(44 + out.length * 2));
  const str = (o, s) => [...s].forEach((c, i) => data.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF");
  data.setUint32(4, 36 + out.length * 2, true);
  str(8, "WAVE");
  str(12, "fmt ");
  data.setUint32(16, 16, true);
  data.setUint16(20, 1, true);
  data.setUint16(22, 1, true);
  data.setUint32(24, SAMPLE_RATE, true);
  data.setUint32(28, SAMPLE_RATE * 2, true);
  data.setUint16(32, 2, true);
  data.setUint16(34, 16, true);
  str(36, "data");
  data.setUint32(40, out.length * 2, true);
  for (let i = 0; i < out.length; i++) {
    const s = Math.max(-1, Math.min(1, out[i]));
    data.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return { blob: new Blob([data.buffer], { type: "audio/wav" }), duration: dur, cut: audio.duration > maxSec };
}

async function upload(blob, ext) {
  const res = await fetch("/api/media?ext=" + ext, { method: "POST", body: blob });
  if (!res.ok) throw new AttachError("Couldn't store the attachment.");
  return (await res.json()).id;
}

async function readAudio(f) {
  let audio;
  try {
    audio = await decodeAudio(await f.arrayBuffer());
  } catch {
    throw new AttachError(`Couldn't read the audio in ${f.name}. Try MP3, WAV, M4A, OGG or FLAC.`);
  }
  const wav = await toWav(audio);
  const media = await upload(wav.blob, "wav");
  return { type: "audio", name: f.name, media, duration: wav.duration, cut: wav.cut, size: f.size };
}

// ---------- video ----------

async function readVideo(f) {
  const url = URL.createObjectURL(f);
  const v = document.createElement("video");
  v.muted = true;
  v.preload = "auto";
  v.playsInline = true;
  try {
    await new Promise((res, rej) => {
      v.onloadeddata = res;
      v.onerror = () => rej(new AttachError(`Couldn't play ${f.name}. Videos in MP4 (H.264) or WebM work best.`));
      v.src = url;
    });
    const dur = isFinite(v.duration) ? v.duration : 0;
    // A few evenly spaced frames: enough to follow what happens without
    // flooding a small model with image tokens.
    const n = Math.max(1, Math.min(8, Math.round(dur / 4) || 1));
    const frames = [];
    for (let i = 0; i < n; i++) {
      const t = dur ? Math.min(dur - 0.05, (dur * (i + 0.5)) / n) : 0;
      await seek(v, t);
      const c = scaledCanvas(v, v.videoWidth, v.videoHeight, 512);
      const blob = await new Promise((res) => c.toBlob(res, "image/jpeg", 0.8));
      if (!blob) throw new AttachError(`Couldn't read frames from ${f.name}.`);
      frames.push({ media: await upload(blob, "jpg"), t });
    }
    let audioMedia = "";
    try {
      const audio = await decodeAudio(await f.arrayBuffer());
      if (audio.duration > 0.5) audioMedia = await upload((await toWav(audio)).blob, "wav");
    } catch {
      // No soundtrack, or one the browser can't decode.
    }
    return { type: "video", name: f.name, duration: dur, frames, audioMedia, size: f.size };
  } finally {
    URL.revokeObjectURL(url);
    v.removeAttribute("src");
    v.load();
  }
}

function seek(v, t) {
  return new Promise((res) => {
    const done = () => {
      v.removeEventListener("seeked", done);
      res();
    };
    v.addEventListener("seeked", done);
    v.currentTime = t;
    setTimeout(done, 3000);
  });
}

// ---------- PDF ----------

let pdfjs = null;
async function loadPdfJs() {
  if (!pdfjs) {
    pdfjs = await import("../vendor/pdfjs/pdf.min.mjs");
    pdfjs.GlobalWorkerOptions.workerSrc = new URL("../vendor/pdfjs/pdf.worker.min.mjs", import.meta.url).href;
  }
  return pdfjs;
}

async function readPdf(f) {
  const lib = await loadPdfJs();
  let doc;
  try {
    doc = await lib.getDocument({ data: new Uint8Array(await f.arrayBuffer()), isEvalSupported: false }).promise;
  } catch (e) {
    if (/password/i.test(e?.name + e?.message)) throw new AttachError(`${f.name} is password-protected.`);
    throw new AttachError(`Couldn't read ${f.name}. The PDF may be damaged.`);
  }
  const pages = [];
  let chars = 0;
  for (let i = 1; i <= doc.numPages && chars < MAX_DOC_CHARS; i++) {
    const page = await doc.getPage(i);
    const tc = await page.getTextContent();
    let text = "";
    for (const it of tc.items) {
      text += it.str + (it.hasEOL ? "\n" : " ");
    }
    text = text.replace(/[ \t]+\n/g, "\n").replace(/[ \t]{2,}/g, " ").trim();
    pages.push(`--- Page ${i} ---\n${text}`);
    chars += text.length;
  }
  const text = pages.join("\n\n").slice(0, MAX_DOC_CHARS);
  if (text.replace(/--- Page \d+ ---/g, "").trim().length < 20) {
    throw new AttachError(`${f.name} has no selectable text (it may be scanned pages). Attach the pages as pictures for a vision model instead.`);
  }
  return { type: "text", name: f.name, text, size: f.size, doc: "pdf", pages: doc.numPages, cut: chars >= MAX_DOC_CHARS };
}

// ---------- Office documents (DOCX, XLSX, PPTX, ODF) ----------

/** Reads the files of a ZIP archive (stored or deflated). */
async function unzip(buf) {
  const dv = new DataView(buf);
  let eocd = -1;
  for (let i = buf.byteLength - 22; i >= Math.max(0, buf.byteLength - 66000); i--) {
    if (dv.getUint32(i, true) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) throw new AttachError("This file isn't a valid document.");
  const count = dv.getUint16(eocd + 10, true);
  let p = dv.getUint32(eocd + 16, true);
  const files = new Map();
  const dec = new TextDecoder();
  for (let i = 0; i < count; i++) {
    if (dv.getUint32(p, true) !== 0x02014b50) break;
    const method = dv.getUint16(p + 10, true);
    const csize = dv.getUint32(p + 20, true);
    const nlen = dv.getUint16(p + 28, true);
    const xlen = dv.getUint16(p + 30, true);
    const clen = dv.getUint16(p + 32, true);
    const local = dv.getUint32(p + 42, true);
    const name = dec.decode(new Uint8Array(buf, p + 46, nlen));
    p += 46 + nlen + xlen + clen;
    files.set(name, async () => {
      const lnlen = dv.getUint16(local + 26, true);
      const lxlen = dv.getUint16(local + 28, true);
      const data = new Uint8Array(buf, local + 30 + lnlen + lxlen, csize);
      if (method === 0) return dec.decode(data);
      if (method !== 8) throw new AttachError("This document uses an unsupported compression.");
      const stream = new Blob([data]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
      return await new Response(stream).text();
    });
  }
  return files;
}

function xmlText(xml, { para = /<\/w:p>|<\/a:p>|<\/text:p>|<\/text:h>/g } = {}) {
  return xml
    .replace(para, "\n")
    .replace(/<w:tab\/>|<text:tab\/>/g, "\t")
    .replace(/<w:br\/>|<a:br\/>|<text:line-break\/>/g, "\n")
    .replace(/<[^>]+>/g, "")
    .replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, "&")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

async function readOffice(f) {
  const files = await unzip(await f.arrayBuffer());
  const get = async (n) => (files.has(n) ? files.get(n)() : "");
  const ext = f.name.split(".").pop().toLowerCase();
  let text = "";
  if (ext === "docx") {
    text = xmlText(await get("word/document.xml"));
  } else if (ext === "pptx") {
    const slides = [...files.keys()].filter((k) => /^ppt\/slides\/slide\d+\.xml$/.test(k)).sort((a, b) => parseInt(a.match(/\d+/)) - parseInt(b.match(/\d+/)));
    const parts = [];
    for (const [i, s] of slides.entries()) parts.push(`--- Slide ${i + 1} ---\n` + xmlText(await get(s)));
    text = parts.join("\n\n");
  } else if (ext === "xlsx") {
    const shared = [...(await get("xl/sharedStrings.xml")).matchAll(/<si>([\s\S]*?)<\/si>/g)].map((m) => xmlText(m[1]));
    const sheets = [...files.keys()].filter((k) => /^xl\/worksheets\/sheet\d+\.xml$/.test(k)).sort((a, b) => parseInt(a.match(/\d+/)) - parseInt(b.match(/\d+/)));
    const parts = [];
    for (const [i, s] of sheets.entries()) {
      const xml = await get(s);
      const rows = [...xml.matchAll(/<row[^>]*>([\s\S]*?)<\/row>/g)].map((r) =>
        [...r[1].matchAll(/<c([^>]*?)(?:\/>|>([\s\S]*?)<\/c>)/g)].map((c) => {
          const v = (c[2] || "").match(/<v>([\s\S]*?)<\/v>/)?.[1] ?? xmlText(c[2] || "");
          return /t="s"/.test(c[1]) ? shared[+v] ?? "" : v;
        }).join("\t"),
      );
      parts.push(`--- Sheet ${i + 1} ---\n` + rows.join("\n"));
    }
    text = parts.join("\n\n");
  } else {
    text = xmlText(await get("content.xml"));
  }
  if (!text.trim()) throw new AttachError(`${f.name} doesn't contain any text RabAI can read.`);
  const cut = text.length > MAX_DOC_CHARS;
  return { type: "text", name: f.name, text: text.slice(0, MAX_DOC_CHARS), size: f.size, doc: ext, cut };
}

// ---------- sending ----------

const dataCache = new Map();

/** Returns a media file as a data URL (cached). */
export async function mediaDataUrl(id) {
  if (dataCache.has(id)) return dataCache.get(id);
  const blob = await (await fetch("/api/media/" + id)).blob();
  const url = await new Promise((res) => {
    const r = new FileReader();
    r.onload = () => res(r.result);
    r.readAsDataURL(blob);
  });
  if (dataCache.size > 40) dataCache.delete(dataCache.keys().next().value);
  dataCache.set(id, url);
  return url;
}

export function mediaUrl(id) {
  return "/api/media/" + id;
}

export function isAttachError(e) {
  return e instanceof AttachError;
}

export const uploadMedia = upload;
