#!/bin/sh
# ==============================================================================
# BombeCam - Early Boot Firewall Guard (Linux router with bombecam-net)
# Order: Runs before camera network interface is brought up (e.g. systemd
# network-pre.target or /etc/network/if-pre-up.d/bombecam-guard).
# Purpose: Installs an immediate fail-closed forward drop for the camera interface
# so that the camera network is born blocked before any routing can happen.
# It uses bombecam-net's own table name, so when the daemon applies the saved
# "Block cloud video" setting it replaces this guard in one transaction.
# ==============================================================================

set -e

DEFAULTS_FILE="${BOMBECAM_DEFAULTS_FILE:-/etc/default/bombecam}"
if [ -z "$CAMERA_VLAN_IF" ] && [ -f "$DEFAULTS_FILE" ]; then
    . "$DEFAULTS_FILE"
fi
CAM_IF="${CAMERA_VLAN_IF}"
case "$CAM_IF" in
    ''|*[!A-Za-z0-9_.:@-]*|lo)
        echo "[!] FATAL: set CAMERA_VLAN_IF to the actual camera interface; no interface is guessed." >&2
        exit 1 ;;
esac
if [ "${#CAM_IF}" -gt 15 ] || ! command -v ip >/dev/null 2>&1 || ! ip link show dev "$CAM_IF" >/dev/null 2>&1; then
    echo "[!] FATAL: configured camera interface is invalid or does not exist: $CAM_IF" >&2
    exit 1
fi

if ! command -v nft >/dev/null 2>&1; then
    echo "[!] FATAL: nftables not installed. Refusing to bring up camera network." >&2
    exit 1
fi

echo "[+] Installing early boot forward drop guard for ${CAM_IF}..."

# Create table and forward chain if not present, then insert camera forward drop
nft -f - <<EOF
table inet bombecam
delete table inet bombecam
table inet bombecam {
    chain forward {
        type filter hook forward priority filter - 5; policy accept;
        # Non-camera transit is never touched
        iifname != "${CAM_IF}" accept
        # Immediate camera forward drop across IPv4 and IPv6
        iifname "${CAM_IF}" counter drop
    }
}
EOF

echo "[+] Forward drop installed for configured camera interface ${CAM_IF}."
exit 0
