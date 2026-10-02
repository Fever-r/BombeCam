//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// disableConsoleQuickEdit turns off the console's QuickEdit mode. With it on,
// a single click in the BombeCam window starts a text selection, and Windows
// pauses the program's console output until the selection ends. BombeCam's
// log file is the place to copy text from (Right-click > Mark still works).
func disableConsoleQuickEdit() {
	const (
		enableQuickEditMode = 0x0040
		enableExtendedFlags = 0x0080
	)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")
	h, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil || h == syscall.InvalidHandle {
		return
	}
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return // no console (started without one)
	}
	mode = (mode &^ enableQuickEditMode) | enableExtendedFlags
	_, _, _ = setMode.Call(uintptr(h), uintptr(mode))
}
