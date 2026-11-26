// Package server is RabAI's localhost HTTP API: app state, models, chats,
// settings, native dialogs, live events, and a streaming proxy to the engine.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rabai/internal/engine"
	"rabai/internal/gguf"
	"rabai/internal/hw"
	"rabai/internal/imagegen"
	"rabai/internal/imagemodel"
	"rabai/internal/models"
	"rabai/internal/profile"
	"rabai/internal/store"
)

// Native is implemented by the platform window layer.
type Native interface {
	PickModelFile() (string, error)    // "" when cancelled
	PickModelFiles() ([]string, error) // nil when cancelled
	PickFolder() (string, error)       // "" when cancelled
	SaveFile(suggested, ext string, data []byte) (string, error)
	OpenURL(u string) error
	Reveal(path string) error
	SetTheme(dark bool, bg string)
}

// Config wires the server to the rest of the app.
type Config struct {
	Store      *store.Store
	Library    *models.Library
	Engine     *engine.Manager
	HW         hw.Info
	Native     Native
	Web        fs.FS
	Token      string
	Version    string
	ScanDirs   func() []string
	SystemDark func() bool
	OllamaDir  string
	EngineInfo string
	NoGPU      bool // skip graphics card detection (tests)
	ImageLib   *imagemodel.Library
	Images     *imagegen.Manager // nil when this build has no image engine
}

// Server handles HTTP requests.
type Server struct {
	cfg  Config
	addr atomic.Value // "127.0.0.1:port"

	mu      sync.Mutex
	loaded  *loadedModel
	pending *loadedModel

	sseClients  atomic.Int64
	lastClient  atomic.Int64 // unix seconds when the last SSE client left
	eventsMu    sync.Mutex
	eventSubs   map[chan []byte]struct{}
	proxy       *httputil.ReverseProxy
	lastRequest atomic.Int64

	infoMu    sync.Mutex
	infoCache map[string]cachedInfo

	imageState

	gpuMu      sync.Mutex
	gpu        gpuState
	gpuReady   chan struct{}
	tuneCancel context.CancelFunc
}

type cachedInfo struct {
	mod  time.Time
	size int64
	info *gguf.Info
}

// readInfo returns GGUF metadata, cached by path, size and modification time.
func (s *Server) readInfo(path string) (*gguf.Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s.infoMu.Lock()
	c, ok := s.infoCache[path]
	s.infoMu.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.info, nil
	}
	info, err := gguf.ReadInfo(path)
	if err != nil {
		return nil, err
	}
	s.infoMu.Lock()
	if s.infoCache == nil {
		s.infoCache = map[string]cachedInfo{}
	}
	s.infoCache[path] = cachedInfo{mod: st.ModTime(), size: st.Size(), info: info}
	s.infoMu.Unlock()
	return info, nil
}

type loadedModel struct {
	Entry   *models.Entry   `json:"entry"`
	Profile profile.Profile `json:"profile"`
	Info    *gguf.Info      `json:"-"`
	Prefs   profile.Prefs   `json:"prefs"`

	attempts []attempt
}

// New builds the server. Call SetAddr once the listener is bound.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, eventSubs: map[chan []byte]struct{}{}, gpuReady: make(chan struct{})}
	s.addr.Store("")
	if s.cfg.ImageLib == nil {
		s.cfg.ImageLib = imagemodel.Open(filepath.Join(cfg.Store.Root(), "image-models.json"))
	}
	s.cfg.ImageLib.ExtraParts = func() []string {
		var out []string
		for _, e := range cfg.Library.All() {
			out = append(out, e.Path)
		}
		return out
	}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			base, _ := r.In.Context().Value(engineURLKey{}).(string)
			target, _ := url.Parse(base)
			r.SetURL(target)
			r.Out.Host = target.Host
			// The engine refuses browser-originated requests when it runs as a
			// managed child; it also has no use for our session cookie.
			for _, h := range []string{"Origin", "Referer", "Cookie", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Sec-Fetch-User"} {
				r.Out.Header.Del(h)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the user pressed stop
			}
			jsonError(w, http.StatusBadGateway, "The engine didn't respond: "+err.Error())
		},
	}
	cfg.Engine.OnReady = s.onEngineReady
	go s.forwardEngineEvents()
	if cfg.NoGPU {
		s.setGPU(gpuState{Checked: true})
	} else {
		go s.detectGPUs()
	}
	return s
}

// SetAddr records the bound address used for Host header checks.
func (s *Server) SetAddr(addr string) { s.addr.Store(addr) }

// SSEClients returns the number of connected UI event streams and when the
// last one disconnected (used to quit in browser mode).
func (s *Server) SSEClients() (int64, time.Time) {
	return s.sseClients.Load(), time.Unix(s.lastClient.Load(), 0)
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }

	api("GET /api/state", s.handleState)
	api("GET /api/events", s.handleEvents)
	api("GET /api/models", s.handleModels)
	api("POST /api/models/pick", s.handlePick)
	api("POST /api/models/add", s.handleAdd)
	api("POST /api/models/remove-folder", s.handleRemoveFolder)
	api("POST /api/models/inspect", s.handleInspect)
	api("POST /api/models/load", s.handleLoad)
	api("POST /api/models/unload", s.handleUnload)
	api("POST /api/models/forget", s.handleForget)
	api("POST /api/models/scan", s.handleScan)
	api("POST /api/models/add-folder", s.handleAddFolder)
	api("POST /api/models/learn", s.handleLearn)
	api("GET /api/settings", s.handleGetSettings)
	api("PUT /api/settings", s.handlePutSettings)
	api("GET /api/chats", s.handleListChats)
	api("GET /api/chats/search", s.handleSearchChats)
	api("GET /api/chats/{id}", s.handleGetChat)
	api("PUT /api/chats/{id}", s.handlePutChat)
	api("DELETE /api/chats/{id}", s.handleDeleteChat)
	api("POST /api/chats/delete-all", s.handleDeleteAll)
	api("POST /api/native/open-url", s.handleOpenURL)
	api("POST /api/native/reveal", s.handleReveal)
	api("POST /api/native/theme", s.handleTheme)
	api("POST /api/native/save", s.handleSave)
	api("POST /api/gpu/retest", s.handleRetestGPU)
	api("GET /api/images", s.handleImages)
	api("POST /api/images/action", s.imageAction)
	api("POST /api/images/generate", s.handleGenerate)
	api("POST /api/images/cancel", s.handleCancelImage)
	api("GET /api/media/{id}", s.handleMedia)
	api("POST /api/media", s.handleUploadMedia)

	proxy := s.requireSession(http.HandlerFunc(s.handleProxy))
	for _, p := range []string{"/v1/", "/props", "/tokenize", "/detokenize", "/apply-template", "/slots"} {
		mux.Handle("GET "+p, proxy)
		mux.Handle("POST "+p, proxy)
	}

	static := http.FileServerFS(s.cfg.Web)
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.Handle("GET /", cacheStatic(static))
	return s.checkHost(mux)
}

// checkHost rejects requests whose Host header isn't our loopback address,
// which defeats DNS-rebinding attacks from web pages.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, _ := s.addr.Load().(string)
		if addr != "" {
			_, port, _ := net.SplitHostPort(addr)
			if r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

const cookieName = "rabai_session"

func (s *Server) validToken(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.validToken(c.Value) {
			jsonError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		if !s.validToken(t) {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if c, err := r.Cookie(cookieName); err != nil || !s.validToken(c.Value) {
		http.Error(w, "Open RabAI from its window.", http.StatusUnauthorized)
		return
	}
	data, err := fs.ReadFile(s.cfg.Web, "index.html")
	if err != nil {
		http.Error(w, "missing UI", http.StatusInternalServerError)
		return
	}
	// Paint the saved theme on first frame (no white flash in dark mode).
	var st struct {
		Theme string `json:"theme"`
	}
	if raw, _ := s.cfg.Store.ReadJSON("settings.json"); raw != nil {
		json.Unmarshal(raw, &st)
	}
	dark := st.Theme == "dark" || ((st.Theme == "" || st.Theme == "system") && s.cfg.SystemDark != nil && s.cfg.SystemDark())
	if dark {
		data = []byte(strings.Replace(string(data), `data-theme="light"`, `data-theme="dark"`, 1))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; media-src 'self' data: blob:; connect-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Write(data)
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "code": code}})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(v)
}

// ---------- events ----------

func (s *Server) publish(typ string, data any) {
	b, err := json.Marshal(map[string]any{"type": typ, "data": data})
	if err != nil {
		return
	}
	s.eventsMu.Lock()
	for ch := range s.eventSubs {
		select {
		case ch <- b:
		default:
		}
	}
	s.eventsMu.Unlock()
}

func (s *Server) forwardEngineEvents() {
	ch, _ := s.cfg.Engine.Subscribe()
	for st := range ch {
		s.publish("engine", st)
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan []byte, 32)
	s.eventsMu.Lock()
	s.eventSubs[ch] = struct{}{}
	s.eventsMu.Unlock()
	s.sseClients.Add(1)
	defer func() {
		s.eventsMu.Lock()
		delete(s.eventSubs, ch)
		s.eventsMu.Unlock()
		s.sseClients.Add(-1)
		s.lastClient.Store(time.Now().Unix())
	}()
	first, _ := json.Marshal(map[string]any{"type": "engine", "data": s.cfg.Engine.State()})
	fmt.Fprintf(w, "data: %s\n\n", first)
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// ---------- state & models ----------

func (s *Server) currentPrefs() profile.Prefs {
	var st struct {
		Perf profile.Prefs `json:"perf"`
	}
	if raw, _ := s.cfg.Store.ReadJSON("settings.json"); raw != nil {
		json.Unmarshal(raw, &st)
	}
	return st.Perf
}

func (s *Server) idleSleepSeconds() int {
	var st struct {
		IdleUnloadMin int `json:"idleUnloadMin"`
	}
	if raw, _ := s.cfg.Store.ReadJSON("settings.json"); raw != nil {
		json.Unmarshal(raw, &st)
	}
	return st.IdleUnloadMin * 60
}

func (s *Server) profileFor(e *models.Entry, info *gguf.Info, prefs profile.Prefs) profile.Profile {
	l := e.Learned
	if l.BandwidthGBs == 0 {
		l.BandwidthGBs = s.cfg.Library.Bandwidth()
	}
	p := profile.Compute(info, e.Size, e.MMProjSize, e.MMProj, s.currentHW(), prefs, l)
	if dec := l.Accel; dec != nil && prefs.GPU != "off" {
		if g := s.gpuInfo(0); g.Best != nil && g.Best.Desc == dec.Device && dec.Mode != "cpu" && dec.GPU != nil && dec.GPU.GenTPS > 0 {
			p.EstTPS = dec.GPU.GenTPS
			p.Speed = profile.SpeedLabel(p.EstTPS)
			p.Device = dec.Device
		}
		p.Accel = dec.Mode
	}
	return p
}

func (s *Server) currentHW() hw.Info {
	h := s.cfg.HW
	if fresh := hw.Detect(); fresh.AvailRAM > 0 {
		h.AvailRAM = fresh.AvailRAM
		h.OnBattery = fresh.OnBattery
	}
	return h
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	loaded := s.loaded
	if loaded == nil {
		loaded = s.pending
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"version":    s.cfg.Version,
		"engineInfo": s.cfg.EngineInfo,
		"engine":     s.cfg.Engine.State(),
		"model":      loaded,
		"hw":         s.currentHW(),
		"gpu":        s.gpuInfo(0),
	})
}

type modelView struct {
	models.Entry
	Fit    string  `json:"fit"`
	Speed  string  `json:"speed"`
	EstTPS float64 `json:"estTps"`
	Ctx    int     `json:"ctx"`
	Accel  string  `json:"accel,omitempty"`  // cpu, gpu, gpu_safe, or "" (not measured)
	Device string  `json:"device,omitempty"` // GPU it runs on
}

func (s *Server) view(e models.Entry) modelView {
	v := modelView{Entry: e}
	if e.Missing {
		return v
	}
	info, err := s.readInfo(e.Path)
	if err != nil {
		v.Missing = true
		return v
	}
	p := s.profileFor(&e, info, s.currentPrefs())
	v.Fit, v.Speed, v.EstTPS, v.Ctx = p.Fit, p.Speed, p.EstTPS, p.Ctx
	if p.Accel != "tune" {
		v.Accel, v.Device = p.Accel, p.Device
	}
	return v
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	all := s.cfg.Library.All()
	out := make([]modelView, 0, len(all))
	for _, e := range all {
		out = append(out, s.view(e))
	}
	writeJSON(w, map[string]any{"models": out, "folders": s.cfg.Library.Folders()})
}

func (s *Server) inspectResponse(w http.ResponseWriter, path string) {
	e, info, err := models.Inspect(path)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if old := s.cfg.Library.Get(path); old != nil {
		e.Learned = old.Learned
	}
	p := s.profileFor(e, info, s.currentPrefs())
	writeJSON(w, map[string]any{"entry": e, "profile": p})
}

func (s *Server) handlePick(w http.ResponseWriter, r *http.Request) {
	path, err := s.cfg.Native.PickModelFile()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if path == "" {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	s.inspectResponse(w, path)
}

// handleAdd lets the user pick several GGUF files and adds them all to the
// model list without loading any of them.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	paths, err := s.cfg.Native.PickModelFiles()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(paths) == 0 {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	type failure struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	}
	added := []modelView{}
	addedImages := []imageView{}
	failed := []failure{}
	for _, p := range paths {
		e, _, err := s.cfg.Library.Register(p)
		if err == nil {
			added = append(added, s.view(*e))
			continue
		}
		// Not a chat model: perhaps a picture model.
		ie, ierr := s.cfg.ImageLib.Add(p)
		if ierr == nil {
			addedImages = append(addedImages, s.imageView(*ie))
			continue
		}
		msg := err.Error()
		if !strings.EqualFold(filepath.Ext(p), ".gguf") || strings.Contains(msg, "doesn't contain a chat model") {
			msg = ierr.Error()
		}
		failed = append(failed, failure{Path: p, Error: msg})
	}
	if len(addedImages) > 0 {
		s.publish("images", nil)
	}
	writeJSON(w, map[string]any{"added": added, "addedImages": addedImages, "failed": failed})
}

func (s *Server) handleRemoveFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if readJSON(r, &req) != nil {
		jsonError(w, 400, "path required")
		return
	}
	var keep []string
	for _, f := range s.cfg.Library.Folders() {
		if !strings.EqualFold(f, req.Path) {
			keep = append(keep, f)
		}
	}
	s.cfg.Library.SetFolders(keep)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if readJSON(r, &req) != nil || req.Path == "" {
		jsonError(w, 400, "path required")
		return
	}
	s.inspectResponse(w, req.Path)
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path          string         `json:"path"`
		Prefs         *profile.Prefs `json:"prefs"`
		IdleUnloadMin *int           `json:"idleUnloadMin"`
	}
	if readJSON(r, &req) != nil || req.Path == "" {
		jsonError(w, 400, "path required")
		return
	}
	e, info, err := s.cfg.Library.Add(req.Path)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	prefs := s.currentPrefs()
	if req.Prefs != nil {
		prefs = *req.Prefs
	}
	p := s.profileFor(e, info, prefs)
	sleep := s.idleSleepSeconds()
	if req.IdleUnloadMin != nil {
		sleep = *req.IdleUnloadMin * 60
	}
	lm := &loadedModel{Entry: e, Profile: p, Info: info, Prefs: prefs}
	s.mu.Lock()
	s.loaded = nil
	s.pending = lm
	if s.tuneCancel != nil {
		s.tuneCancel()
		s.tuneCancel = nil
	}
	s.mu.Unlock()
	// Attempts, in order: the chosen device; on the GPU, a safe mode without
	// the features old drivers get wrong; the processor; then safer settings
	// (an uncompressed cache, a small context, no image add-on).
	mode, dev := s.accelFor(e, prefs)
	if mode == "tune" {
		lm.Profile.Accel = "tune"
		go s.tune(e, lm, *dev, sleep)
	} else {
		s.startModel(e, lm, mode, dev, sleep)
	}
	writeJSON(w, lm)
}

func withSleep(args []string, sec int) []string {
	if sec > 0 {
		return append(args, "--sleep-idle-seconds", fmt.Sprint(sec))
	}
	return args
}

// onEngineReady promotes the pending model and learns its real memory use.
func (s *Server) onEngineReady(req engine.LoadRequest, st engine.State) {
	lm, _ := req.Tag.(*loadedModel)
	if lm == nil {
		return
	}
	// A fallback attempt may have changed the effective profile.
	if st.Attempt > 0 && st.Attempt < len(lm.attempts) {
		a := lm.attempts[st.Attempt]
		lm.Profile.Accel = a.mode
		if a.mode == "cpu" {
			lm.Profile.Device = ""
		}
		if a.safeKV {
			lm.Profile.CacheK, lm.Profile.CacheV = "f16", "f16"
			if lm.Profile.Ctx > 4096 {
				lm.Profile.Ctx = 4096
			}
		}
		if a.noVision {
			lm.Profile.MMProj, lm.Profile.Vision = "", false
		}
		if lm.attempts[0].mode != "cpu" && a.mode == "cpu" {
			lm.Profile.Notes = append(lm.Profile.Notes, "The graphics card couldn't load this model, so it runs on the processor.")
		} else {
			lm.Profile.Notes = append(lm.Profile.Notes, "Loaded with safer settings after the first attempt failed.")
		}
	}
	if st.Memory != nil && st.Memory.NCtx > 0 {
		lm.Profile.Ctx = st.Memory.NCtx
		per := float64(st.Memory.ContextBytes) / float64(st.Memory.NCtx)
		if lm.Profile.CacheK == "q8_0" {
			per /= 34.0 / 64.0
		}
		if per > 0 {
			s.cfg.Library.Learn(req.ModelPath, func(l *profile.Learned) { l.KVBytesPerToken = per })
		}
	}
	s.mu.Lock()
	if s.pending == lm {
		s.loaded, s.pending = lm, nil
	}
	s.mu.Unlock()
	s.publish("model", lm)
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.loaded, s.pending = nil, nil
	if s.tuneCancel != nil {
		s.tuneCancel()
		s.tuneCancel = nil
	}
	s.mu.Unlock()
	s.cfg.Engine.Unload()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if readJSON(r, &req) != nil {
		jsonError(w, 400, "path required")
		return
	}
	s.cfg.Library.Forget(req.Path)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) scanDirs() []string {
	dirs := append([]string(nil), s.cfg.Library.Folders()...)
	if s.cfg.ScanDirs != nil {
		dirs = append(dirs, s.cfg.ScanDirs()...)
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() && !seen[strings.ToLower(d)] {
			seen[strings.ToLower(d)] = true
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var others []string
	var omu sync.Mutex
	s.cfg.Library.ScanOther = func(p string) {
		omu.Lock()
		others = append(others, p)
		omu.Unlock()
	}
	found := s.cfg.Library.Scan(s.scanDirs(), s.cfg.OllamaDir)
	s.cfg.Library.ScanOther = nil
	out := make([]modelView, 0, len(found))
	for _, e := range found {
		out = append(out, s.view(e))
	}
	images := []imageView{}
	for _, p := range others {
		if e, err := s.cfg.ImageLib.Add(p); err == nil {
			images = append(images, s.imageView(*e))
		}
	}
	if len(images) > 0 {
		s.publish("images", nil)
	}
	writeJSON(w, map[string]any{"found": out, "images": images, "dirs": s.scanDirs()})
}

func (s *Server) handleAddFolder(w http.ResponseWriter, r *http.Request) {
	dir, err := s.cfg.Native.PickFolder()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if dir == "" {
		writeJSON(w, map[string]any{"cancelled": true})
		return
	}
	folders := s.cfg.Library.Folders()
	for _, f := range folders {
		if strings.EqualFold(f, dir) {
			dir = ""
		}
	}
	if dir != "" {
		s.cfg.Library.SetFolders(append(folders, dir))
	}
	s.handleScan(w, r)
}

func (s *Server) handleLearn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string  `json:"path"`
		TPS  float64 `json:"tps"`
		N    int     `json:"n"`
	}
	if readJSON(r, &req) != nil || req.TPS <= 0 || req.N < 16 {
		writeJSON(w, map[string]any{"ok": false})
		return
	}
	s.mu.Lock()
	lm := s.loaded
	s.mu.Unlock()
	if lm == nil || lm.Entry.Path != req.Path {
		writeJSON(w, map[string]any{"ok": false})
		return
	}
	if lm.Profile.Accel != "cpu" && lm.Profile.Accel != "" {
		// GPU speeds say nothing about the processor's memory bandwidth.
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	bw := profile.BandwidthFromRun(lm.Info, lm.Entry.Size, req.TPS)
	s.cfg.Library.Learn(req.Path, func(l *profile.Learned) {
		if l.TokensPerSec == 0 {
			l.TokensPerSec = req.TPS
		} else {
			l.TokensPerSec = 0.7*l.TokensPerSec + 0.3*req.TPS
		}
		l.BandwidthGBs = bw
	})
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- settings ----------

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := s.cfg.Store.ReadJSON("settings.json")
	if err != nil {
		log.Printf("settings: %v", err)
	}
	if raw == nil {
		raw = json.RawMessage("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || !json.Valid(b) {
		jsonError(w, 400, "invalid settings")
		return
	}
	if err := s.cfg.Store.WriteJSON("settings.json", b); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- chats ----------

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.cfg.Store.List())
}

func (s *Server) handleSearchChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.cfg.Store.Search(r.URL.Query().Get("q"), 50))
}

func (s *Server) handleGetChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.cfg.Store.Get(r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "chat not found")
		return
	}
	writeJSON(w, c)
}

func (s *Server) handlePutChat(w http.ResponseWriter, r *http.Request) {
	var c store.Chat
	if err := readJSON(r, &c); err != nil {
		jsonError(w, 400, "invalid chat")
		return
	}
	c.ID = r.PathValue("id")
	if err := s.cfg.Store.Put(&c); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var media []string
	if c, err := s.cfg.Store.Get(id); err == nil {
		media = mediaRefRe.FindAllString(string(c.Messages), -1)
	}
	if err := s.cfg.Store.Delete(id); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	s.deleteUnusedMedia(media)
	writeJSON(w, map[string]any{"ok": true})
}

// deleteUnusedMedia removes media files no remaining chat refers to (a
// branched chat shares its pictures with the original).
func (s *Server) deleteUnusedMedia(ids []string) {
	if len(ids) == 0 {
		return
	}
	used := map[string]bool{}
	for _, m := range s.cfg.Store.List() {
		if c, err := s.cfg.Store.Get(m.ID); err == nil {
			for _, x := range mediaRefRe.FindAllString(string(c.Messages), -1) {
				used[x] = true
			}
		}
	}
	for _, x := range ids {
		if !used[x] {
			s.cfg.Store.DeleteMedia(x)
		}
	}
}

func (s *Server) handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.DeleteAll(); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	os.RemoveAll(filepath.Join(s.cfg.Store.Root(), "media"))
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- native ----------

func (s *Server) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if readJSON(r, &req) != nil {
		jsonError(w, 400, "url required")
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		jsonError(w, 400, "only web links can be opened")
		return
	}
	if err := s.cfg.Native.OpenURL(u.String()); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if readJSON(r, &req) != nil || req.Path == "" {
		jsonError(w, 400, "path required")
		return
	}
	if req.Path == "logs" {
		req.Path = filepath.Join(filepath.Dir(s.cfg.Store.Root()), "logs")
	}
	if err := s.cfg.Native.Reveal(req.Path); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dark bool   `json:"dark"`
		BG   string `json:"bg"`
	}
	if readJSON(r, &req) == nil {
		s.cfg.Native.SetTheme(req.Dark, req.BG)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Ext     string `json:"ext"`
		Content string `json:"content"`
		Media   string `json:"media"` // save a stored picture instead of text
	}
	if readJSON(r, &req) != nil {
		jsonError(w, 400, "invalid request")
		return
	}
	data := []byte(req.Content)
	if req.Media != "" {
		p := s.cfg.Store.MediaPath(req.Media)
		b, err := os.ReadFile(p)
		if p == "" || err != nil {
			jsonError(w, 404, "picture not found")
			return
		}
		data = b
	}
	path, err := s.cfg.Native.SaveFile(req.Name, req.Ext, data)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"path": path, "cancelled": path == ""})
}

// ---------- engine proxy ----------

type engineURLKey struct{}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	base := s.cfg.Engine.BaseURL()
	if base == "" {
		jsonError(w, http.StatusServiceUnavailable, "No model is loaded yet.")
		return
	}
	s.lastRequest.Store(time.Now().Unix())
	s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), engineURLKey{}, base)))
}
