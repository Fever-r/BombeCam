#!/bin/sh
# ==============================================================================
# BombeCam - Persistent Boot Guard Installer
# Installs bombecam-guard.sh to /usr/local/bin/bombecam-guard.sh,
# sets up systemd bombecam-guard.service for network-pre.target,
# and creates the /etc/network/if-pre-up.d/bombecam-guard interface hook.
# ==============================================================================
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Validate before writing privileged configuration or installing any service.
DEFAULTS_FILE="${BOMBECAM_DEFAULTS_FILE:-/etc/default/bombecam}"
if [ -z "$CAMERA_VLAN_IF" ] && [ -f "$DEFAULTS_FILE" ]; then . "$DEFAULTS_FILE"; fi
case "$CAMERA_VLAN_IF" in
    ''|*[!A-Za-z0-9_.:@-]*|lo) echo "[!] Set CAMERA_VLAN_IF to the actual camera interface." >&2; exit 1 ;;
esac
if [ "${#CAMERA_VLAN_IF}" -gt 15 ] || ! command -v ip >/dev/null 2>&1 || ! ip link show dev "$CAMERA_VLAN_IF" >/dev/null 2>&1; then
    echo "[!] Configured camera interface does not exist: $CAMERA_VLAN_IF" >&2
    exit 1
fi

mkdir -p /usr/local/bin /etc/network/if-pre-up.d /etc/systemd/system
mkdir -p /etc/default

# Persist the camera interface so the boot-time guard and the interface hook
# both know it.
if [ -n "${CAMERA_VLAN_IF}" ]; then
    echo "[+] Recording CAMERA_VLAN_IF=${CAMERA_VLAN_IF} in /etc/default/bombecam"
    touch "$DEFAULTS_FILE"
    grep -v '^CAMERA_VLAN_IF=' "$DEFAULTS_FILE" > "$DEFAULTS_FILE.tmp" || true
    echo "CAMERA_VLAN_IF=${CAMERA_VLAN_IF}" >> "$DEFAULTS_FILE.tmp"
    mv "$DEFAULTS_FILE.tmp" "$DEFAULTS_FILE"
elif ! grep -q '^CAMERA_VLAN_IF=' "$DEFAULTS_FILE" 2>/dev/null; then
    echo "[!] CAMERA_VLAN_IF is not set. Re-run as: sudo CAMERA_VLAN_IF=<camera interface> sh $0" >&2
    exit 1
fi


echo "[+] Installing /usr/local/bin/bombecam-guard.sh..."
cp "${SCRIPT_DIR}/bombecam-guard.sh" /usr/local/bin/bombecam-guard.sh
chmod +x /usr/local/bin/bombecam-guard.sh

if [ -d /etc/systemd/system ]; then
    echo "[+] Installing systemd unit /etc/systemd/system/bombecam-guard.service..."
    cp "${ROOT_DIR}/deploy/systemd/bombecam-guard.service" /etc/systemd/system/bombecam-guard.service
    if command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload
        systemctl enable bombecam-guard.service
        echo "[+] Enabled bombecam-guard.service for systemd network-pre.target"
    fi
fi

echo "[+] Installing /etc/network/if-pre-up.d/bombecam-guard hook..."
cat << 'HOOK' > /etc/network/if-pre-up.d/bombecam-guard
#!/bin/sh
[ -f /etc/default/bombecam ] && . /etc/default/bombecam
[ -n "$CAMERA_VLAN_IF" ] || { echo "[!] Camera interface is not configured." >&2; exit 1; }
if [ "$IFACE" = "$CAMERA_VLAN_IF" ]; then
    /usr/local/bin/bombecam-guard.sh
fi
HOOK
chmod +x /etc/network/if-pre-up.d/bombecam-guard
echo "[+] Installed /etc/network/if-pre-up.d/bombecam-guard hook successfully"
echo "[+] BombeCam persistent boot guard installation complete."
