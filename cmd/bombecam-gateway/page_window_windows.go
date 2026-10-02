//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procGetClassNameW       = user32.NewProc("GetClassNameW")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const swRestore = 9

// focusOpenPage brings the first browser window showing a BombeCam page to
// the front (restoring it if minimized). It reports false when there is no
// such window or Windows refused to switch to it.
func focusOpenPage() bool {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return 1
		}
		var title, class [512]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		if n == 0 {
			return 1
		}
		c, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		if isBombeCamPageWindow(syscall.UTF16ToString(title[:n]), syscall.UTF16ToString(class[:c])) {
			found = hwnd
			return 0 // stop
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	if found == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(found); iconic != 0 {
		_, _, _ = procShowWindow.Call(found, swRestore)
	}
	ok, _, _ := procSetForegroundWindow.Call(found)
	return ok != 0
}
