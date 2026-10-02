# Contributing

Bug reports, fixes and improvements are welcome. For a larger change, please open an issue first to discuss it.

## Issues

Use a label so the right people see it:

| Label | For |
|---|---|
| `vendor-change` | Sign-in, live view or controls broke after an Osaio app or server update |
| `config` | Setting the server key, the app ID or other settings |
| `transport` | The cloud client, session signaling, or the local control path |
| `good first issue` | Small, well-described tasks for new contributors |
| `bug` | Anything else that does not work as documented |

Include the BombeCam version (`bombecam-gateway -version`), what you expected, what happened, and the relevant lines from `gateway.log`. Remove email addresses and camera names. **Never post a password, token or device ID.** If Osaio changed its server key or app ID and you found a new one that works, say so in a `vendor-change` issue.

## Pull requests

- Keep each pull request to one change, and describe what it changes for users.
- Add or update tests for the behaviour you change. Tests use made-up keys, app IDs, accounts and camera IDs only; nothing may need a real account or camera to pass.
- Record user-visible changes under `## Unreleased` in `CHANGELOG.md`.
- Update the README or `docs/technical` when you change a setting, an API answer or a message users see.
- CI must pass.

Maintainer topics (versions, dependencies, vendor changes) are in [docs/technical/MAINTAINERS.md](docs/technical/MAINTAINERS.md).

## Building

Requires Go 1.26 or newer. The Osaio server key and app ID are not in the code: `go run ./tools/setup` saves them in `osaio-setup.txt` (git ignores it) and every build below includes them, obscured. A build without one of them asks for it on its web page. See [docs/technical/CONFIGURATION.md](docs/technical/CONFIGURATION.md#building-with-a-key).

```
go run ./tools/setup                # once: the Osaio server key and app ID that builds include
make build                          # Linux/macOS: all programs into bin/native
.\build.ps1                         # Windows: bin\windows-amd64\*.exe, .\bombecam.exe, and SHA256SUMS.txt
.\build.ps1 -Target linux-amd64
go run ./tools/release              # every release download into dist/; needs the key and app ID (see docs/releasing.md)
```

On Windows, double-clicking `build.cmd` asks for the key and app ID the first time, runs `build.ps1` and offers to start the result.

Windows build by hand:

```
keyflag="$(go run ./tools/setup -ldflags)" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -H=windowsgui $keyflag" -o bin/windows-amd64/bombecam.exe ./cmd/bombecam-gateway
```

`-H=windowsgui` makes `bombecam.exe` a tray program without a console window; build without it for a console version. `keyflag` builds in the key and app ID if they are set up; a value that is set up but broken stops the command.

The version lives in `internal/version/version.go`; `bombecam-gateway -version` prints it and the web page shows it.

## Tests

```
go vet ./...
go test ./...
node --check web/app.js
node tests/ui/app_controller_test.cjs          # web page controller (Node.js)
```

`go test ./...` needs **FFmpeg and ffprobe** on PATH (media pipeline and talk audio tests). Other prerequisites:

| Needs | Tests |
|---|---|
| `BOMBECAM_TEST_MEDIAMTX=/path/to/mediamtx` (MediaMTX 1.9.3) | publishing to a real MediaMTX and configuration checks; skipped when not set |
| `-tags integration` with `BOMBECAM_TEST_MEDIAMTX` (and root for nftables) | MediaMTX reconfiguration, settings transactions, kernel-expiring firewall leases |
| `-tags mqttintegration` and a Mosquitto broker | the experimental local MQTT control path |
| Windows | DPAPI key protection |
| A POSIX `sh` (so not Windows) | the `tests/e2e` flows that apply rules on the mock router, which runs the real router script; skipped without it |
| `BOMBECAM_SOAK=<duration>` with `BOMBECAM_TEST_MEDIAMTX` | long-running timestamp soak test |

The router tests run against real kernel traffic and a real SSH server; they need Linux and root:

```
sudo sh tests/router/netns_traffic_test.sh     # router script vs real traffic in network namespaces (nft, iptables-legacy, conntrack, iproute2, python3)
sudo sh tests/router/dropbear_gate_test.sh     # BombeCam's restricted key vs a real dropbear server
python3 tests/deploy/guard_test.py             # Linux boot guard scripts with fake system tools
python3 tests/deploy/gateway_extract_test.py   # real release-extraction script with isolated archives
python3 tests/deploy/gateway_image_test.py --downloads dist --platform linux/amd64 --source
                                             # complete images, exact release binary, FFmpeg and HTTP
```

`tests/e2e` runs the gateway against a mock Osaio cloud and a mock router. The mock cloud accepts only requests that carry the app ID and are signed with the key it expects, so every end-to-end test also checks request signing.

GitHub Actions runs these checks on every push to `main` and every pull request (`.github/workflows/ci.yml`): vet and tests, the MediaMTX tests one package at a time, the router tests in network namespaces and against dropbear, and a build of every release download.

The **Docker images** workflow checks deployment changes on pushes and pull requests. It builds isolated downloads without vendor credentials, then builds and runs both the release and source images on amd64, arm64, arm/v7 and arm/v6 with QEMU. It includes the real FFmpeg installation and encoding/decoding, checks the release binary byte for byte, and opens the running gateway's web page. These checks do not establish camera compatibility or Osaio authentication. For the local image check above, first create `dist` with `go run ./tools/release -allow-no-key` and ensure Docker Buildx and the requested platform's emulation are available. Missing prerequisites and failed checks fail the run; they are never skipped.

## Guidelines

- Keep `router-setup/bombecam-router.sh` identical to `pkg/renderer/openwrt/bombecam-router.sh` (a test checks this), and bump `VERSION` in the script together with `policy.Version` when the router script changes.
- Keep `deploy/mediamtx.yml` equivalent to the configuration generated in `internal/mediamtx/config.go` (a test checks this).
- Block cloud video only allows, drops and rate-limits traffic, plus clearing the blocked cameras' own connection-tracking entries when the rules go into force. It never redirects or rewrites traffic and never modifies camera firmware.
- Describe the firewall as it is: "Camera internet access is limited by destination and rate; encrypted traffic may still carry image data." (`internal/ui/privacy_wording_test.go` checks the wording.)
- Secrets belong in the encrypted profile only and must never be logged. The server key and app ID are settings, not user secrets: they are kept in `server.key` and `app.id` or built in, and are never logged either.
- The server key and the app ID are never in the code or in a commit. Builds set them (obscured) through `internal/buildkey`; at run time both come from a `pkg/osaiovalue` store, and the gateway asks `serverkey.Provider` for the key and `bridge.AppID` for the app ID. Never commit `osaio-setup.txt`.
- Vendor protocol details stay in `pkg/bridge`. Handlers send camera commands through `ControlChannel` only.
- The control API stays on `127.0.0.1` by default, and MediaMTX's API stays on `127.0.0.1`.
- Existing stream addresses (`rtsp://<pc>:8554/<camera-id>` and the named form) must keep working.
- FFmpeg stays optional, and nothing new may contact the internet without saying so in the documentation.
