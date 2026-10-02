package main

import (
	"fmt"
	"strings"
)

// Reusing an open BombeCam tab. The tray icon (and starting bombecam.exe
// again) brings a browser window showing a BombeCam page to the front
// instead of opening another tab; only when there is none does it open one.
// Windows only sees each browser window's current tab, so a BombeCam tab
// hidden behind other tabs still gets a new tab.

// pageTitles are the <title>s of BombeCam's pages. The page shown after
// Shut down is titled differently, so a tab left on it is not reused.
var pageTitles = []string{
	"BombeCam",
	"Use with Frigate and Home Assistant · BombeCam",
	"Ways to block camera traffic · BombeCam",
}

// browserWindowClasses are the window classes of the common browsers:
// Chrome, Edge, Brave, Opera and Vivaldi share Chromium's; then Firefox.
var browserWindowClasses = map[string]bool{
	"Chrome_WidgetWin_1": true,
	"MozillaWindowClass": true,
}

// isBombeCamPageWindow reports whether a top-level window is a browser
// window whose current tab is a BombeCam page. Browsers title their windows
// "<page title> - Google Chrome", "<page title> — Mozilla Firefox",
// "<page title> and 2 more pages - Profile 1 - Microsoft Edge", and so on.
func isBombeCamPageWindow(title, class string) bool {
	if !browserWindowClasses[class] {
		return false
	}
	for _, page := range pageTitles {
		rest, ok := strings.CutPrefix(title, page)
		if !ok {
			continue
		}
		if rest == "" || strings.HasPrefix(rest, " - ") || strings.HasPrefix(rest, " — ") || strings.HasPrefix(rest, " and ") {
			return true
		}
	}
	return false
}

// showPage brings an open BombeCam tab to the front, or opens the page.
func showPage(targetURL string) {
	if focusOpenPage() {
		fmt.Println("[ui] brought the open BombeCam page to the front")
		return
	}
	openBrowser(targetURL)
}
