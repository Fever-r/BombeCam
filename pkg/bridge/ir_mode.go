package bridge

import (
	"strconv"
	"strings"
)

// IrLedMode values, as observed on a Yoton WS03:
//
//   - 0 disables the infrared illuminator: the picture stays in color and
//     goes dark at night.
//   - 1 and 2 both leave night vision to the camera's light sensor: infrared
//     switches on only when the room is dark.
//
// No value forces night vision on in a lit room, so the controls are
// "Auto" and "Off".
const (
	IRModeOff  = 0
	IRModeAuto = 1
	// IRModeOn is kept for API compatibility: "on" means "night vision
	// enabled", which the camera implements as automatic.
	IRModeOn = IRModeAuto
	// irModeAuto2 is the other value the camera treats as automatic.
	irModeAuto2 = 2
)

// IRModeName returns "off" or "auto" for a camera IrLedMode value.
func IRModeName(mode int) string {
	if mode == IRModeOff {
		return "off"
	}
	return "auto"
}

// ParseIRMode accepts a name ("auto", "on"/"night", "off"/"day"), a raw
// IrLedMode number (as a number or numeric string) or a bool (true = auto).
func ParseIRMode(v any) (mode int, ok bool) {
	switch x := v.(type) {
	case nil:
		return IRModeAuto, true
	case bool:
		if x {
			return IRModeAuto, true
		}
		return IRModeOff, true
	case float64:
		return rawIRMode(int(x))
	case int:
		return rawIRMode(x)
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		switch s {
		case "", "auto", "automatic", "on", "night", "ir", "nightvision":
			return IRModeAuto, true
		case "off", "day", "color", "colour":
			return IRModeOff, true
		}
		if n, err := strconv.Atoi(s); err == nil {
			return rawIRMode(n)
		}
	}
	return 0, false
}

func rawIRMode(n int) (int, bool) {
	if n == IRModeOff || n == IRModeAuto || n == irModeAuto2 {
		return n, true
	}
	return 0, false
}
