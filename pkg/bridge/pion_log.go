package bridge

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pion/logging"
)

// pionLoggerFactory is pion's usual logger, except that the TURN client's
// "Fail to refresh permissions ... error 400" (every 2 minutes against
// Osaio's relays) is logged once per stream instead of every time. The relay
// is only a fallback; the video normally goes directly over the network.
type pionLoggerFactory struct {
	base  logging.LoggerFactory
	label string
}

func newPionLoggerFactory(label string) logging.LoggerFactory {
	return &pionLoggerFactory{base: logging.NewDefaultLoggerFactory(), label: label}
}

func (f *pionLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	l := f.base.NewLogger(scope)
	if scope == "turnc" {
		return &turnLogFilter{LeveledLogger: l, label: f.label}
	}
	return l
}

type turnLogFilter struct {
	logging.LeveledLogger
	label string
	once  sync.Once
}

func (t *turnLogFilter) quiet(msg string) bool {
	if !strings.Contains(msg, "refresh permissions") {
		return false
	}
	t.once.Do(func() {
		fmt.Printf("[%s] Osaio's relay server refused a permission refresh (%s). Harmless while video goes directly over your network; not logged again for this stream.\n", t.label, msg)
	})
	return true
}

func (t *turnLogFilter) Error(msg string) {
	if !t.quiet(msg) {
		t.LeveledLogger.Error(msg)
	}
}

func (t *turnLogFilter) Errorf(format string, args ...interface{}) {
	if !t.quiet(fmt.Sprintf(format, args...)) {
		t.LeveledLogger.Errorf(format, args...)
	}
}

func (t *turnLogFilter) Warn(msg string) {
	if !t.quiet(msg) {
		t.LeveledLogger.Warn(msg)
	}
}

func (t *turnLogFilter) Warnf(format string, args ...interface{}) {
	if !t.quiet(fmt.Sprintf(format, args...)) {
		t.LeveledLogger.Warnf(format, args...)
	}
}
