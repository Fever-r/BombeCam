package main

// trayConfig is what the Windows tray icon needs from main.
type trayConfig struct {
	disabled  bool   // headless: no tray
	uiURL     string // the web page
	logDir    string // folder with gateway.log
	streaming func() (active, total int)
}
