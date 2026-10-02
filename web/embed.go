package web

import "embed"

// Assets contains the static single-page application assets for BombeCam Gateway.
//
//go:embed index.html style.css app.js hls.min.js blocking-options.html integrations.html integrations.js
var Assets embed.FS
