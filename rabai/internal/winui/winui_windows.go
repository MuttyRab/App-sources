//go:build windows

// Package winui hosts the UI in a native WebView2 window and provides native
// dialogs, link opening and title-bar theming on Windows.
package winui

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetWindowPlacement  = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement  = user32.NewProc("SetWindowPlacement")
	procIsIconic            = user32.NewProc("IsIconic")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")

	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	windowClass = "webview" // class registered by go-webview2
	swRestore   = 9
	swShow      = 5
)

// Native implements server.Native for Windows.
type Native struct {
	mu sync.Mutex
	w  webview2.WebView
}

// NewNative returns the native helper. Attach a window with SetWindow.
func NewNative() *Native { return &Native{} }

func (n *Native) window() webview2.WebView {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.w
}

func (n *Native) hwnd() uintptr {
	if w := n.window(); w != nil {
		return uintptr(w.Window())
	}
	return 0
}

// onUIThread runs f on the window's thread (or on a dedicated COM thread in
// browser mode) and waits for it.
func (n *Native) onUIThread(f func()) {
	done := make(chan struct{})
	if w := n.window(); w != nil {
		w.Dispatch(func() { f(); close(done) })
		<-done
		return
	}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		defer windows.CoUninitialize()
		f()
		close(done)
	}()
	<-done
}

// PickModelFile shows the Windows "Open" dialog filtered to GGUF files.
func (n *Native) PickModelFile() (string, error) {
	if p := os.Getenv("RABAI_PICK"); p != "" { // automated testing
		return p, nil
	}
	var path string
	var err error
	n.onUIThread(func() {
		path, err = fileDialog(n.hwnd(), dialogOptions{
			title:   "Choose a GGUF model",
			filters: [][2]string{{"GGUF models (*.gguf)", "*.gguf"}, {"All files", "*.*"}},
		})
	})
	return path, err
}

// PickModelFiles shows the "Open" dialog with multi-select for GGUF files.
func (n *Native) PickModelFiles() ([]string, error) {
	if p := os.Getenv("RABAI_PICK"); p != "" { // automated testing
		return filepath.SplitList(p), nil
	}
	var paths []string
	var err error
	n.onUIThread(func() {
		paths, err = fileDialogMulti(n.hwnd(), dialogOptions{
			title:   "Add GGUF models",
			multi:   true,
			filters: [][2]string{{"GGUF models (*.gguf)", "*.gguf"}, {"All files", "*.*"}},
		})
	})
	return paths, err
}

// PickFolder shows the Windows folder picker.
func (n *Native) PickFolder() (string, error) {
	var path string
	var err error
	n.onUIThread(func() {
		path, err = fileDialog(n.hwnd(), dialogOptions{title: "Choose a folder with models", folder: true})
	})
	return path, err
}

// SaveFile shows "Save as" and writes data to the chosen file.
func (n *Native) SaveFile(suggested, ext string, data []byte) (string, error) {
	var path string
	var err error
	label := strings.ToUpper(strings.TrimPrefix(ext, ".")) + " file"
	n.onUIThread(func() {
		path, err = fileDialog(n.hwnd(), dialogOptions{
			title: "Export", save: true, fileName: suggested, defExt: strings.TrimPrefix(ext, "."),
			filters: [][2]string{{label, "*." + strings.TrimPrefix(ext, ".")}, {"All files", "*.*"}},
		})
	})
	if err != nil || path == "" {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// OpenURL opens a web link in the default browser.
func (n *Native) OpenURL(u string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(u), nil, nil, swShow)
}

// Reveal opens File Explorer with path selected (or the folder itself).
func (n *Native) Reveal(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	cmd := exec.Command("explorer.exe")
	if st.IsDir() {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + path + `"`}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	}
	return cmd.Start()
}

// SetTheme colors the Windows 11 title bar to match the app.
func (n *Native) SetTheme(dark bool, bg string) {
	if n.window() == nil {
		return
	}
	n.window().Dispatch(func() { applyTheme(n.hwnd(), dark, bg) })
}

func applyTheme(hwnd uintptr, dark bool, bg string) {
	if hwnd == 0 || procDwmSetWindowAttribute.Find() != nil {
		return
	}
	const (
		dwmwaUseImmersiveDarkMode = 20
		dwmwaBorderColor          = 34
		dwmwaCaptionColor         = 35
		dwmwaTextColor            = 36
	)
	var on uint32
	if dark {
		on = 1
	}
	procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), 4)
	if c, ok := colorRef(bg); ok {
		procDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&c)), 4)
		procDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&c)), 4)
		text := uint32(0x000D0D0D)
		if dark {
			text = 0x00ECECEC
		}
		procDwmSetWindowAttribute.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&text)), 4)
	}
}

// colorRef converts "#rrggbb" to a Windows COLORREF (0x00bbggrr).
func colorRef(hex string) (uint32, bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, false
	}
	r, g, b := uint32(v>>16)&0xff, uint32(v>>8)&0xff, uint32(v)&0xff
	return b<<16 | g<<8 | r, true
}

// WindowOptions configures the main window.
type WindowOptions struct {
	URL       string
	Title     string
	DataPath  string
	StatePath string // where the window position is remembered
	Debug     bool
	Dark      bool
	BG        string
}

type windowPlacement struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    [2]int32
	PtMaxPosition    [2]int32
	RcNormalPosition [4]int32
}

// RunWindow opens the WebView2 window and blocks until it is closed. It
// returns false without blocking if WebView2 can't be created.
func (n *Native) RunWindow(o WindowOptions) bool {
	runtime.LockOSThread()
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     o.Debug,
		DataPath:  o.DataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  o.Title,
			Width:  1280,
			Height: 820,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	n.mu.Lock()
	n.w = w
	n.mu.Unlock()

	hwnd := uintptr(w.Window())
	scale := 1.0
	if procGetDpiForWindow.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
			scale = float64(dpi) / 96
		}
	}
	w.SetSize(int(720*scale), int(520*scale), webview2.HintMin)
	if !restorePlacement(hwnd, o.StatePath) {
		centerScaled(hwnd, scale)
	}
	applyTheme(hwnd, o.Dark, o.BG)
	w.Navigate(o.URL)

	// Remember the window position (polled, since go-webview2 does not
	// expose WM_CLOSE).
	stop := make(chan struct{})
	var last []byte
	var lastMu sync.Mutex
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if b := readPlacement(hwnd); b != nil {
					lastMu.Lock()
					last = b
					lastMu.Unlock()
				}
			}
		}
	}()
	w.Run()
	close(stop)
	lastMu.Lock()
	if last != nil && o.StatePath != "" {
		os.WriteFile(o.StatePath, last, 0o644)
	}
	lastMu.Unlock()
	n.mu.Lock()
	n.w = nil
	n.mu.Unlock()
	w.Destroy()
	return true
}

// centerScaled sizes the window for the display scale (at most 90% of the
// work area) and centers it on its monitor.
func centerScaled(hwnd uintptr, scale float64) {
	const monitorDefaultToNearest = 2
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	var mi struct {
		Size    uint32
		Monitor [4]int32
		Work    [4]int32
		Flags   uint32
	}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if mon == 0 {
		return
	}
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return
	}
	ww, wh := mi.Work[2]-mi.Work[0], mi.Work[3]-mi.Work[1]
	w, h := int32(1280*scale), int32(820*scale)
	if w > ww*9/10 {
		w = ww * 9 / 10
	}
	if h > wh*9/10 {
		h = wh * 9 / 10
	}
	x, y := mi.Work[0]+(ww-w)/2, mi.Work[1]+(wh-h)/2
	const swpNoZOrder = 0x0004
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder)
}

func readPlacement(hwnd uintptr) []byte {
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		return nil // don't remember a minimized window
	}
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return nil
	}
	b, _ := json.Marshal(wp)
	return b
}

func restorePlacement(hwnd uintptr, path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var wp windowPlacement
	if json.Unmarshal(b, &wp) != nil {
		return false
	}
	rc := wp.RcNormalPosition
	if rc[2]-rc[0] < 400 || rc[3]-rc[1] < 300 {
		return false
	}
	wp.Length = uint32(unsafe.Sizeof(wp))
	if wp.ShowCmd != 3 { // SW_SHOWMAXIMIZED
		wp.ShowCmd = 1 // SW_SHOWNORMAL
	}
	r, _, _ := procSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	return r != 0
}

// SingleInstance returns false if RabAI is already running, after
// bringing the existing window to the front.
func SingleInstance(title string) bool {
	name := windows.StringToUTF16Ptr(`Local\RabAI.SingleInstance`)
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		cls := windows.StringToUTF16Ptr(windowClass)
		ttl := windows.StringToUTF16Ptr(title)
		if hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(ttl))); hwnd != 0 {
			procShowWindow.Call(hwnd, swRestore)
			procSetForegroundWindow.Call(hwnd)
		}
		return false
	}
	return true // the mutex handle is kept open for the life of the process
}

// OpenFallback shows the UI when WebView2 isn't available: an Edge app window
// with its own profile (so its process ends when the window closes), or the
// default browser. done is closed when the Edge window exits.
func OpenFallback(url, profileDir string) (done <-chan struct{}, err error) {
	if edge := findEdge(); edge != "" {
		cmd := exec.Command(edge, "--app="+url, "--user-data-dir="+profileDir, "--no-first-run",
			"--no-default-browser-check", "--disable-sync", "--window-size=1280,820")
		if err := cmd.Start(); err == nil {
			ch := make(chan struct{})
			go func() { cmd.Wait(); close(ch) }()
			return ch, nil
		}
	}
	return nil, (&Native{}).OpenURL(url)
}

func findEdge() string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`, registry.QUERY_VALUE); err == nil {
			p, _, err := k.GetStringValue("")
			k.Close()
			if err == nil {
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		p := filepath.Join(os.Getenv(env), "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// SystemDark reports whether Windows apps are set to dark mode.
func SystemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// ShowError displays a blocking message box (used before any window exists).
func ShowError(title, msg string) {
	windows.MessageBox(0, windows.StringToUTF16Ptr(msg), windows.StringToUTF16Ptr(title), windows.MB_OK|windows.MB_ICONERROR)
}
