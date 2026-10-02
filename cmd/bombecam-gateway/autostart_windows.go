//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Start with Windows: a value under the current user's Run key, the same
// place Task Manager's Startup apps list reads (and can turn off). No
// administrator rights are needed.

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "BombeCam"
)

func autostartSupported() bool { return true }

// autostartCommand is what Windows runs at sign-in for this copy.
func autostartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return `"` + exe + `" -startup`, nil
}

// autostartStatus reports whether BombeCam starts with Windows, and whether
// the entry starts this copy (it may point to an older version's folder).
func autostartStatus() (enabled, thisCopy bool, command string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, false, ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	if err != nil || v == "" {
		return false, false, ""
	}
	mine, _ := autostartCommand()
	return true, strings.EqualFold(v, mine), v
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	cmd, err := autostartCommand()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValueName, cmd)
}
