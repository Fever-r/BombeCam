//go:build windows

package main

import "syscall"

func hiddenWindowAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{HideWindow: true} }
