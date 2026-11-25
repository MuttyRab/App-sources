// Talks to the LocalChat backend and (through its proxy) to the engine.

async function request(method, url, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = typeof body === "string" ? body : JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  let data = null;
  const text = await res.text();
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { raw: text };
  }
  if (!res.ok) {
    const msg = data?.error?.message || data?.raw || `Request failed (${res.status})`;
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return data;
}

export const api = {
  state: () => request("GET", "/api/state"),
  settings: () => request("GET", "/api/settings"),
  saveSettings: (s) => request("PUT", "/api/settings", s),
  models: () => request("GET", "/api/models"),
  pickModel: () => request("POST", "/api/models/pick"),
  inspect: (path) => request("POST", "/api/models/inspect", { path }),
  load: (path, prefs) => request("POST", "/api/models/load", { path, prefs }),
  unload: () => request("POST", "/api/models/unload"),
  forget: (path) => request("POST", "/api/models/forget", { path }),
  scan: () => request("POST", "/api/models/scan"),
  addFolder: () => request("POST", "/api/models/add-folder"),
  learn: (path, tps, n) => request("POST", "/api/models/learn", { path, tps, n }).catch(() => {}),
  chats: () => request("GET", "/api/chats"),
  searchChats: (q) => request("GET", "/api/chats/search?q=" + encodeURIComponent(q)),
  chat: (id) => request("GET", "/api/chats/" + encodeURIComponent(id)),
  saveChat: (c) => request("PUT", "/api/chats/" + encodeURIComponent(c.id), c),
  deleteChat: (id) => request("DELETE", "/api/chats/" + encodeURIComponent(id)),
  deleteAllChats: () => request("POST", "/api/chats/delete-all"),
  openURL: (url) => request("POST", "/api/native/open-url", { url }),
  reveal: (path) => request("POST", "/api/native/reveal", { path }),
  theme: (dark, bg) => request("POST", "/api/native/theme", { dark, bg }).catch(() => {}),
  saveFile: (name, ext, content) => request("POST", "/api/native/save", { name, ext, content }),
  props: () => request("GET", "/props"),
  countTokens: (body) => request("POST", "/v1/chat/completions/input_tokens", body),
  complete: (body, signal) =>
    fetch("/v1/chat/completions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal }),
  control: (id, action) => request("POST", "/v1/chat/completions/control", { id, action }),
};

/** Subscribes to backend events with automatic reconnection. */
export function subscribeEvents(onEvent, onStatus) {
  let es;
  let retry = 500;
  const connect = () => {
    es = new EventSource("/api/events");
    es.onopen = () => {
      retry = 500;
      onStatus?.(true);
    };
    es.onmessage = (e) => {
      try {
        onEvent(JSON.parse(e.data));
      } catch (err) {
        console.error(err);
      }
    };
    es.onerror = () => {
      es.close();
      onStatus?.(false);
      setTimeout(connect, retry);
      retry = Math.min(retry * 2, 5000);
    };
  };
  connect();
  return () => es?.close();
}

/**
 * Streams a chat completion. Calls handlers.onDelta({content, reasoning}),
 * onProgress(prompt_progress), onId(id) and resolves with the final
 * {timings, usage, finishReason}. Throws on HTTP or engine errors.
 */
export async function streamChat(body, signal, handlers) {
  const res = await api.complete({ ...body, stream: true }, signal);
  if (!res.ok) {
    let msg = `The engine returned an error (${res.status}).`;
    try {
      const j = await res.json();
      msg = j?.error?.message || msg;
    } catch {}
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  const result = { timings: null, usage: null, finishReason: null };
  let gotId = false;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, idx).replace(/\r$/, "");
      buf = buf.slice(idx + 1);
      if (!line.startsWith("data:")) continue;
      const data = line.slice(5).trim();
      if (data === "[DONE]") continue;
      let chunk;
      try {
        chunk = JSON.parse(data);
      } catch {
        continue;
      }
      if (chunk.error) {
        const err = new Error(chunk.error.message || "The engine reported an error.");
        err.engine = true;
        throw err;
      }
      if (!gotId && chunk.id) {
        gotId = true;
        handlers.onId?.(chunk.id);
      }
      if (chunk.prompt_progress) handlers.onProgress?.(chunk.prompt_progress);
      const ch = chunk.choices?.[0];
      if (ch) {
        const d = ch.delta || {};
        if (d.content || d.reasoning_content) handlers.onDelta?.({ content: d.content || "", reasoning: d.reasoning_content || "" });
        if (ch.finish_reason) result.finishReason = ch.finish_reason;
      }
      if (chunk.timings) result.timings = chunk.timings;
      if (chunk.usage) result.usage = chunk.usage;
    }
  }
  return result;
}
