//go:build !windows

// Package winui provides native integration. On non-Windows systems (used for
// development and automated tests) the UI opens in a browser and dialogs use
// zenity when available.
package winui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Native implements server.Native for development platforms.
type Native struct{}

// NewNative returns the native helper.
func NewNative() *Native { return &Native{} }

func zenity(args ...string) (string, error) {
	if _, err := exec.LookPath("zenity"); err != nil {
		return "", errors.New("no file dialog available on this system")
	}
	out, err := exec.Command("zenity", args...).Output()
	if err != nil {
		return "", nil // cancelled
	}
	return strings.TrimSpace(string(out)), nil
}

// PickModelFile returns $LOCALCHAT_PICK (for automated tests) or asks zenity.
func (n *Native) PickModelFile() (string, error) {
	if p := os.Getenv("LOCALCHAT_PICK"); p != "" {
		if p == "cancel" {
			return "", nil
		}
		return p, nil
	}
	return zenity("--file-selection", "--title=Choose a GGUF model", "--file-filter=GGUF models | *.gguf")
}

// PickFolder returns $LOCALCHAT_PICK_FOLDER or asks zenity.
func (n *Native) PickFolder() (string, error) {
	if p := os.Getenv("LOCALCHAT_PICK_FOLDER"); p != "" {
		return p, nil
	}
	return zenity("--file-selection", "--directory")
}

// SaveFile writes to $LOCALCHAT_SAVE_DIR (tests) or asks zenity.
func (n *Native) SaveFile(suggested, ext string, data []byte) (string, error) {
	path := ""
	if d := os.Getenv("LOCALCHAT_SAVE_DIR"); d != "" {
		path = filepath.Join(d, suggested)
	} else {
		var err error
		if path, err = zenity("--file-selection", "--save", "--filename="+suggested); err != nil || path == "" {
			return "", err
		}
	}
	return path, os.WriteFile(path, data, 0o644)
}

// OpenURL opens a link with xdg-open.
func (n *Native) OpenURL(u string) error { return exec.Command("xdg-open", u).Start() }

// Reveal opens the containing folder.
func (n *Native) Reveal(path string) error {
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		path = filepath.Dir(path)
	}
	return exec.Command("xdg-open", path).Start()
}

// SetTheme is a no-op outside Windows.
func (n *Native) SetTheme(dark bool, bg string) {}

// WindowOptions mirrors the Windows type.
type WindowOptions struct {
	URL, Title, DataPath, StatePath string
	Debug, Dark                     bool
	BG                              string
}

// RunWindow is unavailable outside Windows; callers fall back to a browser.
func (n *Native) RunWindow(o WindowOptions) bool { return false }

// SingleInstance always allows starting on development platforms.
func SingleInstance(title string) bool { return true }

// OpenFallback opens the UI in the default browser.
func OpenFallback(url, profileDir string) (<-chan struct{}, error) {
	return nil, exec.Command("xdg-open", url).Start()
}

// ShowError prints an error.
func ShowError(title, msg string) { fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg) }

// SystemDark reports the OS theme (unknown outside Windows).
func SystemDark() bool { return os.Getenv("LOCALCHAT_DARK") == "1" }
