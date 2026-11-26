# RabAI

A ChatGPT-style app for Windows that runs language models and image models entirely
on your PC. One file, no installer, no internet connection, no account.

![RabAI](web/img/app-icon.png)

## Getting started

1. Download `RabAI.exe` (in `release/`) and double-click it. No install needed.
   - Windows SmartScreen may say "Windows protected your PC" because the app isn't
     code-signed. Click **More info → Run anyway**.
2. The first time, RabAI looks for `.gguf` files in your Downloads, Desktop,
   Documents and LM Studio / Ollama folders. Pick one of the models it finds, or click
   **Add model files** and select one or more `.gguf` files at once.
3. Start chatting. Next time, RabAI reloads your last model automatically.

Needs Windows 10 or 11 (64-bit). Windows 11 already includes the WebView2 component
RabAI draws its window with; if it is missing, RabAI opens in an Edge window
instead.

## Using several models

Add as many GGUF models as you like: **Add model files** accepts several files at once,
and **Models** in the sidebar lets you add more, show them in File Explorer, remove them
from the list, or add folders for RabAI to watch.

Switch models from the dropdown at the top left, just like ChatGPT, even in the middle
of a chat. The new model reads the whole conversation so far and carries on from there.
If you send a message while the new model is still loading, RabAI shows "Loading…" and
sends it as soon as the model is ready. When a chat mixes models, each reply is labeled
with the model that wrote it, and **Try again** (↻) can redo a reply with a different
model.

Models load one at a time so they don't compete for your RAM; switching takes a few
seconds (faster for models you've used recently).

## Making pictures

Add an image model with **Add model files** (the same button as chat models): a
Stable Diffusion 1.x/2.x, SDXL, SD3, FLUX, Z-Image, Qwen Image or other model that
[stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) supports, as
`.gguf` or `.safetensors`. Models that come in several files (a text encoder, a VAE…)
are paired automatically when the files are in the same folder or the usual
`text_encoders`, `vae` and `clip` folders next to it; otherwise **Models → Image models**
says what's missing and lets you choose the file. Your chat models can double as
text encoders (for example Qwen3 4B for Z-Image).

Then just ask: "draw a cat astronaut", "make a logo for my bakery". The picture appears
in the chat with a live preview while it's made. You can also turn on **+ → Create
image** to describe pictures directly and pick a shape (square, portrait, landscape).
Under each picture: download, copy the prompt, **edit** (change the picture with a new
description) and **make another**. Chat models that can use tools (Qwen3, Llama 3.x,
Mistral…) also decide on their own when a picture would answer you.

Good image models for older laptops:

| Model | Size | Without a graphics card | Download |
| --- | --- | --- | --- |
| SD-Turbo (Q8_0) | 2.1 GB | fastest: about 10–20 s per picture, 512×512 | [sd_turbo_q8_0.gguf](https://huggingface.co/samoudi/sd-turbo-gguf/resolve/main/sd_turbo_q8_0.gguf) |
| DreamShaper 8 LCM (Q8_0) | 1.8 GB | about 1–2 min, nicer art and photos | [DreamShaper8_LCM_q8_0.gguf](https://huggingface.co/haven-ai-companion/dreamshaper8-lcm-gguf/resolve/main/DreamShaper8_LCM_q8_0.gguf) |
| DreamShaper XL Turbo (Q4_K) | 2.8 GB | best quality, a few minutes (much faster if your graphics card helps) | [dreamshaper-xl-v2-turbo-Q4_K_S.gguf](https://huggingface.co/off-grid-ai/dreamshaper-xl-v2-turbo-GGUF/resolve/main/dreamshaper-xl-v2-turbo-Q4_K_S.gguf) |

Times are from a 4-core laptop-class processor; RabAI picks the right settings for each
(steps, guidance, sampler) from the model file.

On the processor RabAI uses a tiny decoder (TAESD) for these, which makes pictures about
three times faster with nearly the same look (**Settings → Pictures** to change it). If
both models don't fit in memory together, the chat model pauses while a picture is made
and comes back afterwards.

## Graphics cards

RabAI includes a Vulkan GPU engine that works with the normal NVIDIA, AMD and Intel
drivers (no CUDA needed). Graphics cards in laptops are often not faster than the
processor, and old drivers can compute wrong results, so nothing is assumed: the first
time you load a model, RabAI runs it briefly on the graphics card and on the processor
("Testing your graphics card"), compares the speed **and** checks the answers match,
and keeps whichever is better for that model. If the card gives wrong results it tries
a safe mode without the features old drivers get wrong, and otherwise uses the processor.
The GPU library is only loaded for models that use the card, so a broken driver can't
affect anything else. Image models are tested the same way with a small test picture.

**Settings → Model & performance → Graphics card** can force it on or off, and **Test
again** re-measures after a driver update.

## Attachments

The **+** button and drag-and-drop accept:

- **Pictures** for vision models (a model with its `mmproj` file next to it)
- **PDFs** (text is extracted), **Word**, **Excel**, **PowerPoint** and OpenDocument files,
  and any text or code file
- **Audio** (MP3, WAV, M4A, OGG, FLAC…) for models that understand speech and sound
  (for example Qwen2.5-Omni, Voxtral, Ultravox, Gemma 3n, each with its `mmproj` file)
- **Video**: RabAI takes up to eight frames for vision models and the soundtrack for
  audio models

When the current model can't use a kind of file, RabAI says so and tells the model that
something was attached, so the answer makes sense.

## Which model should I use?

On an older laptop without a strong graphics card, smaller models feel much faster:

| Your RAM | Good choice | Example |
| --- | --- | --- |
| 8 GB | 1–4B parameters, `Q4_K_M` | Qwen3 4B, Gemma 3 4B, Llama 3.2 3B |
| 16 GB | up to 8–9B, `Q4_K_M` | Qwen3 8B, Llama 3.1 8B |
| 32 GB | up to 14B, or MoE models like Qwen3 30B-A3B | |

The model menu shows a badge for each model (**Fast / Good speed / Slow / Too big**)
based on your PC. After you use a model, the estimate switches to the speed it actually
measured.

For pictures, download a vision model together with its `mmproj-*.gguf` file and keep
both in the same folder. RabAI pairs them automatically and the **+** button
accepts images.

## How it adapts to the model and to your PC

When you load a model, RabAI reads the model file (architecture, size, layers,
training context, chat template) and your hardware (RAM, CPU cores, CPU features).
From that it picks the engine settings:

- **Context size**: as large as fits in your RAM. 8K tokens for normal models, 16K for
  models that think, never more than the model supports. When memory is tight it
  compresses the conversation memory (q8_0 KV cache) instead of shrinking the context.
- **CPU threads**: one per performance core. Intel's efficiency cores are used only for
  reading prompts, since they slow down generation.
- **CPU instructions**: the engine ships 13 builds, from SSE4.2 to AVX-512, and loads the
  fastest one your processor supports.
- **Thinking models** (Qwen3, DeepSeek R1, gpt-oss…): a **Think** toggle, effort levels
  when the model has them, an optional thinking limit, and **Answer now** to cut thinking
  short.
- **Recommended sampling** from the model file, or from the model maker's published
  settings for well-known families.
- **Long chats** keep working: when a conversation outgrows the model's memory, the
  oldest messages are left out and a divider marks where.
- If a model fails to load, RabAI retries automatically with safer settings, then
  explains the problem in plain words.

You can override all of this in **Settings → Model & performance**.

## Features

- Sidebar with chats grouped by date, pinned chats, search (Ctrl+K), rename, archive,
  delete, and export to Markdown
- Streaming replies with Markdown, tables, code highlighting with copy buttons, and math
- Collapsible "Thought for N s" sections for reasoning models
- Regenerate, edit-and-resend with version arrows (‹ 2/3 ›), branch into a new chat
- Picture generation with image models, live previews, edits and downloads
- Attachments: pictures, PDFs, Word/Excel/PowerPoint, text and code, audio and video;
  drag and drop; long pastes become attachments
- Temporary chats that are never saved
- Custom instructions and response style (Personalization), light/dark/system theme,
  accent colors
- Read aloud with Windows voices, keyboard shortcuts (Ctrl+/ lists them)
- Free memory when idle (unloads the model after a quiet period, reloads on demand)

## Privacy and data

Everything stays on your PC. Chats and settings live in `%APPDATA%\RabAI`; the
engine, logs and window profile in `%LOCALAPPDATA%\RabAI`. The local server only
accepts connections from RabAI's own window.

## Troubleshooting

- **A model won't load**: the message explains why (unsupported model type, not enough
  memory, damaged download). **Settings → Data controls → Open logs folder** has the
  engine's full log (`engine.log`).
- **Replies are slow**: try a smaller model or a `Q4_K_M` version, close other apps, plug
  in the laptop, or choose **Faster** under **Settings → Model & performance**.

## Building from source

On Ubuntu 24.04 with Go 1.24:

```bash
sudo apt-get install -y mingw-w64 g++-mingw-w64-x86-64-posix cmake ninja-build zip python3-numpy
build/build-vulkan-deps.sh   # Vulkan headers, import library and a recent glslc
build/build-sd.sh            # compiles stable-diffusion.cpp's sd-server for Windows
build/build-engine.sh        # compiles llama.cpp v1.0.0 and packs both engines into the payload zip
build/build-app.sh           # produces dist/RabAI.exe
go test ./internal/...       # unit tests
```

The end-to-end tests in `e2e/` drive the UI with Playwright against a running instance
(`--no-window` prints its URL). `e2e/visual.cjs` screenshots every screen in light and
dark mode at several window sizes and reports anything overlapping, clipped or spilling
out of the window. `e2e/thinking.cjs` checks a thinking model with Think on and off:
no reasoning or raw think tags in the answer, and only effort levels the model knows.
`e2e/images.cjs` makes pictures (plain requests, image mode, shapes, edits, downloads)
and attaches PDF, Word, Excel, PowerPoint, audio and video files. `--sd-engine` points
RabAI at a stable-diffusion.cpp `sd-server` for development.

## License and credits

RabAI is an independent project and isn't affiliated with OpenAI. It uses
[llama.cpp](https://github.com/ggml-org/llama.cpp) (MIT),
[stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) (MIT), TAESD (MIT),
go-webview2 (MIT), marked (MIT), DOMPurify (Apache-2.0/MPL-2.0), highlight.js
(BSD-3-Clause), KaTeX (MIT), pdf.js (Apache-2.0) and Lucide icons (ISC). Image models
you download come with their own licenses. License texts are in `web/vendor/` and in the engine folder.
