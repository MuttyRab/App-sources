// LocalChat: a ChatGPT-style app for running GGUF models locally.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"localchat/internal/engine"
	"localchat/internal/hw"
	"localchat/internal/models"
	"localchat/internal/server"
	"localchat/internal/store"
	"localchat/internal/winui"
	"localchat/web"
)

// Set at build time with -ldflags "-X main.version=... -X main.engineVersion=...".
var (
	version       = "1.0.0"
	engineVersion = "llama.cpp"
)

const appName = "LocalChat"

func init() {
	// WebView2 must be driven from the thread that created it.
	runtime.LockOSThread()
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v\n\n%s", r, debug.Stack())
			log.Printf("panic: %s", msg)
			winui.ShowError(appName, "LocalChat ran into a problem and has to close.\n\n"+fmt.Sprint(r))
			os.Exit(2)
		}
	}()

	var (
		flagBrowser  = flag.Bool("browser", false, "open the UI in a browser instead of the app window")
		flagNoWindow = flag.Bool("no-window", false, "run the server only and print its URL (for testing)")
		flagPort     = flag.Int("port", 0, "port for the local server (0 = any free port)")
		flagData     = flag.String("data", "", "folder for chats and settings")
		flagEngine   = flag.String("engine", "", "path to a llama-server executable to use instead of the built-in one")
		flagWeb      = flag.String("web", "", "serve the UI from this folder (development)")
		flagDebug    = flag.Bool("debug", false, "enable developer tools in the window")
	)
	flag.Parse()

	dataDir, localDir := appDirs(*flagData)
	logsDir := filepath.Join(localDir, "logs")
	os.MkdirAll(logsDir, 0o755)
	setupLog(filepath.Join(logsDir, "app.log"))
	log.Printf("%s %s starting (%s/%s)", appName, version, runtime.GOOS, runtime.GOARCH)

	if !*flagNoWindow && !winui.SingleInstance(appName) {
		return
	}

	st, err := store.Open(dataDir)
	if err != nil {
		fatal("Couldn't open the data folder " + dataDir + ": " + err.Error())
	}
	lib := models.Open(filepath.Join(dataDir, "models.json"))
	h := hw.Detect()
	log.Printf("hardware: %s, %d physical cores (%d performance), %.1f GB RAM, AVX2=%v", h.CPUName, h.Physical, h.PerfCores, float64(h.TotalRAM)/(1<<30), h.AVX2)

	exe, err := resolveEngine(*flagEngine, localDir)
	if err != nil {
		fatal("The AI engine couldn't be prepared: " + err.Error())
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *flagPort))
	if err != nil {
		fatal("Couldn't start the local server: " + err.Error())
	}
	port := ln.Addr().(*net.TCPAddr).Port
	eng := engine.New(exe, logsDir, port)
	native := winui.NewNative()

	var webFS fs.FS = web.FS
	if *flagWeb != "" {
		webFS = os.DirFS(*flagWeb)
	}
	token := randomHex(24)
	srv := server.New(server.Config{
		Store: st, Library: lib, Engine: eng, HW: h, Native: native, Web: webFS,
		Token: token, Version: version, EngineInfo: engineVersion,
		ScanDirs: defaultScanDirs, OllamaDir: ollamaDir(), SystemDark: winui.SystemDark,
	})
	srv.SetAddr(ln.Addr().String())
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", port, token)

	dark, bg := initialTheme(st)
	switch {
	case *flagNoWindow:
		fmt.Println(url)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
	case !*flagBrowser && native.RunWindow(winui.WindowOptions{
		URL: url, Title: appName, DataPath: filepath.Join(localDir, "WebView2"),
		StatePath: filepath.Join(localDir, "window.json"), Debug: *flagDebug, Dark: dark, BG: bg,
	}):
		// The window was closed.
	default:
		log.Printf("WebView2 window unavailable; using a browser window")
		done, err := winui.OpenFallback(url, filepath.Join(localDir, "BrowserProfile"))
		if err != nil {
			fatal("Couldn't open a window for LocalChat: " + err.Error())
		}
		waitForBrowser(srv, done)
	}

	log.Printf("shutting down")
	eng.Shutdown()
	httpSrv.Close()
}

// appDirs returns the roaming data folder (chats, settings) and the local
// folder (engine, WebView2 profile, logs).
func appDirs(override string) (data, local string) {
	if override != "" {
		return override, filepath.Join(override, "local")
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), appName), filepath.Join(os.Getenv("LOCALAPPDATA"), appName)
	}
	cfg, _ := os.UserConfigDir()
	cache, _ := os.UserCacheDir()
	return filepath.Join(cfg, "localchat"), filepath.Join(cache, "localchat")
}

func setupLog(path string) {
	if st, err := os.Stat(path); err == nil && st.Size() > 4<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
}

func fatal(msg string) {
	log.Print(msg)
	winui.ShowError(appName, msg)
	os.Exit(1)
}

// resolveEngine returns the llama-server executable: an explicit override,
// the copy unpacked from the embedded payload (Windows), or $LOCALCHAT_ENGINE.
func resolveEngine(override, localDir string) (string, error) {
	if override == "" {
		override = os.Getenv("LOCALCHAT_ENGINE")
	}
	if override != "" {
		if st, err := os.Stat(override); err == nil && st.IsDir() {
			override = filepath.Join(override, exeName("llama-server"))
		}
		if _, err := os.Stat(override); err != nil {
			return "", err
		}
		return override, nil
	}
	if len(engine.Payload) == 0 {
		return "", fmt.Errorf("this build has no built-in engine; pass --engine")
	}
	dir, err := engine.Extract(engine.Payload, filepath.Join(localDir, "engine"))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, exeName("llama-server")), nil
}

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// defaultScanDirs lists the folders where people usually keep GGUF models.
func defaultScanDirs() []string {
	if env := os.Getenv("LOCALCHAT_SCAN"); env != "" {
		return filepath.SplitList(env)
	}
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "Desktop"),
		filepath.Join(home, "Documents"),
		filepath.Join(home, "models"),
		filepath.Join(home, "AI"),
		filepath.Join(home, ".lmstudio", "models"),
		filepath.Join(home, ".cache", "lm-studio", "models"),
		filepath.Join(home, ".cache", "llama.cpp"),
		filepath.Join(home, ".cache", "huggingface", "hub"),
	}
	if runtime.GOOS == "windows" {
		dirs = append(dirs,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "llama.cpp"),
			filepath.Join(home, "OneDrive", "Documents"),
			filepath.Join(home, "OneDrive", "Desktop"),
		)
	}
	return dirs
}

func ollamaDir() string {
	if d := os.Getenv("OLLAMA_MODELS"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ollama", "models")
}

// initialTheme picks the title-bar colors before the UI has loaded.
func initialTheme(st *store.Store) (dark bool, bg string) {
	var s struct {
		Theme string `json:"theme"`
	}
	if raw, _ := st.ReadJSON("settings.json"); raw != nil {
		json.Unmarshal(raw, &s)
	}
	switch strings.ToLower(s.Theme) {
	case "dark":
		dark = true
	case "light":
		dark = false
	default:
		dark = winui.SystemDark()
	}
	if dark {
		return true, "#212121"
	}
	return false, "#ffffff"
}

// waitForBrowser blocks until the Edge app window closes or, for a plain
// browser tab, until no UI has been connected for a while.
func waitForBrowser(srv *server.Server, done <-chan struct{}) {
	started := time.Now()
	seen := false
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			// An Edge process that exits right away handed the window to an
			// already-running Edge; fall back to watching the UI connection.
			if time.Since(started) < 10*time.Second {
				done = nil
				continue
			}
			return
		case <-t.C:
			n, last := srv.SSEClients()
			if n > 0 {
				seen = true
				continue
			}
			if seen && time.Since(last) > 45*time.Second {
				return
			}
		}
	}
}
