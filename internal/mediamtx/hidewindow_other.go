//go:build !windows

package mediamtx

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
