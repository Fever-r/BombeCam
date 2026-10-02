//go:build windows

// Package winproc starts helper programs (FFmpeg, MediaMTX, arp, route...)
// without a console window of their own. BombeCam can run without a console
// (tray mode); a console program it starts would otherwise open a new
// console window each time.
package winproc

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide makes cmd start without a console window.
func Hide(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
