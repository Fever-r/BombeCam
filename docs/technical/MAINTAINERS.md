# Maintaining BombeCam

How to test, release, and keep BombeCam working when the vendor changes something. Day-to-day contribution rules are in [CONTRIBUTING.md](../../CONTRIBUTING.md); the release procedure is in [releasing.md](../releasing.md).

## Tests

```bash
go vet ./...
go test -count=1 ./...
node --check web/app.js
node tests/ui/app_controller_test.cjs
```

None of these need a real account or camera. Unit tests use `serverkey.Static` with a made-up key and put a made-up app ID in `BOMBECAM_APP_ID`; the end-to-end tests in `tests/e2e` run the real gateway against a mock cloud that rejects anything not signed with the key it expects or not carrying the app ID it expects, and against a mock router. See [CONFIGURATION.md](CONFIGURATION.md#testing-without-a-real-account).

Some tests need extra tools (FFmpeg, MediaMTX, root, a POSIX shell for the router script). Without them they skip. The table in CONTRIBUTING.md lists what each needs. CI on Linux has all of them.

For a release check, a skipped test counts as a failure. Run the suite with JSON output and look for skips:

```bash
go test -count=1 -json ./... > test-results.jsonl
grep '"Action":"skip"' test-results.jsonl
```

A mock test shows that BombeCam behaves correctly against the mock. It does not show that the vendor's current servers or a real camera behave the same way. Say which one you checked when you report a result.

## When the vendor changes something

Work out what changed before touching code. A refused sign-in alone does not prove the key or the app ID changed; see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

| What changed | Who acts | Where |
|---|---|---|
| The server key only | Users at once, maintainer for the next release | Users paste it under **Settings → Server key** ([CONFIGURATION.md](CONFIGURATION.md#changing-the-key)). Maintainer: update the built-in key (below). |
| Server addresses | User, or maintainer for new defaults | `OSAIO_DEFAULT_WEB`, `OSAIO_DEFAULT_WS`, `OSAIO_GLOBAL_BASE` override them; defaults are constants in `pkg/bridge/cloud.go`. |
| The app ID the vendor expects | Users at once, maintainer for the next release | Users paste it under **Settings → App ID** ([CONFIGURATION.md](CONFIGURATION.md#changing-the-app-id)). Maintainer: update the `OSAIO_APP_ID` secret and your `osaio-setup.txt` (below). Never in the code. |
| App version the vendor expects | Maintainer | `userAgent` at the top of `pkg/bridge/cloud.go`. |
| Request or reply format, signing method | Maintainer | `pkg/bridge/cloud.go` |
| Live-session or command messages | Maintainer | `pkg/bridge/signaling.go`, `pkg/bridge/signaling_channel.go` |

### Updating the built-in key

The same steps update the built-in app ID; do both if both changed.

1. Confirm the new value works: paste the key under **Settings → Server key** (or the app ID under **Settings → App ID**) on a working setup and check sign-in, live view and one control.
2. Put the key in the repository's `OSAIO_SERVER_KEY` secret (**Settings → Secrets and variables → Actions**), which release builds use, and in your own `osaio-setup.txt` (`go run ./tools/setup -key <key>`). For the app ID, use the `OSAIO_APP_ID` secret and `go run ./tools/setup -app-id <app-id>`. Neither is in the code, so no code changes.
3. Add a line under `## Unreleased` in `CHANGELOG.md`, for example "Built-in server key updated for Osaio's change of <month>." (or "Built-in app ID updated ...").
4. Release a patch version.
5. In the release notes, remind users who saved a key or app ID by hand that they can press **Use built-in key** or **Use built-in app ID** to go back to the shipped one.

### Other vendor changes

For a code change:

1. Reproduce it in a test first: extend the mock in `tests/e2e/harness.go` or a `pkg/bridge` test so it answers the way the vendor now does.
2. Change only `pkg/bridge`. If a handler, the web page, `pkg/serverkey` or `pkg/osaiovalue` seems to need a change too, the boundary has leaked; fix that separately.
3. Run the full test list above.
4. Ask someone with a real camera to confirm sign-in, live view and one control before releasing.
5. Release a patch version and label the issue `vendor-change`.

Keep the key and the app ID out of the code: they reach a build only through `internal/buildkey` (`osaio-setup.txt`, `BOMBECAM_SERVER_KEY`/`BOMBECAM_APP_ID`, or the Actions secrets for releases), so a change needs no code change. `TestNoValuesInSource` fails if the variables the linker sets (`serverkey.builtIn`, `bridge.builtInAppID`) get a value anywhere in the code of those packages, and `TestLDFlagsSetTheValues` checks that a build sets them without plain text in the binary.

## Versions and the changelog

BombeCam uses [semantic versioning](https://semver.org): `MAJOR.MINOR.PATCH`.

- **Patch:** fixes, including adapting to a vendor change, with no action needed from users.
- **Minor:** new features or settings that existing setups do not need.
- **Major:** anything that makes existing setups need an action, such as a removed or renamed setting. Docker setups follow the image's major-version tag, so a new major version also changes that tag in `deploy/docker-compose.yml` and `deploy/.env.example` (`bombecam-gateway:1` to `:2`).

Record every user-visible change under `## Unreleased` at the top of `CHANGELOG.md` as you merge it. At release time, rename that heading to the version; the release notes are taken from it. The version itself lives in `internal/version/version.go`.

## Dependencies

- Add a dependency only when the standard library or an existing one cannot do the job.
- Use licenses compatible with MIT (MIT, BSD, Apache-2.0, ISC and similar) and add each new one to `THIRD_PARTY_NOTICES.md`.
- Pin versions in `go.mod` and commit `go.sum`. Update with `go get <module>@<version>`, then `go mod tidy`, and run the full tests.
- MediaMTX is pinned by version and SHA-256 in `internal/mediamtx/config.go` and in `.github/workflows/ci.yml`; update both together.

## CI

`.github/workflows/ci.yml` runs on every pull request and every push to `main`: vet, Go tests, web page tests, a build of every download, the MediaMTX tests, and the router tests on Linux. `.github/workflows/docker.yml` builds and runs the gateway images on all four platforms when `deploy/` or the image tests change. `.github/workflows/release.yml` runs on a version tag, tests again, runs the same image checks on the real downloads, pushes the Docker image for that version and creates a draft release; publishing the release moves the image's major-version tag and `latest` to it. Merge only with CI green.

## Issues

Labels in use (create them on GitHub if they are missing):

| Label | For |
|---|---|
| `vendor-change` | Something broke after a vendor app or server update |
| `config` | Setting the server key, the app ID or other settings |
| `transport` | The cloud client, signaling, or local control path |
| `good first issue` | Small, well-described tasks for new contributors |
| `bug` | Anything else that does not work as documented |

Ask reporters for the BombeCam version, the sources shown under **Settings → Server key** and **Settings → App ID**, and log lines with emails and camera names removed.

## Keeping personal data out

- `server.key`, `app.id` (and `*.server.key`, `*.app.id`), `deploy/.env` and `config.json` are in `.gitignore` and `.dockerignore`, so a local key, app ID or login is never committed or baked into an image.
- `osaio-setup.txt`, the values that builds include, is in `.gitignore` only: a Docker image built from source reads it on purpose, and the values end up only in the binary, obscured. The published image is made from the release downloads, so its build never sees the file or the secrets.
- Templates (`deploy/.env.example`) contain empty values only.
- Tests and fixtures use made-up emails, camera IDs, keys and app IDs; the end-to-end harness builds the gateway with a made-up built-in key and app ID.
- Before publishing a release, build it from a clean checkout of the tag (the release workflow does this) so no local files end up in it.
