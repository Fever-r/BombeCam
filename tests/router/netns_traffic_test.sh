#!/bin/sh
# Real-traffic test of bombecam-router.sh in Linux network namespaces.
#
#   cam (aa:bb:cc:dd:ee:01) ─┐
#   pc  (11:22:33:44:55:66) ─┴─ br-lan ── router ── wan ── "internet"
#   guest (192.168.9.10) ───────── r-guest ─┘
#
# "internet" hosts: 3.1.2.3 (control, :8883), 52.9.9.9 (stream setup, :443),
# 104.16.0.1 (stands in for Backblaze, :443), 1.2.3.4 (stands in for a relay,
# UDP 3478), 8.8.8.8 (DNS, UDP 53), 2001:db8:1::1 (IPv6, :443).
#
# The router runs the real script (not a dry run) against a real kernel, once
# with nftables (fw4 code path) and once with iptables-legacy (fw3 code path).
# Needs root, iproute2, nft, iptables-legacy, conntrack, python3. Usage:
#   sudo sh tests/router/netns_traffic_test.sh [path/to/bombecam-router.sh]
set -u
SCRIPT=$(readlink -f "${1:-$(dirname "$0")/../../pkg/renderer/openwrt/bombecam-router.sh}")
WORK=$(mktemp -d)
PASS=0
FAIL=0
NS="bc_cam bc_pc bc_guest bc_router bc_wan"

cleanup() {
	# Every helper (servers, streams) runs from $WORK; background subshells
	# would otherwise keep them, and this script's output pipe, alive.
	pkill -f "$WORK/" 2>/dev/null
	for n in $NS; do ip netns del "$n" 2>/dev/null; done
	[ -f "$WORK/servers.pid" ] && kill "$(cat "$WORK/servers.pid")" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

ok() { PASS=$((PASS + 1)); echo "  PASS  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }
SKIP=0
HAVE_V6=0
[ -d /proc/sys/net/ipv6 ] && HAVE_V6=1
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
check() { # check "description" expected(0|1) command...
	d=$1; e=$2; shift 2
	if "$@" >/dev/null 2>&1; then r=0; else r=1; fi
	if [ "$r" = "$e" ]; then ok "$d"; else bad "$d"; fi
}
X() { ns=$1; shift; ip netns exec "$ns" "$@"; }

setup() {
	for n in $NS; do ip netns add "$n"; X "$n" ip link set lo up; done
	ip link add r-cam netns bc_router type veth peer name eth0 netns bc_cam
	ip link add r-pc netns bc_router type veth peer name eth0 netns bc_pc
	ip link add r-guest netns bc_router type veth peer name eth0 netns bc_guest
	ip link add r-wan netns bc_router type veth peer name eth0 netns bc_wan
	X bc_cam ip link set eth0 address aa:bb:cc:dd:ee:01
	X bc_pc ip link set eth0 address 11:22:33:44:55:66

	X bc_router ip link add br-lan type bridge
	X bc_router ip link set r-cam master br-lan
	X bc_router ip link set r-pc master br-lan
	for i in br-lan r-cam r-pc r-guest r-wan; do X bc_router ip link set "$i" up; done
	X bc_router ip addr add 192.168.8.1/24 dev br-lan
	[ $HAVE_V6 = 1 ] && X bc_router ip addr add fd00:8::1/64 dev br-lan nodad
	X bc_router ip addr add 192.168.9.1/24 dev r-guest
	X bc_router ip addr add 203.0.113.2/24 dev r-wan
	[ $HAVE_V6 = 1 ] && X bc_router ip addr add 2001:db8:1::2/64 dev r-wan nodad
	X bc_router ip route add default via 203.0.113.1
	[ $HAVE_V6 = 1 ] && X bc_router ip -6 route add default via 2001:db8:1::1
	X bc_router sysctl -qw net.ipv4.ip_forward=1
	[ $HAVE_V6 = 1 ] && X bc_router sysctl -qw net.ipv6.conf.all.forwarding=1
	# The router's own NAT, as on any home router (not part of BombeCam).
	X bc_router nft -f - <<-'EOF'
	table ip home_nat {
		chain post { type nat hook postrouting priority srcnat; oifname "r-wan" masquerade; }
	}
	table inet fw4_like {
		chain forward {
			type filter hook forward priority filter; policy accept;
			ct state established,related accept
		}
	}
	EOF

	for n in bc_cam bc_pc bc_guest bc_wan; do X "$n" ip link set eth0 up; done
	X bc_cam ip addr add 192.168.8.123/24 dev eth0
	[ $HAVE_V6 = 1 ] && X bc_cam ip addr add fd00:8::123/64 dev eth0 nodad
	X bc_cam ip route add default via 192.168.8.1
	[ $HAVE_V6 = 1 ] && X bc_cam ip -6 route add default via fd00:8::1
	X bc_pc ip addr add 192.168.8.50/24 dev eth0
	X bc_pc ip route add default via 192.168.8.1
	X bc_guest ip addr add 192.168.9.10/24 dev eth0
	X bc_guest ip route add default via 192.168.9.1
	X bc_wan ip addr add 203.0.113.1/24 dev eth0
	[ $HAVE_V6 = 1 ] && X bc_wan ip addr add 2001:db8:1::1/64 dev eth0 nodad
	for a in 3.1.2.3 52.9.9.9 104.16.0.1 1.2.3.4 8.8.8.8; do X bc_wan ip addr add "$a/32" dev lo; done
	[ $HAVE_V6 = 1 ] && X bc_wan ip -6 route add fd00:8::/64 via 2001:db8:1::2   # no NAT66: route the camera's ULA back

	cat >"$WORK/servers.py" <<-'EOF'
	import socket, threading, sys, os, time, itertools
	log = sys.argv[1]
	totals = {}          # conn id -> [peer, port, bytes]
	ids = itertools.count()
	def tcp(fam, addr, port):
	    s = socket.socket(fam, socket.SOCK_STREAM)
	    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
	    s.bind((addr, port)); s.listen(64)
	    while True:
	        c, peer = s.accept()
	        threading.Thread(target=sink, args=(c, peer, port), daemon=True).start()
	def sink(c, peer, port):
	    k = next(ids); totals[k] = [peer[0], port, 0]
	    try:
	        while True:
	            b = c.recv(65536)
	            if not b: break
	            totals[k][2] += len(b)
	    except Exception: pass
	def dump():
	    while True:
	        with open(log + ".tmp", "w") as f:
	            for k, v in list(totals.items()): f.write("%d %s %d %d\n" % (k, v[0], v[1], v[2]))
	        os.replace(log + ".tmp", log); time.sleep(0.2)
	def udp(addr, port):
	    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
	    s.bind((addr, port))
	    while True:
	        d, a = s.recvfrom(2048); s.sendto(d, a)
	for p in (8883, 443):
	    threading.Thread(target=tcp, args=(socket.AF_INET, "0.0.0.0", p), daemon=True).start()
	threading.Thread(target=tcp, args=(socket.AF_INET6, "2001:db8:1::1", 443), daemon=True).start()
	for a, p in (("8.8.8.8", 53), ("8.8.8.8", 123), ("1.2.3.4", 3478)):
	    threading.Thread(target=udp, args=(a, p), daemon=True).start()
	threading.Thread(target=dump, daemon=True).start()
	threading.Event().wait()
	EOF
	cat >"$WORK/lan-server.py" <<-'EOF'
	import socket
	s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
	s.bind(("0.0.0.0", 8554)); s.listen(8)
	while True:
	    c, _ = s.accept(); c.close()
	EOF
	cat >"$WORK/client.py" <<-'EOF'
	import socket, sys, time, os
	mode, host, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
	fam = socket.AF_INET6 if ":" in host else socket.AF_INET
	if mode == "tcp":
	    s = socket.socket(fam, socket.SOCK_STREAM); s.settimeout(3)
	    s.connect((host, port)); s.sendall(b"hello"); s.close(); sys.exit(0)
	if mode == "udp":
	    s = socket.socket(fam, socket.SOCK_DGRAM); s.settimeout(2)
	    s.sendto(b"q" * 40, (host, port)); s.recvfrom(100); sys.exit(0)
	if mode == "stream":   # a camera-like UDP stream until killed; echoes received go to a file
	    out = sys.argv[4]
	    s = socket.socket(fam, socket.SOCK_DGRAM); s.setblocking(False)
	    n = 0; last = 0.0
	    while True:
	        try: s.sendto(b"v" * 1000, (host, port))
	        except OSError: pass
	        try:
	            while True: s.recv(2048); n += 1
	        except OSError: pass
	        if time.time() - last > 0.1:
	            open(out + ".tmp", "w").write(str(n)); os.replace(out + ".tmp", out); last = time.time()
	        time.sleep(0.01)
	if mode == "bulk":   # push data for N seconds, print bytes the kernel accepted
	    secs = float(sys.argv[4])
	    s = socket.socket(fam, socket.SOCK_STREAM); s.settimeout(3)
	    s.connect((host, port)); s.setblocking(False)
	    end = time.time() + secs; buf = b"v" * 16384
	    while time.time() < end:
	        try: s.send(buf)
	        except BlockingIOError: time.sleep(0.01)
	    import struct
	    s.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
	    s.close(); sys.exit(0)
	EOF
	X bc_wan python3 "$WORK/servers.py" "$WORK/recv.log" &
	echo $! >"$WORK/servers.pid"
	X bc_pc python3 "$WORK/lan-server.py" &
	X bc_guest python3 "$WORK/lan-server.py" &
	sleep 1
	# The router resolves the allowed names through this fake nslookup.
	mkdir -p "$WORK/bin"
	cat >"$WORK/bin/nslookup" <<-'EOF'
	#!/bin/sh
	case "$1" in
	mqtts02-us.osaio.net) printf 'Name:\t%s\nAddress: 3.1.2.3\n' "$1" ;;
	wss-us.osaio.net) printf 'Name:\t%s\nAddress: 52.9.9.9\n' "$1" ;;
	*) exit 1 ;;
	esac
	EOF
	chmod +x "$WORK/bin/nslookup"
}

# bulk_rate NS HOST PORT SECS -> bytes the server received on new connections
bulk_rate() {
	last=$(awk 'BEGIN { m = -1 } $1 > m { m = $1 } END { print m }' "$WORK/recv.log" 2>/dev/null)
	X "$1" python3 "$WORK/client.py" bulk "$2" "$3" "$4" 2>/dev/null
	sleep 0.5
	awk -v p="$3" -v l="${last:--1}" '$1 > l && $3 == p { s += $4 } END { print s + 0 }' "$WORK/recv.log"
}

matrix() { # matrix <label> <blocking 0|1>
	b=$2
	e() { if [ "$b" = 1 ]; then echo "$1"; else echo 0; fi; }   # expected result when blocking
	echo "[$1]"
	check "camera -> control 3.1.2.3:8883 connects" 0 X bc_cam python3 "$WORK/client.py" tcp 3.1.2.3 8883
	check "camera -> stream setup 52.9.9.9:443 connects" 0 X bc_cam python3 "$WORK/client.py" tcp 52.9.9.9 443
	check "camera -> DNS 8.8.8.8:53/udp answers" 0 X bc_cam python3 "$WORK/client.py" udp 8.8.8.8 53
	check "camera -> NTP 8.8.8.8:123/udp answers" 0 X bc_cam python3 "$WORK/client.py" udp 8.8.8.8 123
	check "camera -> 'Backblaze' 104.16.0.1:443 $( [ "$b" = 1 ] && echo blocked || echo connects)" "$(e 1)" X bc_cam python3 "$WORK/client.py" tcp 104.16.0.1 443
	check "camera -> control host on wrong port 3.1.2.3:443 $( [ "$b" = 1 ] && echo blocked || echo connects)" "$(e 1)" X bc_cam python3 "$WORK/client.py" tcp 3.1.2.3 443
	check "camera -> 'relay' 1.2.3.4:3478/udp $( [ "$b" = 1 ] && echo blocked || echo answers)" "$(e 1)" X bc_cam python3 "$WORK/client.py" udp 1.2.3.4 3478
	if [ $HAVE_V6 = 1 ]; then
		check "camera -> IPv6 [2001:db8:1::1]:443 $( [ "$b" = 1 ] && echo blocked || echo connects)" "$(e 1)" X bc_cam python3 "$WORK/client.py" tcp 2001:db8:1::1 443
	else
		skip "IPv6 checks: this kernel has no IPv6"
	fi
	check "camera -> PC on same LAN 192.168.8.50:8554 connects" 0 X bc_cam python3 "$WORK/client.py" tcp 192.168.8.50 8554
	check "camera -> other local subnet 192.168.9.10:8554 connects" 0 X bc_cam python3 "$WORK/client.py" tcp 192.168.9.10 8554
	check "PC (not a camera) -> 104.16.0.1:443 connects" 0 X bc_pc python3 "$WORK/client.py" tcp 104.16.0.1 443
	n=$(bulk_rate bc_cam 3.1.2.3 8883 10)
	if [ "$b" = 1 ]; then
		# 10 s at 4096 B/s plus the 8192-byte burst, with a little TCP slack.
		if [ "$n" -gt 0 ] && [ "$n" -le 60000 ]; then ok "camera bulk upload on control channel capped: $n bytes in 10 s (~$((n / 10)) B/s)"
		else bad "camera bulk upload on control channel NOT capped: $n bytes in 10 s"; fi
	else
		if [ "$n" -gt 1000000 ]; then ok "without the block the same upload is fast: $n bytes in 10 s"
		else bad "baseline upload unexpectedly slow: $n bytes"; fi
	fi
	if [ "$b" = 1 ]; then
		n=$(bulk_rate bc_pc 104.16.0.1 443 3)
		if [ "$n" -gt 1000000 ]; then ok "PC traffic is not capped: $n bytes in 3 s"; else bad "PC traffic affected: $n bytes"; fi
	fi
}

# connections_clean FW: after blocked and allowed traffic from a clean start,
# the router's connection list shows only allowed categories.
connections_clean() {
	out=$(run_router "$1" connections 2>&1)
	if echo "$out" | grep -q "not_allowed=0" && echo "$out" | grep -q "^    control " &&
		echo "$out" | grep -q "^    stream_setup " && echo "$out" | grep -q "^    dns " &&
		echo "$out" | grep -q "^    time " &&
		! echo "$out" | grep -q "104.16.0.1\|1.2.3.4"; then
		ok "$1 connections: only control, stream setup, DNS, time and local; nothing to 'Backblaze' or the 'relay'"
	else
		bad "$1 connections shows traffic outside the allowlist"; echo "$out"
	fi
}

# A stream the camera opened before blocking: 100 UDP packets/s to the "relay",
# counting the echoes that come back. stream_rate prints echoes per second.
stream_start() {
	rm -f "$WORK/stream.count"
	ip netns exec bc_cam python3 "$WORK/client.py" stream 1.2.3.4 3478 "$WORK/stream.count" &
	sleep 1
}
stream_stop() {
	pkill -f "$WORK/client.py stream" 2>/dev/null
	while pgrep -f "$WORK/client.py stream" >/dev/null; do sleep 0.1; done
}
stream_rate() {
	a=$(cat "$WORK/stream.count" 2>/dev/null || echo 0)
	sleep 1
	b=$(cat "$WORK/stream.count" 2>/dev/null || echo 0)
	echo $((b - a))
}
# relay_tracked: connections the router tracks from the camera to the relay.
relay_tracked() { X bc_router conntrack -L -f ipv4 -s 192.168.8.123 -d 1.2.3.4 2>/dev/null | grep -c 'dport=3478'; }
# stream_cut LABEL: after blocking, the earlier stream gets nothing through and
# the router no longer tracks its connection (it was cleared, not just dropped).
stream_cut() {
	r=$(stream_rate)
	[ "$r" = 0 ] && ok "$1: the stream stopped" || bad "$1: the stream still gets through ($r echoes/s)"
	[ "$(relay_tracked)" = 0 ] && ok "$1: the router no longer tracks the earlier relay connection" ||
		{ bad "$1: the earlier relay connection is still tracked"; X bc_router conntrack -L -s 192.168.8.123 2>/dev/null; }
}

run_router() { # run_router <fw3|fw4> args...
	fw=$1; shift
	X bc_router env PATH="$WORK/bin:$WORK/ipt:$PATH" BC_ROOT="$WORK/root_$fw" BC_FW="$fw" \
		sh "$SCRIPT" "$@"
}

[ "$(id -u)" = 0 ] || { echo "needs root"; exit 2; }
setup
echo "== Baseline (no BombeCam rules): proves the test network routes everything"
matrix "baseline" 0

echo "== fw4 / nftables code path"
mkdir -p "$WORK/root_fw4"
stream_start
r=$(stream_rate)
[ "$r" -gt 20 ] && ok "before blocking, a camera stream to the 'relay' flows ($r echoes/s)" || bad "baseline stream does not flow ($r echoes/s)"
out=$(run_router fw4 apply yes --camera "Test Camera=AA:BB:CC:DD:EE:01" 2>&1)
echo "$out" | grep -q "BOMBECAM_RESULT status=ok block=yes firewall=fw4" && ok "apply yes succeeded" || { bad "apply yes failed"; echo "$out"; }
echo "$out" | grep -q " cleared=yes " && ok "apply cleared the camera's open connections" || { bad "apply did not clear the camera's connections"; echo "$out"; }
stream_cut "fw4, stream opened before blocking"
stream_stop
X bc_router conntrack -F >/dev/null 2>&1   # what a router reboot does: no connections from before the rules
matrix "fw4, Block cloud video = YES" 1
connections_clean fw4
X bc_router nft list chain inet bombecam capped | grep -q 'drop comment "cap: Test Camera"' && ok "per-camera cap rule present" || bad "cap rule missing"
out=$(run_router fw4 status 2>&1)
echo "$out" | grep -q "rules loaded: yes" && echo "$out" | grep -q "BOMBECAM_RESULT status=ok" && ok "status reports the rules loaded" || { bad "status failed"; echo "$out"; }
before=$(X bc_router nft list chain inet bombecam camera_out | grep -c 'counter packets [1-9]')
run_router fw4 refresh >/dev/null 2>&1
after=$(X bc_router nft list chain inet bombecam camera_out | grep -c 'counter packets [1-9]')
[ "$before" -gt 0 ] && [ "$before" = "$after" ] && ok "refresh with unchanged addresses leaves rules and counters alone" || bad "refresh reloaded unnecessarily ($before -> $after)"
X bc_router nft delete table inet bombecam
run_router fw4 refresh >/dev/null 2>&1
X bc_router nft list table inet bombecam >/dev/null 2>&1 && ok "refresh re-loads rules that went missing" || bad "refresh did not self-heal"
out=$(run_router fw4 apply no 2>&1)
echo "$out" | grep -q "block=no" && ok "apply no succeeded" || { bad "apply no failed"; echo "$out"; }
echo "$out" | grep -q "residue=0" && ok "apply no checked the live firewall: nothing left" || { bad "apply no did not confirm a clean firewall"; echo "$out"; }
X bc_router nft list table inet bombecam >/dev/null 2>&1 && bad "table still present after apply no" || ok "table removed"
run_router fw4 status 2>&1 | grep -q "residue=0" && ok "status reports no BombeCam rules left" || bad "status does not report a clean firewall"
matrix "fw4, after apply no" 0

echo "== fw3 / iptables (legacy) code path"
X bc_router nft delete table inet fw4_like
mkdir -p "$WORK/ipt" "$WORK/root_fw3"
for t in iptables ip6tables; do
	ln -sf "$(command -v $t-legacy)" "$WORK/ipt/$t"
	ln -sf "$(command -v $t-legacy-restore)" "$WORK/ipt/$t-restore"
done
# Mimic fw3: established accept in FORWARD, then zone forwarding.
X bc_router "$WORK/ipt/iptables" -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
X bc_router "$WORK/ipt/iptables" -A FORWARD -i br-lan -j ACCEPT
stream_start
out=$(run_router fw3 apply yes --camera "Test Camera=AA:BB:CC:DD:EE:01" 2>&1)
echo "$out" | grep -q "BOMBECAM_RESULT status=ok block=yes firewall=fw3 cameras=1 cap=bytes" && ok "apply yes succeeded (byte cap via hashlimit)" || { bad "apply yes failed"; echo "$out"; }
echo "$out" | grep -q " cleared=yes " && ok "apply cleared the camera's open connections" || { bad "apply did not clear the camera's connections"; echo "$out"; }
X bc_router "$WORK/ipt/iptables" -S FORWARD | head -n 2 | grep -q 'bombecam_out' && ok "jump sits above the established-accept rule" || bad "jump not at the top of FORWARD"
stream_cut "fw3, stream opened before blocking"
stream_stop
# The camera's connections from the "after apply no" run (Backblaze, relay)
# were cleared by apply: nothing outside the allowed list is left.
out=$(run_router fw3 connections 2>&1)
if echo "$out" | grep -q "not_allowed=0" && ! echo "$out" | grep -q "tcp 104.16.0.1:443"; then
	ok "connections: the camera's connections from before the rules were cleared"
else
	bad "connections still lists connections from before the rules"; echo "$out"
fi
X bc_router conntrack -F >/dev/null 2>&1
matrix "fw3, Block cloud video = YES" 1
connections_clean fw3
out=$(run_router fw3 status 2>&1)
echo "$out" | grep -q "rules loaded: yes" && echo "$out" | grep -q "allowed, under cap" && ok "fw3 status reports rules and counters" || { bad "fw3 status failed"; echo "$out"; }
# Simulate fw3 flushing FORWARD on reload, then the include putting rules back.
# A connection the camera opens while the rules are missing is cleared by load.
X bc_router "$WORK/ipt/iptables" -D FORWARD -m mac --mac-source aa:bb:cc:dd:ee:01 -j bombecam_out
stream_start
r=$(stream_rate)
[ "$r" -gt 20 ] && ok "while a firewall reload has removed the rules, a new camera stream flows ($r echoes/s)" || bad "stream did not flow without the rules ($r echoes/s)"
run_router fw3 load >/dev/null 2>&1
X bc_router "$WORK/ipt/iptables" -C FORWARD -m mac --mac-source aa:bb:cc:dd:ee:01 -j bombecam_out 2>/dev/null && ok "load restores the jump after a firewall reload" || bad "load did not restore the jump"
stream_cut "fw3, stream opened during a firewall reload"
stream_stop
# Traffic that gets past the rules (here an accept placed above BombeCam's
# jump) must make apply fail instead of reporting the camera blocked.
X bc_router "$WORK/ipt/iptables" -I FORWARD 1 -s 192.168.8.123 -d 1.2.3.4 -j ACCEPT
stream_start
out=$(run_router fw3 apply yes 2>&1)
if echo "$out" | grep -q "BOMBECAM_RESULT status=error" && echo "$out" | grep -q "got past the rules" && echo "$out" | grep -q "udp 1.2.3.4:3478"; then
	ok "apply reports a camera connection that gets past the rules instead of claiming success"
else
	bad "apply did not report a connection getting past the rules"; echo "$out"
fi
stream_stop
X bc_router "$WORK/ipt/iptables" -D FORWARD -s 192.168.8.123 -d 1.2.3.4 -j ACCEPT
# a second jump of another shape (as a firewall reload or an older script
# could leave): "apply no" must remove it too, not just the jumps it made
X bc_router "$WORK/ipt/iptables" -I FORWARD -i br-lan -m mac --mac-source AA:BB:CC:DD:EE:01 -j bombecam_out
X bc_router "$WORK/ipt/iptables" -I FORWARD -m mac --mac-source aa:bb:cc:dd:ee:01 -j bombecam_out
out=$(run_router fw3 apply no 2>&1)
echo "$out" | grep -q "block=no" && ok "apply no succeeded" || { bad "apply no failed"; echo "$out"; }
echo "$out" | grep -q "residue=0" && ok "apply no checked the live firewall: nothing left" || { bad "apply no did not confirm a clean firewall"; echo "$out"; }
X bc_router "$WORK/ipt/iptables" -S 2>/dev/null | grep -q bombecam && bad "iptables rules left behind" || ok "iptables rules removed (including extra jumps)"
run_router fw3 status 2>&1 | grep -q "residue=0" && ok "status reports no BombeCam rules left" || bad "status does not report a clean firewall"
matrix "fw3, after apply no" 0

echo
echo "Result: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" = 0 ]
