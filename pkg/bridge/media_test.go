package bridge

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBuildFFmpegArgs_HasAudio(t *testing.T) {
	audioURL := "tcp://127.0.0.1:54321?timeout=5000000"
	rtspURL := "rtsp://127.0.0.1:8554/test-cam"

	args := buildFFmpegArgs(true, audioURL, rtspURL)

	// Verify no inherited pipe:3
	for _, arg := range args {
		if strings.Contains(arg, "pipe:3") {
			t.Fatalf("unexpected pipe:3 in args: %v", args)
		}
		if arg == "-an" {
			t.Fatalf("unexpected -an flag when hasAudio is true: %v", args)
		}
	}

	// Verify video input 0 is pipe:0
	foundVideoInput := false
	for i := 0; i < len(args)-3; i++ {
		if args[i] == "-f" && args[i+1] == "h264" && args[i+2] == "-i" && args[i+3] == "pipe:0" {
			foundVideoInput = true
			break
		}
	}
	if !foundVideoInput {
		t.Fatalf("expected '-f h264 -i pipe:0' in args: %v", args)
	}

	// Verify audio input 1 is audioURL
	foundAudioInput := false
	for i := 0; i < len(args)-3; i++ {
		if args[i] == "-f" && args[i+1] == "aac" && args[i+2] == "-i" && args[i+3] == audioURL {
			foundAudioInput = true
			break
		}
	}
	if !foundAudioInput {
		t.Fatalf("expected '-f aac -i %s' in args: %v", audioURL, args)
	}

	// Verify RTSP destination is final argument
	if args[len(args)-1] != rtspURL {
		t.Fatalf("expected final argument to be %s, got %s", rtspURL, args[len(args)-1])
	}
}

func TestBuildFFmpegArgs_NoAudio(t *testing.T) {
	rtspURL := "rtsp://127.0.0.1:8554/noaudio-cam"

	args := buildFFmpegArgs(false, "", rtspURL)

	hasAN := false
	for _, arg := range args {
		if arg == "-an" {
			hasAN = true
		}
		if strings.Contains(arg, "aac") || strings.Contains(arg, "tcp://") {
			t.Fatalf("unexpected audio argument in no-audio args: %s", arg)
		}
	}
	if !hasAN {
		t.Fatalf("expected -an flag in no-audio args: %v", args)
	}

	if args[len(args)-1] != rtspURL {
		t.Fatalf("expected final argument to be %s, got %s", rtspURL, args[len(args)-1])
	}
}

func TestAudioLoopback_LifecycleAndTransmission(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	silenceFrame := []byte{0xFF, 0xF1, 0x6C, 0x40, 0x01, 0x7F, 0xFC, 0x01, 0x18, 0x20, 0x07}
	testFrames := [][]byte{
		{0xFF, 0xF1, 0x6C, 0x40, 0x02, 0x7F, 0xFC, 0xAA, 0xBB},
		{0xFF, 0xF1, 0x6C, 0x40, 0x02, 0x7F, 0xFC, 0xCC, 0xDD},
		{0xFF, 0xF1, 0x6C, 0x40, 0x02, 0x7F, 0xFC, 0xEE, 0xFF},
	}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		conn, dialErr := net.Dial("tcp", addr)
		if dialErr != nil {
			t.Errorf("client dial failed: %v", dialErr)
			return
		}
		defer conn.Close()

		// Read primed silence frame
		silenceBuf := make([]byte, len(silenceFrame))
		if _, rErr := io.ReadFull(conn, silenceBuf); rErr != nil {
			t.Errorf("failed to read silence frame: %v", rErr)
			return
		}
		if !bytes.Equal(silenceBuf, silenceFrame) {
			t.Errorf("silence frame mismatch: got %v, want %v", silenceBuf, silenceFrame)
			return
		}

		// Read data frames
		for i, expected := range testFrames {
			buf := make([]byte, len(expected))
			if _, rErr := io.ReadFull(conn, buf); rErr != nil {
				t.Errorf("failed to read test frame %d: %v", i, rErr)
				return
			}
			if !bytes.Equal(buf, expected) {
				t.Errorf("frame %d mismatch: got %v, want %v", i, buf, expected)
			}
		}

		// Verify EOF after server close
		extra := make([]byte, 10)
		n, eofErr := conn.Read(extra)
		if eofErr != io.EOF && n != 0 {
			t.Errorf("expected EOF, got n=%d, err=%v", n, eofErr)
		}
	}()

	serverConn, acceptErr := listener.Accept()
	if acceptErr != nil {
		t.Fatalf("server accept failed: %v", acceptErr)
	}

	// Prime silence frame
	if _, wErr := serverConn.Write(silenceFrame); wErr != nil {
		t.Fatalf("failed to write silence frame: %v", wErr)
	}

	// Transmit test frames
	for _, f := range testFrames {
		if _, wErr := serverConn.Write(f); wErr != nil {
			t.Fatalf("failed to write frame: %v", wErr)
		}
	}

	// Close server side to signal EOF
	_ = serverConn.Close()

	select {
	case <-clientDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for client to finish reading loopback audio")
	}
}

func TestAudioLoopback_AcceptTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}
	defer listener.Close()

	tcpL, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatalf("expected *net.TCPListener")
	}

	// Set short deadline
	_ = tcpL.SetDeadline(time.Now().Add(50 * time.Millisecond))

	_, acceptErr := tcpL.Accept()
	if acceptErr == nil {
		t.Fatal("expected accept timeout error, got nil")
	}

	netErr, ok := acceptErr.(net.Error)
	if !ok || !netErr.Timeout() {
		t.Fatalf("expected net.Error timeout, got: %v", acceptErr)
	}
}
