//go:build windows

package mediamtx

import (
	"os/exec"

	"github.com/Fever-r/BombeCam/internal/winproc"
)

// hideWindow keeps helper processes from flashing (or, with BombeCam's
// window-less build, opening) a console window.
func hideWindow(cmd *exec.Cmd) {
	winproc.Hide(cmd)
}
