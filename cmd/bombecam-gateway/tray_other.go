//go:build !windows

package main

// runMainLoop waits for the shutdown signal. (The tray icon is Windows only.)
func runMainLoop(t trayConfig, wait func()) { wait() }

// attachParentConsole is needed only for the Windows GUI build.
func attachParentConsole() {}

// fatalNotice reports a startup error; outside Windows the log says it all.
func fatalNotice(msg string) {}
