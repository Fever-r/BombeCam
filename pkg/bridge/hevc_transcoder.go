package bridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Fever-r/BombeCam/internal/winproc"
)

const hevcInputQueue = 120

var hevcAnnexBStartCode = []byte{0, 0, 0, 1}

// hevcTranscoder converts only cameras whose firmware sends actual HEVC NALs
// to H.264 for BombeCam's existing RTSP/HLS/WebRTC output contract.
type hevcTranscoder struct {
	label  string
	cmd    *exec.Cmd
	video  io.WriteCloser
	listen net.Listener

	videoQueue chan []byte
	audioQueue chan []byte
	done       chan struct{}
	once       sync.Once
}

func buildHEVCTranscoderArgs(hasAudio bool, audioURL, rtspURL string) []string {
	args := []string{
		"-hide_banner", "-loglevel", "warning", "-y",
		"-thread_queue_size", "10240", "-fflags", "+genpts",
		"-use_wallclock_as_timestamps", "1", "-f", "hevc", "-i", "pipe:0",
	}
	if hasAudio {
		args = append(args,
			"-thread_queue_size", "10240", "-fflags", "+genpts", "-f", "aac", "-i", audioURL,
			"-map", "0:v:0", "-map", "1:a:0", "-map", "1:a:0",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
			"-c:a:0", "aac", "-b:a:0", "32k", "-ar:a:0", "8000", "-ac:a:0", "1",
			"-c:a:1", "pcm_mulaw", "-ar:a:1", "8000", "-ac:a:1", "1",
		)
	} else {
		args = append(args,
			"-map", "0:v:0", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-an",
		)
	}
	return append(args, "-f", "rtsp", "-rtsp_transport", "tcp", rtspURL)
}

func startHEVCTranscoder(ffmpeg, label, rtspURL string, hasAudio bool) (*hevcTranscoder, error) {
	h := &hevcTranscoder{
		label:      label,
		videoQueue: make(chan []byte, hevcInputQueue),
		done:       make(chan struct{}),
	}
	var audioURL string
	if hasAudio {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("HEVC audio loopback listener: %w", err)
		}
		h.listen = l
		h.audioQueue = make(chan []byte, hevcInputQueue)
		audioURL = "tcp://" + l.Addr().String()
	}

	args := buildHEVCTranscoderArgs(hasAudio, audioURL, rtspURL)
	cmd := exec.CommandContext(context.Background(), ffmpeg, args...)
	winproc.Hide(cmd)
	cmd.Stderr, cmd.Stdout = os.Stderr, os.Stdout
	video, err := cmd.StdinPipe()
	if err != nil {
		if h.listen != nil {
			_ = h.listen.Close()
		}
		return nil, fmt.Errorf("HEVC transcoder stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = video.Close()
		if h.listen != nil {
			_ = h.listen.Close()
		}
		return nil, fmt.Errorf("start HEVC transcoder: %w", err)
	}
	h.cmd, h.video = cmd, video
	go h.writeVideo()
	if hasAudio {
		go h.writeAudio()
	}
	go func() {
		err := cmd.Wait()
		h.stop()
		if err != nil {
			fmt.Printf("[%s] [publish] HEVC-to-H.264 transcoder exited: %v\n", label, err)
		} else {
			fmt.Printf("[%s] [publish] HEVC-to-H.264 transcoder exited\n", label)
		}
	}()
	fmt.Printf("[%s] [publish] HEVC-to-H.264 transcoding enabled for RTSP/HLS/WebRTC compatibility\n", label)
	return h, nil
}

func (h *hevcTranscoder) WriteVideo(nals [][]byte) error {
	var b bytes.Buffer
	for _, n := range nals {
		if len(n) < 2 {
			continue
		}
		b.Write(hevcAnnexBStartCode)
		b.Write(n)
	}
	if b.Len() == 0 {
		return nil
	}
	select {
	case <-h.done:
		return io.ErrClosedPipe
	case h.videoQueue <- b.Bytes():
		return nil
	default:
		return fmt.Errorf("HEVC transcoder video queue is full")
	}
}

func (h *hevcTranscoder) WriteAudio(adts []byte) error {
	if h.audioQueue == nil || len(adts) == 0 {
		return nil
	}
	frame := append([]byte(nil), adts...)
	select {
	case <-h.done:
		return io.ErrClosedPipe
	case h.audioQueue <- frame:
		return nil
	default:
		return fmt.Errorf("HEVC transcoder audio queue is full")
	}
}

func (h *hevcTranscoder) writeVideo() {
	for {
		select {
		case <-h.done:
			return
		case b := <-h.videoQueue:
			if _, err := h.video.Write(b); err != nil {
				fmt.Printf("[%s] [publish] HEVC transcoder video input failed: %v\n", h.label, err)
				h.stop()
				return
			}
		}
	}
}

func (h *hevcTranscoder) writeAudio() {
	conn, err := h.listen.Accept()
	if err != nil {
		select {
		case <-h.done:
			return
		default:
			fmt.Printf("[%s] [publish] HEVC transcoder audio input failed: %v\n", h.label, err)
			h.stop()
			return
		}
	}
	_ = h.listen.Close()
	defer conn.Close()
	const silence = "\xff\xf1\x6c\x40\x01\x7f\xfc\x01\x18\x20\x07"
	if _, err := io.WriteString(conn, silence); err != nil {
		return
	}
	lastAudio := time.Now()
	ticker := time.NewTicker(64 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case frame := <-h.audioQueue:
			if _, err := conn.Write(frame); err != nil {
				h.stop()
				return
			}
			lastAudio = time.Now()
		case now := <-ticker.C:
			if now.Sub(lastAudio) > 150*time.Millisecond {
				if _, err := io.WriteString(conn, silence); err != nil {
					h.stop()
					return
				}
				lastAudio = now
			}
		}
	}
}

func (h *hevcTranscoder) stop() {
	h.once.Do(func() {
		close(h.done)
		if h.listen != nil {
			_ = h.listen.Close()
		}
		if h.video != nil {
			_ = h.video.Close()
		}
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	})
}

func (h *hevcTranscoder) Close() { h.stop() }
