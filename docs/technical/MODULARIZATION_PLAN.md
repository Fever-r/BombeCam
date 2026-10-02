# Modularization plan

Goal: the server key and the app ID can be swapped by anyone without a new release, and the vendor's cloud protocol is confined to one place, so that a vendor change means a settings change or an edit to `pkg/bridge`, never a hunt through the whole program.

## Decisions this plan follows

These come from the maintainers' discussion of the documentation task and from the project's decision record:

- **Ship the latest known key in the downloads, keep it out of the code, let users replace it.** The downloads work out of the box. **Settings → Server key** says where the key in use comes from (never the key itself) and swaps it at once, so users are not stuck waiting for a release when the vendor changes its key. Keeping the key private was considered and dropped: it is part of the vendor's app and gives no real protection. It is set at build time instead of written into the source.
- **The app ID works like the key.** It ships built in, stays out of the code, and users replace it under **Settings → App ID**, which never shows it either.
- **One way in for the key and the app ID.** The built-in values reach a build only through `internal/buildkey` and are never in the code; everything else asks the store (`serverkey.Provider` for the key, `bridge.AppID` for the app ID).
- **Explain why the key and the app ID are needed and how to change them.** The docs do not repeat vendor addresses or protocol details, which stay in the code.
- **Controls stay on the cloud.** Local control was set aside because the camera only trusts the vendor's server certificate and modifying the camera is not acceptable. The experimental local code stays as a reference and is never used as an automatic fallback.
- **Make forking easy.** The project is MIT-licensed. Clear interfaces, no hidden secrets, tests that run without a real account, semantic versioning, a changelog and labelled issues.

## Done

### 1. Key provider and shared value store (done)

`pkg/serverkey` defines `Provider` (`Key() (string, error)`) with two implementations: `Store` for the gateway and `Static` for tests. `Store` reads, in order, `-server-key-file`, `BOMBECAM_SERVER_KEY`, `BOMBECAM_SERVER_KEY_FILE`, `server.key` in the data folder, and the built-in `LatestKnown`. The web page saves to `server.key` or removes it to go back to the built-in key. A source that is set but broken is reported, not skipped.

The store itself lives in `pkg/osaiovalue`, and the app ID uses it too. `pkg/serverkey` and `pkg/bridge/appid.go` only describe their value (`serverkey.Kind`, `bridge.AppIDKind`): the app ID is read, in order, from `-app-id-file`, `BOMBECAM_APP_ID`, `BOMBECAM_APP_ID_FILE`, `app.id` in the data folder, and the built-in `bridge.LatestKnownAppID`. Saving a value equal to the built-in one removes the saved file, the same as a reset. A value from the command line or the environment cannot be changed from the page (`serverkey.ErrNotEditable`, `bridge.ErrAppIDNotEditable`).

Checks: `pkg/serverkey/serverkey_test.go` covers order, the built-in fallback, broken sources, the file format, saving, reset, and refusing to overwrite an environment key; it runs the shared store through `serverkey.Load`. `pkg/osaiovalue/osaiovalue_test.go` runs each store test once for the server key and once for the app ID (`TestBothKindsLoadPrecedence`, `TestBothKindsBrokenSourceIsReportedNotSkipped`, `TestBothKindsSaveBuiltInRemovesFile`, `TestBothKindsNotEditableFromFlagOrEnvironment`, `TestKindsAreSeparate`, and others). `TestAppIDFromStoreAppliesAtOnce` and `TestAppIDFileFromEnvironment` check that requests use the app ID store.

### 2. Key and app ID supplied to the cloud client (done)

`bridge.NewCloud` takes a `serverkey.Provider`, and the sessions pass the gateway's store to every client they create. `Cloud.sign` asks the provider before each request and sends nothing without a key. The key no longer sits inside the cloud client; its built-in value is `serverkey.LatestKnown`, set at build time.

The app ID is not passed to each client. The gateway installs its store once with `bridge.UseAppIDs`, and `Cloud` and `Connect` ask `bridge.AppID()` before each request; without an app ID they send nothing and return an error that wraps `bridge.ErrNoAppID`. Live-session reconnects take the app ID in use at that moment.

Checks: `TestCloudSignsWithSuppliedKey` (signatures change with the supplied key), `TestCloudSendsNothingWithoutKey` (zero requests), `TestCloneKeepsKeySource`, `TestRequestsCarryTheAppID`, `TestCloudSendsNothingWithoutAppID` (zero requests), and the end-to-end mock cloud, which rejects requests signed with anything but the harness key or carrying any other app ID.

### 3. Hot swap and recovering from a key or app ID change (done)

A key or app ID saved on the page is used by every session from the next request. If the configured key is unusable, startup sets `server_key_missing` and waits; if the app ID is, it sets `app_id_missing` and waits. Saving the missing value resumes it. A refused sign-in is reported as `invalid_credentials` with a message naming the password, the key and the app ID. Saving a new key or app ID signs every refused login in again and lifts the 10-minute renewal wait. Router rules and saved data are never touched.

Checks: `TestStartupWaitsForServerKey`, `TestStartupWaitsForAppID`, `TestServerKeyChangeRecoversSessions`, `TestAppIDChangeRecoversSessions`, `TestE2E_ServerKey_*`, `TestE2E_AppID_*`.

### 4. Key and app ID settings on the web page and in deployment (done)

**Settings → Server key** says where the key in use comes from without showing it, saves a new one, and offers **Use built-in key**. **Settings → App ID** does the same for the app ID and offers **Use built-in app ID**. The sign-in page shows a key or app ID field only when that value is unusable. `GET/POST/DELETE /api/v1/server-key` and `/api/v1/app-id` read the status, save and reset; neither ever returns the value. Docker passes `BOMBECAM_SERVER_KEY`, `BOMBECAM_SERVER_KEY_FILE`, `BOMBECAM_APP_ID` and `BOMBECAM_APP_ID_FILE`. `server.key` and `app.id` are in `.gitignore` and `.dockerignore`. The configuration and troubleshooting pages ship in every download.

Checks: `tests/ui/app_controller_test.cjs` (neither dialog shows its value; the key and the app ID are saved before sign-in; a sign-in refused for a missing app ID opens its field), the end-to-end tests above, and a manual check of the sign-in page on a local build.

### 5. Handlers use only the control interface (done)

`ControlChannel` gained `SetAttribute` for switches without their own method. Every control handler gets its channel from `cameraControl` and no longer sends vendor messages or retries a failed command over the signaling socket itself. `/cmd` remains a deliberate raw passthrough for debugging.

Checks: `TestAPI_SettingSwitchesUseControlChannel`, `TestAPI_CameraControlDispatch`, `TestDetectionSwitches`, `TestLocalControlsNeverFallBackToAvailableCloud`.

## Still to do

Each step should land on its own, with the checks listed, and leave the program working.

### 6. Put the cloud client behind interfaces

Define small interfaces where they are used: an account interface for `SessionManager` (sign in, camera list) and a session-setup interface for `StreamManager` (start a live session). Make `*bridge.Cloud` satisfy them, and replace the concrete type in `SessionManager`, `SessionRegistry`, `StreamManager` and `session_renew.go`.

Done when: those files no longer name `*bridge.Cloud`, and gateway tests can run with a fake account and a fake session starter instead of an HTTP mock.

### 7. Separate media from session signaling

`streamOnce` calls `VideoCall`, `Connect` and `NewViewer` in a row, and `Viewer` sends session messages over the signaling connection itself and can arm talk directly. Give media a session interface (offer/answer, candidates, keep-alive) owned by the session, so `Viewer` only handles audio and video. Talk arming goes only through `ControlChannel`. Keep the existing audio encoders and decoders.

Done when: `media.go` no longer imports or calls the signaling type, a dropped control connection is reported separately from a stalled stream, and the live badge still needs decoded-frame progress.

### 8. Keep vendor setting names inside `pkg/bridge`

Readback, telemetry and Home Assistant discovery in the gateway still use the vendor's names for night vision, the status light and the detection switches. Map them once in `pkg/bridge` to neutral names (`night_vision`, `status_light`, `motion_detection`, ...) and use those in the gateway and API.

Done when: a search for the vendor attribute names in `cmd/` finds nothing, and the API answers are unchanged for existing clients (or the change is recorded as breaking in the changelog).

### 9. Split `pkg/bridge`

After steps 6 to 8, move the code into packages by job: cloud HTTP and signing, session signaling, control channels, media. A package move alone does not help; do it only once the interfaces above are in place.

Done when: the media and control packages do not import the cloud HTTP package, and only the cloud package and the gateway's start-up code refer to `pkg/serverkey` and `pkg/osaiovalue`.

### 10. Optional: other key providers

Anything that can return a string can implement `serverkey.Provider`, for example an operating-system credential store. The app ID has no such interface yet: `bridge.UseAppIDs` takes an `osaiovalue.Store`. Add one only if users ask for it; keep the order of sources documented in [CONFIGURATION.md](CONFIGURATION.md).

## How to check the whole

| Check | How |
|---|---|
| Key and app ID out of the code | The variables the linker sets get no value in the code (`TestNoValuesInSource`); the build flags set both without plain text in the binary (`TestLDFlagsSetTheValues`). |
| Hot swap | A key or app ID saved on the page is used without a restart (`TestE2E_ServerKey_BuiltInThenHotSwap`, `TestE2E_AppID_BuiltInThenHotSwap`). |
| Key and app ID kept out of logs | Logs, `/api/v1/server-key`, `/api/v1/app-id` and `/api/v1/onboarding/status` never contain them (`TestStatusDoesNotRevealKey`, the status checks and `AssertZeroSecrets` in the end-to-end tests). |
| Works without a real account | The full test list in [MAINTAINERS.md](MAINTAINERS.md#tests) passes with no account configured. |
| Strict run | No skipped tests in the release check. |
| Real hardware | Separately confirmed by someone with a camera; a mock test is not a hardware result. |

## Notes on sources

- Code references are to this repository. Findings from the previous documentation pass were checked against the code before reuse; where they disagreed, the code was taken as correct.
- The linked chat session could not be opened from the tools available. Its relevant content reached this plan through the maintainers' summary of it and their later decision to ship the latest known key (the decisions listed at the top). A local chat export in the workspace covers earlier versions and says nothing about the key.
