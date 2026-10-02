package netstack

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Local NTP Server Responder.
// Simple UDP port 123 responder answering
// camera time synchronization queries with valid monotonic/NTP timestamps,
// preventing the camera's ~180s firmware watchdog reset.

// ntpEpochOffset is the number of seconds between the NTP epoch (1900-01-01)
// and Unix epoch (1970-01-01): 70 years + 17 leap days = 2,208,988,800 seconds.
const ntpEpochOffset = 2208988800

// NTPServer is a standalone RFC 5905 UDP time responder.
type NTPServer struct {
	ListenAddr     string
	RequestsServed int64

	mu       sync.Mutex
	conn     *net.UDPConn
	running  bool
	stopChan chan struct{}
}

// NewNTPServer creates a new NTP server instance.
func NewNTPServer(listenAddr string) *NTPServer {
	if listenAddr == "" {
		listenAddr = ":123"
	}
	return &NTPServer{
		ListenAddr: listenAddr,
		stopChan:   make(chan struct{}),
	}
}

// Start binds the UDP port and starts serving NTP client queries.
func (s *NTPServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	udpAddr, err := net.ResolveUDPAddr("udp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("resolve udp addr %q: %w", s.ListenAddr, err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp %q: %w", s.ListenAddr, err)
	}

	s.conn = conn
	s.running = true
	s.stopChan = make(chan struct{})

	go s.serve()
	return nil
}

// Stop terminates the NTP responder and closes the socket.
func (s *NTPServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	s.running = false
	close(s.stopChan)
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return nil
}

// Addr returns the bound UDP address of the NTP server.
func (s *NTPServer) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.LocalAddr()
	}
	return nil
}

// serve receives NTP requests and dispatches responses.
func (s *NTPServer) serve() {
	buf := make([]byte, 512)
	for {
		select {
		case <-s.stopChan:
			return
		default:
		}

		n, remoteAddr, err := s.conn.ReadFrom(buf)
		rxTime := time.Now()
		if err != nil {
			select {
			case <-s.stopChan:
				return
			default:
				continue
			}
		}

		resp, err := s.ProcessNTPRequest(buf[:n], rxTime)
		if err == nil && resp != nil {
			atomic.AddInt64(&s.RequestsServed, 1)
			_, _ = s.conn.WriteTo(resp, remoteAddr)
		}
	}
}

// ProcessNTPRequest generates a valid Mode 4 (Server) datagram responding to a client query.
// Valid NTP Mode 4 datagram satisfying camera watchdog timer.
func (s *NTPServer) ProcessNTPRequest(req []byte, rxTime time.Time) ([]byte, error) {
	if len(req) < 48 {
		return nil, fmt.Errorf("ntp datagram too short: %d bytes (minimum 48)", len(req))
	}

	// Client mode check (byte 0 bits 0-2: Mode 3 = Client)
	clientMode := req[0] & 0x07
	if clientMode != 3 && clientMode != 0 {
		// Accept Mode 3 (Client) or Mode 0 (reserved / some simple clients)
		return nil, fmt.Errorf("invalid client ntp mode: %d", clientMode)
	}

	resp := make([]byte, 48)

	// Byte 0: LI = 0 (no warning), VN = 4 (NTPv4), Mode = 4 (Server) -> 0x24
	resp[0] = 0x24
	// Byte 1: Stratum = 2 (secondary reference, synchronized to local host clock)
	resp[1] = 0x02
	// Byte 2: Poll interval (4 = 16 seconds)
	resp[2] = 0x04
	// Byte 3: Precision (-20, ~1 microsecond) -> 0xec
	resp[3] = 0xec

	// Bytes 4-7: Root Delay = 0
	// Bytes 8-11: Root Dispersion = 0 (minimal error)

	// Bytes 12-15: Reference Identifier = "BOMB"
	copy(resp[12:16], []byte("BOMB"))

	// Reference Timestamp: current time minus 1 second
	refTime := rxTime.Add(-1 * time.Second)
	putNTPTimestamp(resp[16:24], refTime)

	// Originate Timestamp: copy directly from client Transmit Timestamp (bytes 40-47 of request)
	copy(resp[24:32], req[40:48])

	// Receive Timestamp: time query was received
	putNTPTimestamp(resp[32:40], rxTime)

	// Transmit Timestamp: time response was transmitted
	txTime := time.Now()
	putNTPTimestamp(resp[40:48], txTime)

	return resp, nil
}

// putNTPTimestamp encodes a time.Time into an 8-byte NTP fixed-point timestamp.
func putNTPTimestamp(b []byte, t time.Time) {
	secs := uint32(t.Unix() + ntpEpochOffset)
	nanos := uint64(t.Nanosecond())
	// Fraction: (nanos * 2^32) / 10^9
	frac := uint32((nanos << 32) / 1000000000)

	binary.BigEndian.PutUint32(b[0:4], secs)
	binary.BigEndian.PutUint32(b[4:8], frac)
}
