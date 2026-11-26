//go:build windows

package main

import "golang.org/x/sys/windows"

// Crashes in the engines (for example a broken graphics driver while RabAI
// tests the GPU) must not pop up Windows error dialogs: RabAI notices the
// failure and falls back on its own. Child processes inherit this mode.
func init() {
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOGPFAULTERRORBOX | windows.SEM_NOOPENFILEERRORBOX)
}
