# LocalChat Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Windows `LocalChat.exe` that loads any local GGUF into a bundled llama.cpp engine and chats with it in a ChatGPT-faithful UI tuned for older CPU-only laptops.

**Architecture:** Go shell (WebView2 window, localhost HTTP API, engine manager) serving an embedded vanilla-JS UI; llama-server (llama.cpp v1.0.0, MinGW cross-build, all CPU variants) runs as a child process with a per-model profile.

**Tech Stack:** Go 1.24, github.com/jchv/go-webview2, golang.org/x/sys, go-winres; llama.cpp v1.0.0; marked 18.2.0, DOMPurify 3.4.17, highlight.js 11.12.0, KaTeX 0.19.1, Lucide 1.70.0; Playwright (tests only).

**Spec:** `docs/superpowers/specs/2026-11-25-localchat-design.md`

## Global Constraints

- App root: `localchat/` (Go module `localchat`). Windows build: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`, `-ldflags "-H windowsgui -s -w"`.
- No network access by the app at runtime; HTTP server binds `127.0.0.1` only, checks `Host`, requires the session cookie for `/api/*` and engine routes.
- Data: `%APPDATA%\LocalChat` (settings.json, chats/, models.json); `%LOCALAPPDATA%\LocalChat` (engine/, webview/, logs/).
- Engine flags always: `--host 127.0.0.1 --port <free> --no-webui -np 1 --jinja -fa auto`; plus profile flags `-c -t -tb -b -ub -ctk -ctv -cram` and `--mmproj` when paired.
- UI copy: sentence case, plain verbs, no OpenAI names or logo. Product name "LocalChat".
- Frontend libraries are vendored files (no CDN).

## Review Focus

- A GGUF that is truncated, not a GGUF, an mmproj, a LoRA adapter, or split part 2 of N → clear message in the picker, never a crash (tests in Task 1 and Task 4).
- Model larger than RAM → loads with ctx 2048 and a "too big" warning, not a refusal (Task 2 test).
- App window closed or app killed while a model loads → engine process exits (Task 3: stdin EOF + job object; verified under Wine in Task 9).
- Conversation longer than the context → oldest turns dropped from the request with a notice instead of an engine error (Task 7 e2e test).
- Chat or settings file corrupted/half-written → app starts, skips the bad chat, keeps a `.bad` copy (Task 4 test).

---

### Task 1: GGUF metadata reader

**Files:** Create `localchat/internal/gguf/gguf.go`, `localchat/internal/gguf/gguf_test.go`

**Interfaces — Produces:**
`func ReadInfo(path string) (*Info, error)`; `type Info struct { Path string; FileSize int64; Arch, Name, SizeLabel, Type, Quant string; FileType int; Params, ParamsActive uint64; Layers, CtxTrain, EmbdLen, HeadCount, HeadCountKV, KeyLen, ValueLen, SlidingWindow, ExpertCount, ExpertUsed, KVLoraRank uint32; ChatTemplate string; Sampling map[string]any; SplitCount, SplitIndex uint32; Pooling bool }`; `func (i *Info) KVBytesPerToken(bytesPerElem float64) float64`; `func QuantName(fileType int) string`.

- [ ] Test `TestReadInfo_Qwen3`, `TestReadInfo_Gemma3`, `TestReadInfo_MMProj` against the downloaded test models (skip if absent): arch `qwen3`/`gemma3`/`clip`, CtxTrain 40960/32768, Type `mmproj` for the projector, Params within 1% of 0.6e9/0.27e9.
- [ ] Test `TestReadInfo_Garbage`: random bytes and a 10-byte truncated GGUF return an error, no panic.
- [ ] Implement streaming parser (bufio, skips arrays without allocating, limits string/array sizes to guard against corrupt files), counts params from tensor dims, active params for MoE via `_exps` tensors × used/total.
- [ ] `go test ./internal/gguf/` passes; commit.

### Task 2: Hardware detection and model profile

**Files:** Create `localchat/internal/hw/{hw.go,hw_windows.go,hw_other.go}`, `localchat/internal/profile/{profile.go,profile_test.go}`

**Interfaces — Produces:**
`hw.Detect() hw.Info{TotalRAM, AvailRAM uint64; Logical, Physical, PerfCores int; CPUName string; AVX, AVX2, AVX512, FMA, F16C bool; OnBattery bool}`;
`profile.Compute(m *gguf.Info, h hw.Info, p Prefs, l Learned) Profile`;
`Profile{Ctx int; CacheK, CacheV string; Threads, ThreadsBatch, Batch, UBatch, CacheRAMMiB int; Fit string /*fits|tight|too_big*/; NeedBytes uint64; EstTPS float64; Speed string; Reasoning, Vision bool; ImageMaxTokens int; Sampling map[string]float64; ModelTitles bool; Notes []string}`; `func (p Profile) Args() []string`.

- [ ] Tests with synthetic Info/hw: 8 GB RAM + 4.9 GB 8B Q4_K_M → Ctx ≥ 4096, Fit `tight`; 16 GB + same → Ctx 8192 f16; 8 GB + 12 GB model → Fit `too_big`, Ctx 2048; reasoning template → target 16384 when it fits; hybrid CPU 2P+8E → Threads 2, ThreadsBatch 10; MoE active params lower EstTPS divisor; Learned KV bytes/token overrides the estimate.
- [ ] Implement; tests pass; commit.

### Task 3: Engine manager

**Files:** Create `localchat/internal/engine/{engine.go,extract.go,proc_windows.go,proc_other.go,engine_test.go}`

**Interfaces — Produces:** `engine.New(dir string) *Manager`; `(*Manager).Load(req LoadRequest) error` (async); `Unload()`; `State() State{Status /*idle|loading|ready|error*/; Progress float64; Model string; Port int; Error string; Memory *MemReport; Props json.RawMessage}`; `Subscribe() (<-chan State, func())`; `BaseURL() string`; `Extract(zipBytes []byte, root string) (dir string, err error)`.

- [ ] Test with a fake `llama-server` script (Linux) that prints `cmd_child_to_router:state:` lines and serves `/health`: progress events are published, ready status reached, unload kills the child, a failing child yields `error` with its last stderr lines mapped to friendly text.
- [ ] Implement: free port, env `LLAMA_SERVER_ROUTER_PORT`, stdin pipe, stderr ring buffer + log file, Windows `CREATE_NO_WINDOW` + kill-on-close Job Object, graceful `cmd_router_to_child:exit` then kill after 5 s.
- [ ] Tests pass; commit.

### Task 4: Storage and model library

**Files:** Create `localchat/internal/store/{store.go,store_test.go}`, `localchat/internal/models/{library.go,library_test.go}`

**Interfaces — Produces:** `store.Open(root string) (*Store, error)`; chats `List() []ChatMeta`, `Get(id) (*Chat, error)`, `Put(*Chat) error`, `Delete(id) error`, `DeleteAll() error`; `Settings() Settings`, `SaveSettings(Settings) error`. `models.Library` with `Add(path) (*Entry, error)`, `Recent() []Entry`, `Scan(dirs []string) []Entry`, `PairMMProj(path) string`, `Learn(path string, l profile.Learned)`.

- [ ] Tests: atomic write survives a simulated crash (temp file left behind is ignored); corrupted chat file is skipped and renamed `.bad`; scan pairs `mmproj-*.gguf`, hides projectors/adapters, lists only part 1 of splits with summed size.
- [ ] Implement; tests pass; commit.

### Task 5: HTTP server, proxy, window and dialogs

**Files:** Create `localchat/main.go`, `localchat/internal/server/{server.go,server_test.go}`, `localchat/internal/winui/{window_windows.go,window_other.go,dialog_windows.go,dialog_other.go}`, `localchat/web/embed.go`

**Interfaces — Produces (HTTP):** `GET /api/state`, `GET /api/events` (SSE), `POST /api/models/pick`, `POST /api/models/load {path, overrides}`, `POST /api/models/unload`, `GET /api/models`, `POST /api/models/scan`, `GET|PUT /api/settings`, `GET /api/chats`, `GET|PUT|DELETE /api/chats/{id}`, `POST /api/chats/delete-all`, `POST /api/open-url`, `POST /api/window/theme`, `POST /api/files/pick`; proxied `/v1/*`, `/props`, `/tokenize`, `/apply-template`.

- [ ] Tests: requests without cookie → 401; wrong Host → 403; proxy streams SSE chunks unbuffered and strips `Origin`/`Sec-Fetch-*`; engine not ready → 503 JSON.
- [ ] Implement; on Windows: WebView2 window 1280×820 centered, icon resource, Win11 caption color + dark-mode attribute synced to theme, window placement persisted, single-instance (named mutex + focus existing), Edge `--app` fallback.
- [ ] Tests pass; commit.

### Task 6: UI shell, sidebar, header, model menu, settings

**Files:** Create `localchat/web/index.html`, `web/css/app.css`, `web/js/{app.js,api.js,state.js,ui.js,icons.js,sidebar.js,modelmenu.js,settings.js}`, vendor files.

- [ ] Playwright check (`localchat/e2e/ui.spec.mjs`): empty state shows "What can I help with?", sidebar collapses, search modal filters chats, settings tabs switch, theme toggle changes `data-theme`, screenshots light/dark at 1280×800 and 900×700.
- [ ] Implement; check passes; commit.

### Task 7: Conversation: composer, streaming, Markdown, thinking, branches

**Files:** Create `web/js/{chat.js,composer.js,markdown.js,stream.js,context.js}`

- [ ] Playwright with the real engine and Qwen3-0.6B: send → streamed answer renders; stop works; regenerate creates "2/2" arrows; edit-and-resend; thinking block shows "Thought for Ns" and collapses; code block has Copy; table and KaTeX render; temporary chat isn't saved; context overflow drops oldest turns with a notice; image attach works with SmolVLM + mmproj.
- [ ] Implement; checks pass; commit.

### Task 8: Engine build scripts and Windows packaging

**Files:** Create `localchat/build/{build-engine.sh,build-app.sh,mingw-toolchain.cmake,mingw-compat.h}`, `localchat/winres/{winres.json,icon.png}`, `localchat/internal/engine/payload_windows.go` (embed zip)

- [ ] `build-engine.sh` produces `engine-win-x64.zip` with llama-server.exe, ggml/llama DLLs, all `ggml-cpu-*.dll`, MinGW runtime DLLs and LICENSE files; `build-app.sh` produces `dist/LocalChat.exe`.
- [ ] Verify: `file dist/LocalChat.exe` reports PE32+ GUI; under Wine `llama-server.exe --version` prints `version: ... (b1b770d)` and loads Qwen3-0.6B and answers `/v1/chat/completions`.
- [ ] Commit.

### Task 9: Verification, review, delivery

- [ ] Run all Go tests and Playwright checks; design-critique and accessibility-review passes on screenshots; fix findings.
- [ ] Code review of the branch; fix findings.
- [ ] README for the owner (how to run, SmartScreen note, model suggestions); push branch; deliver the exe.
