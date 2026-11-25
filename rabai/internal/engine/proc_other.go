//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

func prepareCmd(cmd *exec.Cmd) {
	// Ask Linux to kill the engine if RabAI dies (development builds).
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func afterStart(cmd *exec.Cmd) {}
