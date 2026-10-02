//go:build !windows

package profile

import (
	"errors"
)

var (
	// ErrDPAPINotSupported is returned when DPAPI is invoked on non-Windows platforms.
	ErrDPAPINotSupported = errors.New("DPAPI is only supported on Windows")
)

// ProtectKeyDPAPI is a no-op on non-Windows platforms.
func ProtectKeyDPAPI(data []byte) ([]byte, error) {
	return nil, ErrDPAPINotSupported
}

// UnprotectKeyDPAPI is a no-op on non-Windows platforms.
func UnprotectKeyDPAPI(data []byte) ([]byte, error) {
	return nil, ErrDPAPINotSupported
}

// DPAPIAvailable returns false on non-Windows platforms.
func DPAPIAvailable() bool {
	return false
}
