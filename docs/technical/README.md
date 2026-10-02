# Technical documentation

These pages explain why BombeCam needs the Osaio server key and app ID, how to swap them, how the code is divided, and what to do when the vendor changes something. They are for people who run, maintain or fork BombeCam.

| Page | Read it when you want to |
|---|---|
| [CONFIGURATION.md](CONFIGURATION.md) | Understand the server key and the app ID, replace them, or go back to the built-in ones |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Work out why sign-in, streams or controls stopped working |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Find where configuration, the cloud client, device control and media live |
| [MAINTAINERS.md](MAINTAINERS.md) | Run the tests, cut a release, or handle a vendor change in code |
| [MODULARIZATION_PLAN.md](MODULARIZATION_PLAN.md) | See what has been separated already and what is left |

Release steps are in [../releasing.md](../releasing.md). Contribution rules are in [CONTRIBUTING.md](../../CONTRIBUTING.md). Warranty and affiliation terms are in [DISCLAIMER.md](../../DISCLAIMER.md).

## What these pages do not contain

- The key and app ID values. They are not in the code either: builds take them from `osaio-setup.txt` or GitHub Actions secrets ([CONFIGURATION.md](CONFIGURATION.md#building-with-a-key)); the web page never shows them.
- Vendor server addresses, message formats or the signing method. Those live in the code (`pkg/bridge`).
- Account details, device identifiers, network addresses or test data from anyone's setup. Examples use placeholders.

## How sure are these pages?

Each statement is one of:

- **Done:** in the code on this branch and covered by a test named next to it.
- **Planned:** described in [MODULARIZATION_PLAN.md](MODULARIZATION_PLAN.md), not built yet.
- **Unknown:** the source material does not say. Written as "not known".

Tests ran against mock services only. No statement here is a claim about the vendor's current servers or about camera hardware.

## Open questions

- **Key format and rotation.** It is not known what format the vendor's key or app ID must have, how often they change, or how a new one is announced. BombeCam therefore accepts any single-line value for either and does not check it against a pattern. The built-in key and app ID are the ones known when the version was released.
- **Telling a bad key or app ID from a bad password.** It is not known whether the vendor answers a wrongly signed request, or one with an outdated app ID, differently from a wrong password. BombeCam reports all of them as "Osaio rejected the sign-in" and names the password, the key and the app ID.
- **Other client identifiers.** The vendor may change its app ID or app version together with the key. A new app ID needs no code change: users paste it under **Settings → App ID** at once, and maintainers update the `OSAIO_APP_ID` secret and `osaio-setup.txt` for the next release. A new app version (`userAgent` in `pkg/bridge/cloud.go`) does need one (see [MAINTAINERS.md](MAINTAINERS.md#when-the-vendor-changes-something)).
