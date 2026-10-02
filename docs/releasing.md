# Making a release

A release is a Git tag (such as `v1.0.0`) plus ready-to-run downloads built from exactly that tag. Users download from the [Releases page](https://github.com/Fever-r/BombeCam/releases); the repository itself holds only source code.

## What a release contains

| File | Contents |
|---|---|
| `BombeCam-<version>-windows-amd64.zip` | `bombecam.exe` (tray program), `bombecam-gateway.exe` (the same with a console window), README, LICENSE, DISCLAIMER, THIRD_PARTY_NOTICES, CHANGELOG, and `docs/technical/` CONFIGURATION and TROUBLESHOOTING |
| `BombeCam-<version>-linux-amd64.tar.gz` | `bombecam-gateway`, `bombecam-net`, `bombecam-verify`, `bombecam-policy`, `bombecam-certgen`, `router-setup/bombecam-router.sh`, and the same documents |
| `BombeCam-<version>-linux-arm64.tar.gz` | The same for 64-bit ARM (Raspberry Pi 3/4/5 with a 64-bit OS) |
| `BombeCam-<version>-linux-armv6.tar.gz` | The same for 32-bit ARM (32-bit Raspberry Pi OS; also runs on armv7) |
| `SHA256SUMS.txt` | The SHA-256 checksum of each archive |

GitHub adds **Source code (zip)** and **Source code (tar.gz)** to every release automatically.

The release also has a **Docker image**, `ghcr.io/fever-r/bombecam-gateway:<version>`, for linux/amd64, arm64, arm/v7 and arm/v6. It is made from the Linux downloads above: the image build takes `bombecam-gateway` out of each archive (requiring exactly one matching filename and SHA-256 in `SHA256SUMS.txt`), so the image runs exactly the downloaded program and its build never sees the secrets. Before pushing the image, the workflow builds and runs each platform, checks that its binary matches the download, exercises FFmpeg encoding and decoding, and requests the gateway's web page. Publishing the release points the image's major-version tag (`1`) and `latest` at it; `deploy/docker-compose.yml` runs the major-version tag.

`go run ./tools/release` builds the download archives and checksum list. It cross-compiles every target from any computer with Go (no CGO), puts each into a folder named after the archive, and dates every file with the last commit's time. Byte-identical archives require the same source, Go toolchain, build inputs and timestamp. It builds in the Osaio server key and app ID, obscured, from `BOMBECAM_SERVER_KEY`/`BOMBECAM_APP_ID` or `osaio-setup.txt`, and stops without them; `-allow-no-key` overrides that for test builds only. The workflow builds the Docker image separately.

**Once: the secrets.** The Release workflow takes the server key and app ID from the repository's `OSAIO_SERVER_KEY` and `OSAIO_APP_ID` secrets: **Settings → Secrets and variables → Actions → New repository secret**, once for each. Without them the workflow stops before building. Update them whenever Osaio changes its key or app ID ([technical/MAINTAINERS.md](technical/MAINTAINERS.md#updating-the-built-in-key)).

**Once: make the image public.** The first release's workflow creates the image's package on GitHub as private, so `docker compose pull` says `denied` for everyone else. After that first run: your GitHub profile → **Packages** → **bombecam-gateway** → **Package settings** → **Change visibility** → **Public**. Later releases keep that setting.

## Steps

1. **Set the version.** Edit `internal/version/version.go` (for example `const Version = "1.1.0"`) and rename the `## Unreleased` heading at the top of `CHANGELOG.md` to `## 1.1.0`; the release notes are taken from it. A release that needs users to change their setup is a new major version (see [technical/MAINTAINERS.md](technical/MAINTAINERS.md#versions-and-the-changelog)). Commit and push to `main`, and wait for the CI checks to pass.
2. **Tag it.**

   ```
   git tag v1.1.0
   git push origin v1.1.0
   ```

   The tag must be `v` + the version in `internal/version/version.go`; the build stops if they differ.
3. **GitHub builds the draft.** The **Release** workflow (`.github/workflows/release.yml`) runs the tests, builds the downloads from the tag, pushes the Docker image `ghcr.io/fever-r/bombecam-gateway:1.1.0` and creates a **draft** release with the downloads, titled "BombeCam 1.1.0", with notes from the changelog. Watch it under the repository's **Actions** tab.
4. **Test the draft.** Download the Windows ZIP from the draft on a Windows PC, extract it, run `bombecam.exe`, and check that the header shows the new version and your cameras work. Under **Settings → Server key** and **Settings → App ID**, check that the built-in key and app ID are in use. To try the image, set `BOMBECAM_GATEWAY_IMAGE=ghcr.io/fever-r/bombecam-gateway:1.1.0` in `deploy/.env` and run `docker compose up -d`.
5. **Publish.** On the draft, press **Edit → Publish release**. It becomes the latest release that the README links to, and the Release workflow runs again to point the image's `1` and `latest` tags at `1.1.0` (not for a pre-release such as `1.2.0-beta.1`). Publish releases in version order: publishing an older patch after a newer release would move those tags back to it.

A draft is only visible to people with write access to the repository, and releases of a private repository are only visible to people with access to it.

### Without GitHub Actions

On any computer with Go, with the server key and app ID set up (`go run ./tools/setup`):

```
go run ./tools/release -notes release-notes.md
```

This writes the downloads into `dist/` and the notes to `release-notes.md`. Then on GitHub: **Releases → Draft a new release**, choose or create the tag, paste the notes, attach every file from `dist/`, and publish. Build from a clean checkout of the tag, never from a folder with personal files in it.

This makes no Docker image. To push one by hand, log in with `docker login ghcr.io` (a personal access token with `write:packages`) and run the `docker buildx build --push` command from the **Push the Docker image** step of `release.yml`, with the version and your lowercase GitHub name filled in. Do it before publishing, or the workflow that moves the `1` and `latest` tags finds no image.

### Only the Windows ZIP

```
go run ./tools/release -targets windows-amd64
```

## Things to know

- **Windows SmartScreen.** Programs downloaded from the internet that are not code-signed show "Windows protected your PC" until they have built up reputation; users choose **More info → Run anyway**. A code-signing certificate removes the warning (open-source projects can apply for free signing, for example through SignPath Foundation).
- **Checksums.** Users can compare a download with `SHA256SUMS.txt`: on Windows `Get-FileHash .\BombeCam-1.1.0-windows-amd64.zip`, on Linux `sha256sum -c SHA256SUMS.txt`.
- **Native downloads do not include MediaMTX or FFmpeg.** BombeCam downloads MediaMTX 1.9.3 on first start and verifies its checksum; FFmpeg is optional and installed by the user. The gateway Docker image includes FFmpeg, and Compose runs MediaMTX in a separate container. When the pinned MediaMTX version changes, update its checksums in `internal/mediamtx/config.go` and the download step in `.github/workflows/ci.yml`.
- **The router script has its own version** (`VERSION` in `bombecam-router.sh` and `policy.Version`). Change it only when the script changes; BombeCam then offers **Update router** to users.
- **Deleting a release** does not delete its tag or its Docker image. To redo a release, delete the release and the tag (`git push --delete origin v1.1.0`), fix, and tag again; the new run replaces the image's `1.1.0` tag.
