//go:build !windows

// Package winproc starts helper programs without a console window on Windows.
package winproc

import "os/exec"

// Hide does nothing outside Windows.
func Hide(cmd *exec.Cmd) {}
