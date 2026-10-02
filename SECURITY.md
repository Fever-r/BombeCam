# Security

## Reporting a vulnerability

Please report security problems privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue for a vulnerability.

Include what you found, how to reproduce it, and the BombeCam version (shown in the page header and at the top of `gateway.log`). Reports are acknowledged as soon as possible, and fixes are released with a note in the changelog.

## Security model

BombeCam runs on your own computer and talks to your cameras, Osaio's servers and, optionally, your router.

**Web page and API**
- Listens on `127.0.0.1:8654` by default.
- BombeCam has its own administrator, created on this computer at first start (or from `BOMBECAM_ADMIN_PASSWORD` when there is none). It can only be created from this computer, and only while none exists. Osaio logins never grant access to BombeCam.
- Requests from other devices always need the administrator's sign-in (a browser session, or an API token from `/api/v1/operator/token`). Requests from this computer need it too if the administrator turned on sign-in for this PC; otherwise they are trusted.
- Passwords are Argon2id hashes in the encrypted profile. Optional two-step sign-in uses TOTP (RFC 6238, each code accepted once) with ten one-time recovery codes stored as hashes. Five wrong passwords or codes from one address lock it out for 10 minutes. Changing the password signs every other browser out. **Delete all stored information** needs the sign-in and keeps the administrator; the password changes only under Sign-in settings.
- There is no password reset. Only the computer running BombeCam can start over without the password (**Forgot the password? → Shut down and delete saved data**, which deletes all saved data including the administrator); anyone there could delete the profile file anyway. Other devices have no way to do it. The optional password hint is shown only on that computer and may not contain the password.
- The `Host` header is checked against the gateway's own addresses (DNS-rebinding protection), state-changing browser requests must come from the gateway's own origin, and cookie sessions need a CSRF token.

**Streams**
- MediaMTX serves RTSP, HLS and WebRTC on all interfaces so NVRs can connect. By default anyone on the network can read the streams; the **stream password** option requires a user name and password.
- Only BombeCam on the same computer may publish streams or use MediaMTX's management API, which listens on `127.0.0.1` and accepts browser requests only from the gateway's origin.

**Stored data**
- Osaio logins, cameras, settings and BombeCam's router SSH key are kept in an AES-256-GCM encrypted profile. On Windows the key is protected with DPAPI; on Linux it is a file readable only by its owner. Secrets are never written to logs.

**Router (Block cloud video)**
- The router's admin password is used for one SSH connection and never stored.
- BombeCam's SSH key on the router is restricted to the router script's own commands (no shell, no port or agent forwarding, no terminal).
- The router's SSH host key is pinned on first use; BombeCam refuses to connect if it changes.
- The router script accepts only validated MAC addresses and sanitized camera names, and only ever clears connection-tracking entries for checked camera addresses.

**Downloads**
- The only thing BombeCam downloads is MediaMTX (from its official GitHub release, when not installed locally). The archive must match a SHA-256 checksum built into BombeCam or it is not installed.

## Scope and limits

- Block cloud video limits a camera's internet traffic by destination and rate. It cannot inspect encrypted traffic, so it cannot guarantee that no image data leaves through an allowed connection.
- Camera controls go through Osaio's servers; Osaio can see which commands are sent.
- BombeCam does not modify camera firmware.
