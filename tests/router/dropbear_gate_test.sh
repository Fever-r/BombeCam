#!/bin/sh
# Runs tests/router/dropbear_gate_test.go against a real dropbear SSH server
# (the server OpenWrt and GL.iNet ship). Needs root, dropbear and Go.
#   sudo sh tests/router/dropbear_gate_test.sh
set -eu
cd "$(dirname "$0")/../.."
[ "$(id -u)" = 0 ] || { echo "needs root"; exit 2; }
command -v dropbear >/dev/null || { echo "needs dropbear (apt install dropbear-bin)"; exit 2; }
WORK=$(mktemp -d)
USER_NAME=bctest$$
PORT=$((20000 + $$ % 10000))
cleanup() {
	for p in "$WORK"/dropbear*.pid; do [ -f "$p" ] && kill "$(cat "$p")" 2>/dev/null; done || true
	userdel "$USER_NAME" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
useradd -M -d "$WORK/home" -s /bin/sh "$USER_NAME"
echo "$USER_NAME:router-pass" | chpasswd
mkdir -p "$WORK/home/.ssh" "$WORK/root"
echo 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOTHERKEYOTHERKEYOTHERKEYOTHERKEYOTHERKEY user@laptop' >"$WORK/home/.ssh/authorized_keys"
chmod 700 "$WORK/home/.ssh"
chmod 600 "$WORK/home/.ssh/authorized_keys"
chown -R "$USER_NAME" "$WORK/home" "$WORK/root"
chmod 755 "$WORK"
dropbearkey -t ed25519 -f "$WORK/hostkey_ed25519" >/dev/null 2>&1
dropbearkey -t rsa -s 2048 -f "$WORK/hostkey_rsa" >/dev/null 2>&1   # OpenWrt ships both
dropbear -F -E -r "$WORK/hostkey_ed25519" -r "$WORK/hostkey_rsa" -p "127.0.0.1:$PORT" -P "$WORK/dropbear.pid" 2>"$WORK/dropbear.log" &
dropbear -F -E -r "$WORK/hostkey_ed25519" -p "127.0.0.1:$((PORT + 1))" -P "$WORK/dropbear2.pid" 2>>"$WORK/dropbear.log" &
sleep 1
BOMBECAM_DROPBEAR_ED25519_ADDR="127.0.0.1:$((PORT + 1))" BOMBECAM_DROPBEAR_ADDR="127.0.0.1:$PORT" BOMBECAM_DROPBEAR_USER="$USER_NAME" BOMBECAM_DROPBEAR_PASS=router-pass \
	BOMBECAM_DROPBEAR_ROOT="$WORK/root" BOMBECAM_DROPBEAR_AUTHKEYS="$WORK/home/.ssh/authorized_keys" \
	go test -count=1 -v -run TestDropbear ./tests/router/ || { echo "--- dropbear log"; cat "$WORK/dropbear.log"; exit 1; }
