# LocalChat: a ChatGPT-style app for GGUF models on Windows

Date: 2026-11-25

## Intent

The owner has an HP laptop running Windows 11 with no strong graphics card and some
GGUF models already downloaded. They want one `.exe` they can double-click, pick a
`.gguf`, and chat with it in an interface that looks and behaves like ChatGPT, is
feature complete, and stays smooth on older hardware. The app should adapt itself to
whichever model is loaded and to the PC it runs on.

## Goals

- A single Windows x64 `LocalChat.exe`: no installer, no Python, no admin rights.
- Pick any `.gguf` with the native Windows file dialog, by drag and drop, from a
  recent-models list, or from a scan of common model folders.
- A ChatGPT-faithful interface: sidebar with history and search, model menu in the
  header, centered composer, streaming Markdown answers, code blocks with copy,
  math, tables, "thinking" sections, regenerate, edit-and-resend with version arrows,
  branch to a new chat, read aloud, temporary chats, pin/rename/archive/delete,
  export, custom instructions, light/dark/system themes, keyboard shortcuts.
- Model-aware behavior: read each GGUF's metadata and size the context, KV cache
  type, threads, batch sizes and prompt cache to the model and the PC's RAM/CPU;
  show "fits / tight / too big" and an expected-speed rating before loading; enable
  thinking controls only for reasoning models, image input only when a vision
  projector is present, and recommended sampling per model family.
- Smooth on old hardware: CPU-first engine with runtime CPU-feature dispatch
  (SSE4.2 through AVX-512), physical-core thread counts, memory-mapped loading, and a
  lightweight UI (no framework, throttled rendering, no blur effects).
- Everything stays on the PC. No network calls by the app.

## Non-goals

- Downloading models from the internet inside the app (links to Hugging Face search
  pages are fine; they open in the browser).
- Tool use / agents / web search, voice conversation mode, accounts or sync.
- GPU acceleration as a default. (Optional Vulkan support is a stretch goal; the
  owner's GPU is weak, so CPU is the baseline.)

## Anti-goals

- Copying OpenAI branding (logo, the word ChatGPT in the UI). The look is matched;
  the name and mark are original.
- Anything that makes an 8 GB laptop swap: no Electron, no unbounded caches, no
  context sizes picked from the model's training maximum.
- Orphaned engine processes holding gigabytes of RAM after the window closes.

## Constraints found during research

- GitHub release downloads are blocked from the build environment, so the engine is
  compiled from llama.cpp source (tag `v1.0.0`, Nov 2026) with MinGW-w64 (GCC 13,
  posix threads). Ubuntu's MinGW headers lack `THREAD_POWER_THROTTLING_STATE`; a
  small forced-include shim restores llama.cpp's Windows 11 anti-core-parking code.
- llama.cpp's Windows physical-core detection is compiled out under MinGW, so the app
  must pass `-t`/`-tb` itself.
- `--fit` treats host RAM as unlimited, so on CPU it would choose the model's full
  training context. The app must choose `-c` from RAM itself.
- WebView2 ships with Windows 11. `github.com/jchv/go-webview2` is pure Go (no cgo),
  so the whole app cross-compiles from Linux.

## Approaches considered

1. **Go + WebView2 window + bundled llama-server (chosen).** Small, native window,
   uses the Edge engine already on Windows 11, ~30 MB RAM for the shell. The engine
   runs as a child process so a crash in a model never takes down the UI, and
   switching models is a clean restart.
2. Electron + node-llama-cpp. Familiar stack but 150 MB+ download and 300 MB+ RAM
   before a model loads; directly works against "smooth on older hardware".
3. Python + PyInstaller. Cannot be built for Windows from this environment, slow
   start, large bundle, antivirus false positives are common.

## Architecture

```
LocalChat.exe (Go, -H windowsgui)
 ├─ WebView2 window  ──HTTP──▶  embedded web UI (HTML/CSS/vanilla JS)
 ├─ HTTP server 127.0.0.1:<random>  (token cookie + Host check)
 │    ├─ /api/*      app state, models, chats, settings, dialogs (Go)
 │    ├─ /api/events server-sent events (engine state, load progress)
 │    └─ /v1/*, /props …  reverse proxy ──▶ llama-server (child)
 └─ engine manager
      ├─ extracts embedded engine zip to %LOCALAPPDATA%\LocalChat\engine\<hash>
      ├─ starts llama-server.exe with a per-model profile, hidden console,
      │  inside a kill-on-close Job Object, stdin pipe (child exits on EOF)
      └─ reads load progress / ready / memory report from the child's stdout
         (llama-server's router-child protocol, enabled by LLAMA_SERVER_ROUTER_PORT)
```

Fallback when WebView2 is unavailable: open the UI in Edge `--app` mode, then the
default browser.

### Data on disk

- `%APPDATA%\LocalChat\settings.json` — UI and engine settings.
- `%APPDATA%\LocalChat\chats\<id>.json` — one file per chat, written atomically.
- `%APPDATA%\LocalChat\models.json` — known models, metadata cache, per-model
  overrides, measured speed and measured KV bytes per token.
- `%LOCALAPPDATA%\LocalChat\` — engine files, WebView2 profile, logs.

### Chat format

Each chat stores a message tree (`id`, `parent`, `children`, `role`, `content`,
`reasoning`, attachments, stats, timestamps) plus the active leaf. Regenerate adds an
assistant sibling; editing a user message adds a user sibling; version arrows switch
siblings; "Branch in new chat" copies the active path.

## Model-aware profile

Inputs: GGUF metadata (architecture, size label, file type, layers, KV heads,
key/value lengths, sliding window, expert counts, training context, chat template
text, `general.sampling.*`, `general.type`), file size, sibling `mmproj` file, and
hardware (total/available RAM, physical and performance cores, CPU features).

- Memory: usable = total RAM − max(2 GB, 25%). KV budget = usable − model size −
  compute overhead. KV bytes/token from metadata (later replaced by the engine's own
  measured value). Context = largest of a ladder (2k…32k, capped by training
  context and the speed preset) that fits; switch KV cache to `q8_0` when `f16`
  would give less than the target. Reasoning models target 16k, others 8k.
- Fit rating: fits / tight / too big; "too big" still loads with a warning.
- Speed estimate: memory bandwidth ÷ bytes read per token (active parameters for
  MoE). Starts at a conservative 12 GB/s and self-calibrates from real runs.
- Threads: performance physical cores for generation, all physical cores for prompt
  processing. Batch/ubatch shrink on low-RAM machines. Prompt cache RAM (`-cram`)
  sized from leftover memory (0 on tight machines).
- Features: Think toggle, effort levels and "Answer now" for templates with reasoning
  support; image input when a vision projector is next to the model (with a lower
  image token cap on slow CPUs); sampling from GGUF metadata, else a small table of
  model-card recommendations (Qwen3, Gemma 3, Llama 3, DeepSeek R1 distills,
  gpt-oss, Mistral Small); else engine defaults. User overrides always win.
- Titles: model-written titles when the model is fast enough and prompt cache RAM is
  available; otherwise a cleaned-up first message.

## Interface

ChatGPT's layout and tokens (light: white canvas, #f9f9f9 sidebar, #f4f4f4 user
bubble; dark: #212121 canvas, #181818 sidebar, #303030 composer and bubble), system
UI font (Segoe UI Variable on Windows 11), 48rem reading column, 28px-radius
composer, black/white circular send button, Lucide-style 20px line icons. Windows 11
title bar colored to match the theme. Respect reduced motion; visible focus rings;
full keyboard operation.

## What's left to the builder

Exact copy, icon choices, animation timings, error-message wording, and the
internal module split, within the constraints above.
