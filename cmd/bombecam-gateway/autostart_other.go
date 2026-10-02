//go:build !windows

package main

// Start with Windows exists only on Windows.

func autostartSupported() bool { return false }

func autostartStatus() (enabled, thisCopy bool, command string) { return false, false, "" }

func setAutostart(on bool) error { return errAutostartUnsupported }
