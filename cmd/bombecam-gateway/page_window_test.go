package main

import "testing"

func TestIsBombeCamPageWindow(t *testing.T) {
	const chromium, firefox = "Chrome_WidgetWin_1", "MozillaWindowClass"
	for _, tc := range []struct {
		title, class string
		want         bool
	}{
		{"BombeCam - Google Chrome", chromium, true},
		{"BombeCam and 3 more pages - Profile 1 - Microsoft​ Edge", chromium, true},
		{"BombeCam — Mozilla Firefox", firefox, true},
		{"Use with Frigate and Home Assistant · BombeCam - Google Chrome", chromium, true},
		{"BombeCam", chromium, true},
		// not a BombeCam page, or not a browser
		{"BombeCam has shut down - Google Chrome", chromium, false},
		{"BombeCam-main - GitHub - Google Chrome", chromium, false},
		{"Inbox - Gmail - Google Chrome", chromium, false},
		{"BombeCam", "CabinetWClass", false}, // the BombeCam folder in File Explorer
		{"BombeCam - Notepad", "Notepad", false},
	} {
		if got := isBombeCamPageWindow(tc.title, tc.class); got != tc.want {
			t.Errorf("%q (%s) = %v, want %v", tc.title, tc.class, got, tc.want)
		}
	}
}
