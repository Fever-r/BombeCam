#!/bin/sh
# Extract the exact, checksummed gateway download for a Docker target.
set -eu

case "$1" in
    amd64|arm64) arch="$1" ;;
    arm) arch=armv6 ;; # The armv6 binary also runs on armv7.
    *) echo "unsupported gateway architecture: $1" >&2; exit 1 ;;
esac
downloads="$2"
out="$3"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
cd "$downloads"

set -- BombeCam-*-linux-"$arch".tar.gz
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "the downloads need exactly one BombeCam-*-linux-$arch.tar.gz" >&2
    exit 1
fi
archive="$1"
sum="$(awk -v archive="$archive" '$2 == archive { print $1 }' SHA256SUMS.txt)"
case "$sum" in
    ''|*[!0-9a-fA-F]*) echo "missing or invalid checksum for $archive" >&2; exit 1 ;;
esac
[ "${#sum}" -eq 64 ] || { echo "need exactly one SHA-256 checksum for $archive" >&2; exit 1; }
printf '%s  %s\n' "$sum" "$archive" | sha256sum -c -

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
tar -xzf "$archive" -C "$scratch" "${archive%.tar.gz}/bombecam-gateway"
mv "$scratch/${archive%.tar.gz}/bombecam-gateway" "$out/"
