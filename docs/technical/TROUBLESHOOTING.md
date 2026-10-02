# Troubleshooting cloud sign-in, the server key and the app ID

Start with the log. BombeCam writes everything it prints to `gateway.log` (the previous run is kept as `gateway.prev.log`):

| System | Log folder |
|---|---|
| Windows | `%LOCALAPPDATA%\bombecam\logs\` (tray icon → Open log folder) |
| Linux | `$XDG_DATA_HOME/bombecam/logs/` or `~/.local/share/bombecam/logs/` |
| Docker | `docker compose logs gateway` |

The log never contains the server key, the app ID or passwords, so it is safe to attach to an issue after removing your email address and camera names.

## Quick checks

1. **Which key is in use?** Open **Settings → Server key** (or `http://127.0.0.1:8654/api/v1/server-key`). It says whether the built-in key, a key you saved, or a start-up setting is used, and shows any problem.
2. **Which app ID is in use?** Open **Settings → App ID** (or `http://127.0.0.1:8654/api/v1/app-id`). It says the same for the app ID. Neither shows the value itself.
3. **Does the account work?** Sign in to the Osaio app with the same login BombeCam uses.
4. **Is the cloud reachable?** "Osaio unreachable, retrying..." in the header means a network or service problem, not a key or app ID problem.

## Symptoms

| What you see | Likely cause | What to check or do |
|---|---|---|
| **Osaio rejected the sign-in** right after an Osaio app update, while the same login works in the app | Osaio changed its server key or its app ID (the message names both) | Paste the new key under **Settings → Server key**, or the new app ID under **Settings → App ID**. Either works at once; BombeCam signs in again by itself. A newer BombeCam release with the new built-in values also fixes it. |
| **Osaio rejected the sign-in**, no app update | Wrong email or password | Press **Update password** for that login on the **Osaio logins** tab. |
| Worked before; now cameras stay on *Connecting...* and then *failed to connect 3 times*; log shows `the Osaio sign-in for ... seems to have expired; signing in again with the saved login` followed by `signing in again failed` | The session expired and signing in again failed, often because the key or app ID changed | Same as the first row. Saving a key or app ID also lifts the 10-minute wait between renewal attempts. |
| Updated BombeCam to get a newer key, still rejected; **Settings → Server key** says "the key you saved" | A key saved earlier overrides the newer built-in one | Press **Use built-in key**. |
| Updated BombeCam to get a newer app ID, still rejected; **Settings → App ID** says "the app ID you saved" | An app ID saved earlier overrides the newer built-in one | Press **Use built-in app ID**. |
| Sign-in page says **BombeCam needs a working Osaio server key**; the header says **Osaio server key needed (Settings)** | The configured key source is broken (for example a hand-edited `server.key` with two lines) | Paste a working key on the page, or fix the file. If the field is greyed out, the key comes from `-server-key-file` or an environment variable; fix it there. |
| Sign-in page says **BombeCam needs a working Osaio app ID**; the header says **Osaio app ID needed (Settings)** | The configured app ID source is broken (for example a hand-edited `app.id` with two lines) | Paste a working app ID on the page, or fix the file. If the field is greyed out, the app ID comes from `-app-id-file` or an environment variable; fix it there. |
| Built BombeCam from source (or its Docker image); the page asks for **a working Osaio server key** at first start; log says `[server-key] no server key configured` | The build has no key in it (no `osaio-setup.txt` when it was built) | Paste the key on the page, or run `go run ./tools/setup` and build again. If you don't have it, use a release download or the published Docker image (clear `BOMBECAM_GATEWAY_IMAGE` in `deploy/.env`): both have it built in. |
| Built BombeCam from source (or its Docker image); the page asks for **a working Osaio app ID** at first start; log says `[app-id] no app ID configured` | The build has no app ID in it (no `osaio-setup.txt` when it was built) | Paste the app ID on the page, or run `go run ./tools/setup` and build again. If you don't have it, use a release download or the published Docker image (clear `BOMBECAM_GATEWAY_IMAGE` in `deploy/.env`): both have it built in. |
| Log: `[server-key] ... is set but unusable: ...` | The source named in the line is broken: the file named by `-server-key-file` or `BOMBECAM_SERVER_KEY_FILE` is missing, empty or has two key lines, or `BOMBECAM_SERVER_KEY` contains spaces or line breaks | Fix it (one key line; `#` comments allowed) and restart. For a broken `server.key` in the data folder, saving a key on the page is enough. BombeCam does not fall back to another source on purpose. |
| Log: `[app-id] ... is set but unusable: ...` | The source named in the line is broken: the file named by `-app-id-file` or `BOMBECAM_APP_ID_FILE` is missing, empty or has two app ID lines, or `BOMBECAM_APP_ID` contains spaces or line breaks | Fix it (one app ID line; `#` comments allowed) and restart. For a broken `app.id` in the data folder, saving an app ID on the page is enough. BombeCam does not fall back to another source on purpose. |
| **Settings → Server key** says "Change it where it is set, then restart BombeCam." and **Save key** is greyed out, or saving answers "the server key is set on the command line or in the environment" (`409 server_key_not_editable`) | A start-up setting overrides the page | Change or remove `-server-key-file`, `BOMBECAM_SERVER_KEY` or `BOMBECAM_SERVER_KEY_FILE`, then restart. |
| **Settings → App ID** says "Change it where it is set, then restart BombeCam." and **Save app ID** is greyed out, or saving answers "the app ID is set on the command line or in the environment" (`409 app_id_not_editable`) | A start-up setting overrides the page | Change or remove `-app-id-file`, `BOMBECAM_APP_ID` or `BOMBECAM_APP_ID_FILE`, then restart. |
| Header says **Osaio unreachable, retrying...**; log shows `Osaio not reachable (...); retrying in ...` | No internet, DNS trouble, or the vendor's service is down | Nothing to change in BombeCam. It keeps retrying. A key or app ID problem looks different: it fails at once and does not retry. |
| Edited `server.key` or `app.id` by hand, nothing changed | Files are read at start | Restart BombeCam, or save the value from the web page instead. |
| Docker: `docker compose up` or `pull` says `denied` or `not found` for `ghcr.io/fever-r/bombecam-gateway` | No published image with that tag yet, or a typo in `BOMBECAM_GATEWAY_IMAGE` | Check the tag against the [Releases page](https://github.com/Fever-r/BombeCam/releases); clear `BOMBECAM_GATEWAY_IMAGE` to use the newest 1.x image. |
| Docker: new key or app ID in `.env` not used | The container still has the old environment | `docker compose up -d` recreates it. Check with `/api/v1/server-key` or `/api/v1/app-id`, not by printing the environment. |
| Controls answer **control transport unavailable** or *no active signaling connection* | Controls travel over the camera's live session, which needs a working cloud sign-in | Fix sign-in first; controls return when the camera streams again. |
| Everything worked, a new key is set, sign-in still fails | The vendor changed more than the key: its app ID, app version or message format | A new app ID needs no code change: paste it under **Settings → App ID**; it works at once. A new app version or message format needs a code change. See [MAINTAINERS.md](MAINTAINERS.md#when-the-vendor-changes-something). |

## What a key or app ID problem does not affect

- Router rules for **Block cloud video** stay in force. A missing or rejected key or app ID never removes or loosens them.
- Saved logins, cameras and settings are kept. Nothing is deleted while BombeCam waits for a key or app ID.
- BombeCam never switches to the experimental local control mode because the cloud failed.

## Reporting a problem

Open an issue with the `vendor-change` label if sign-in broke after an Osaio app update (and say so if you found a new key or app ID that works), or `config` for problems setting the key or app ID. Include the BombeCam version (`bombecam-gateway -version`), the sources shown under **Settings → Server key** and **Settings → App ID**, and the relevant log lines.
