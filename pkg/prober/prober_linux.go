//go:build linux

package prober

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------- L2 attribution

// AttributeL2 listens on the camera link for a frame that is unambiguously the
// camera trying to leave the segment through us: its source MAC, our MAC as the
// next hop, an off-subnet destination, and the TTL we learned with blocking off.
//
// This is the evidence that separates ENFORCED_PROVEN from ENFORCED_OBSERVED.
// It is never assumed.
func (p *Prober) AttributeL2(ctx context.Context, cameraMAC string, learnedTTL int) (bool, error) {
	iface, err := net.InterfaceByName(p.cfg.CameraInterface)
	if err != nil {
		return false, fmt.Errorf("camera interface %s: %w", p.cfg.CameraInterface, err)
	}
	wantSrc, err := net.ParseMAC(strings.TrimSpace(cameraMAC))
	if err != nil {
		return false, fmt.Errorf("invalid camera MAC %q: %w", cameraMAC, err)
	}
	_, subnet, err := net.ParseCIDR(p.cfg.CameraSubnet)
	if err != nil {
		return false, err
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		return false, fmt.Errorf("cannot open a capture socket (needs root / CAP_NET_RAW): %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ALL),
		Ifindex:  iface.Index,
	}); err != nil {
		return false, fmt.Errorf("cannot bind the capture socket to %s: %w", p.cfg.CameraInterface, err)
	}

	deadline := time.Now().Add(p.cfg.CaptureWindow)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	tv := syscall.NsecToTimeval(int64(500 * time.Millisecond))
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		n, _, rerr := syscall.Recvfrom(fd, buf, 0)
		if rerr != nil || n < 34 {
			continue
		}
		f := buf[:n]
		dstMAC, srcMAC := f[0:6], f[6:12]
		if !macEqual(srcMAC, wantSrc) {
			continue
		}
		if !macEqual(dstMAC, iface.HardwareAddr) {
			continue // not being routed through us
		}
		etherType := binary.BigEndian.Uint16(f[12:14])
		if etherType != 0x0800 || n < 34 {
			continue // IPv4 only for the TTL check
		}
		ip := f[14:]
		ttl := int(ip[8])
		dst := net.IP(ip[16:20])
		if subnet.Contains(dst) {
			continue // intra-segment traffic proves nothing about egress
		}
		if learnedTTL > 0 && ttl != learnedTTL {
			continue // not a first-hop frame from the camera
		}
		return true, nil
	}
	return false, nil
}

// LearnTTL observes the camera's initial TTL, to be called with blocking off.
func (p *Prober) LearnTTL(ctx context.Context, cameraMAC string) (int, error) {
	iface, err := net.InterfaceByName(p.cfg.CameraInterface)
	if err != nil {
		return 0, err
	}
	wantSrc, err := net.ParseMAC(strings.TrimSpace(cameraMAC))
	if err != nil {
		return 0, err
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		return 0, fmt.Errorf("cannot open a capture socket (needs root / CAP_NET_RAW): %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ALL), Ifindex: iface.Index,
	}); err != nil {
		return 0, err
	}
	tv := syscall.NsecToTimeval(int64(500 * time.Millisecond))
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	deadline := time.Now().Add(p.cfg.CaptureWindow)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		n, _, rerr := syscall.Recvfrom(fd, buf, 0)
		if rerr != nil || n < 34 {
			continue
		}
		f := buf[:n]
		if !macEqual(f[6:12], wantSrc) {
			continue
		}
		if binary.BigEndian.Uint16(f[12:14]) != 0x0800 {
			continue
		}
		return int(f[14+8]), nil
	}
	return 0, fmt.Errorf("no IPv4 frame seen from %s within %s", cameraMAC, p.cfg.CaptureWindow)
}

// ---------------------------------------------------------------- helpers

func macEqual(a []byte, b net.HardwareAddr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func htons(v uint16) uint16 { return (v<<8)&0xff00 | v>>8 }
