# Third-party notices

BombeCam includes or uses the following third-party software. Each component remains under its own license; the full license texts are in the respective projects (and, for Go modules, in the module source downloaded by `go mod download`).

## Included in BombeCam's binaries

| Component | Version | License |
|---|---|---|
| [hls.js](https://github.com/video-dev/hls.js) (`web/hls.min.js`) | 1.5.17 | Apache License 2.0 |
| [Pion WebRTC](https://github.com/pion/webrtc) and its modules: datachannel, dtls, ice, interceptor, logging, mdns, randutil, rtcp, rtp, sctp, sdp, srtp, stun, transport, turn | see `go.mod` | MIT |
| [Eclipse Paho MQTT Go client](https://github.com/eclipse/paho.mqtt.golang) | 1.5.1 | Eclipse Public License 2.0 / Eclipse Distribution License 1.0 |
| [Gorilla WebSocket](https://github.com/gorilla/websocket) | 1.5.3 | BSD 2-Clause |
| [fyne.io/systray](https://github.com/fyne-io/systray) | 1.12.2 | Apache License 2.0 |
| [godbus/dbus](https://github.com/godbus/dbus) | 5.1.0 | BSD 2-Clause |
| [google/uuid](https://github.com/google/uuid) | 1.6.0 | BSD 3-Clause |
| [wlynxg/anet](https://github.com/wlynxg/anet) | 0.0.5 | BSD 3-Clause |
| [rsc.io/qr](https://github.com/rsc/qr) (QR code for two-step sign-in setup) | 0.2.0 | BSD 3-Clause |
| Go extended libraries: [x/crypto](https://pkg.go.dev/golang.org/x/crypto), [x/net](https://pkg.go.dev/golang.org/x/net), [x/sync](https://pkg.go.dev/golang.org/x/sync), [x/sys](https://pkg.go.dev/golang.org/x/sys) | see `go.mod` | BSD 3-Clause |
| [Go standard library](https://go.dev) | | BSD 3-Clause |

## Used alongside BombeCam (not included)

| Component | How it is used | License |
|---|---|---|
| [MediaMTX](https://github.com/bluenviron/mediamtx) 1.9.3 | Separate program that serves the streams. Downloaded from its official release on first use if not installed, or run from the Docker image `bluenviron/mediamtx`. | MIT |
| [FFmpeg](https://ffmpeg.org) | Optional separate program, installed by the user, for talk audio, browser audio and snapshots. | LGPL 2.1+ / GPL 2+, depending on the build |
| [Eclipse Mosquitto](https://mosquitto.org) | Optional Docker service for the experimental local MQTT mode. | EPL 2.0 / EDL 1.0 |

OSAIO, Yoton, GNCC, WOLFANG and Surfola are trademarks of their respective owners. BombeCam is not affiliated with them.
