//go:build !windows

package main

// focusOpenPage: BombeCam opens no browser outside Windows.
func focusOpenPage() bool { return false }
