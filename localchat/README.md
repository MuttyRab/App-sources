# LocalChat

A ChatGPT-style app for Windows that runs GGUF language models entirely on your PC.
One file, no installer, no internet connection, no account.

![LocalChat](web/img/app-icon.png)

## Getting started

1. Download `LocalChat.exe` (in `release/`) and double-click it.
   - Windows SmartScreen may say "Windows protected your PC" because the app isn't
     code-signed. Click **More info → Run anyway**.
2. The first time, LocalChat looks for `.gguf` files in your Downloads, Desktop,
   Documents and LM Studio / Ollama folders. Pick one of the models it finds, or click
   **Browse for a model file**.
3. Start chatting. Next time, LocalChat reloads your last model automatically.

Needs Windows 10 or 11 (64-bit). Windows 11 already includes the WebView2 component
LocalChat draws its window with; if it is missing, LocalChat opens in an Edge window
instead.

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
both in the same folder. LocalChat pairs them automatically and the **+** button
accepts images.

## How it adapts to the model and to your PC

When you load a model, LocalChat reads the model file (architecture, size, layers,
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
- If a model fails to load, LocalChat retries automatically with safer settings, then
  explains the problem in plain words.

You can override all of this in **Settings → Model & performance**.

## Features

- Sidebar with chats grouped by date, pinned chats, search (Ctrl+K), rename, archive,
  delete, and export to Markdown
- Streaming replies with Markdown, tables, code highlighting with copy buttons, and math
- Collapsible "Thought for N s" sections for reasoning models
- Regenerate, edit-and-resend with version arrows (‹ 2/3 ›), branch into a new chat
- Image and text/code file attachments, drag and drop, long pastes become attachments
- Temporary chats that are never saved
- Custom instructions and response style (Personalization), light/dark/system theme,
  accent colors
- Read aloud with Windows voices, keyboard shortcuts (Ctrl+/ lists them)
- Free memory when idle (unloads the model after a quiet period, reloads on demand)

## Privacy and data

Everything stays on your PC. Chats and settings live in `%APPDATA%\LocalChat`; the
engine, logs and window profile in `%LOCALAPPDATA%\LocalChat`. The local server only
accepts connections from LocalChat's own window.

## Troubleshooting

- **A model won't load**: the message explains why (unsupported model type, not enough
  memory, damaged download). **Settings → Data controls → Open logs folder** has the
  engine's full log (`engine.log`).
- **Replies are slow**: try a smaller model or a `Q4_K_M` version, close other apps, plug
  in the laptop, or choose **Faster** under **Settings → Model & performance**.

## Building from source

On Ubuntu 24.04 with Go 1.24:

```bash
sudo apt-get install -y mingw-w64 g++-mingw-w64-x86-64-posix cmake ninja-build zip
build/build-engine.sh        # compiles llama.cpp v1.0.0 for Windows into the payload zip
build/build-app.sh           # produces dist/LocalChat.exe
go test ./internal/...       # unit tests
```

The end-to-end tests in `e2e/` drive the UI with Playwright against a running instance
(`--no-window` prints its URL).

## License and credits

LocalChat is an independent project and isn't affiliated with OpenAI. It uses
[llama.cpp](https://github.com/ggml-org/llama.cpp) (MIT), go-webview2 (MIT), marked (MIT),
DOMPurify (Apache-2.0/MPL-2.0), highlight.js (BSD-3-Clause), KaTeX (MIT) and Lucide icons
(ISC). License texts are in `web/vendor/` and in the engine folder.
