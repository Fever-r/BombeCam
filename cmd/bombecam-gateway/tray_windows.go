//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
)

// BombeCam runs without a console window on Windows (the bombecam.exe
// build is a GUI program). The tray icon by the clock is how to reach it:
// click it to show the web page (an open BombeCam tab is brought to the
// front instead of opening another); right-click for Start with Windows, the
// log folder and Shut down.

// runMainLoop runs the tray until wait returns (Ctrl+C, the page's Shut
// down, or Windows signing out), or until Shut down is picked in the tray.
func runMainLoop(t trayConfig, wait func()) {
	if t.disabled {
		wait()
		return
	}
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
		systray.Quit()
	}()
	systray.Run(func() { trayReady(t) }, requestShutdown)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func trayReady(t trayConfig) {
	systray.SetIcon(appIcon())
	systray.SetTitle("BombeCam")
	systray.SetTooltip("BombeCam")
	systray.SetOnTapped(func() { showPage(t.uiURL) })

	mOpen := systray.AddMenuItem("Open BombeCam", "Open BombeCam's page in your browser")
	enabled, thisCopy, _ := autostartStatus()
	mStart := systray.AddMenuItemCheckbox("Start with Windows", "Start BombeCam when you sign in to Windows", enabled && thisCopy)
	mLog := systray.AddMenuItem("Open log folder", "The folder with gateway.log")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Shut down BombeCam", "Stop all streams and close BombeCam. Firewall rules stay on your router.")

	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		updateTip := func() {
			if t.streaming == nil {
				return
			}
			active, total := t.streaming()
			systray.SetTooltip(fmt.Sprintf("BombeCam: %d of %d cameras streaming", active, total))
			on, mine, _ := autostartStatus()
			if on && mine {
				mStart.Check()
			} else {
				mStart.Uncheck()
			}
		}
		updateTip()
		for {
			select {
			case <-mOpen.ClickedCh:
				showPage(t.uiURL)
			case <-mStart.ClickedCh:
				on := !mStart.Checked()
				if err := setAutostart(on); err != nil {
					fmt.Printf("[startup] could not change Start with Windows: %v\n", err)
				} else {
					fmt.Printf("[startup] start with Windows: %v\n", on)
				}
				updateTip()
			case <-mLog.ClickedCh:
				cmd := exec.Command("explorer.exe", t.logDir)
				_ = cmd.Start()
			case <-mQuit.ClickedCh:
				requestShutdown()
				return
			case <-tick.C:
				updateTip()
			}
		}
	}()
}

// attachParentConsole shows BombeCam's output in the terminal it was
// started from, if any (the GUI build has no console of its own).
func attachParentConsole() {
	const attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(uintptr(attachParentProcess)); r == 0 {
		return // double-clicked, or already has a console
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
}

// fatalNotice shows a startup error in a message box: without a console
// window it would otherwise go unseen.
func fatalNotice(msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	text, _ := syscall.UTF16PtrFromString(msg)
	title, _ := syscall.UTF16PtrFromString("BombeCam")
	const mbIconWarning = 0x30
	_, _, _ = user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbIconWarning)
}
