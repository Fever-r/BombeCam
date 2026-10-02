package bridge

import "testing"

func TestParseIRMode(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{"auto", IRModeAuto, true}, {"on", IRModeAuto, true}, {"night", IRModeAuto, true},
		{"off", IRModeOff, true}, {"day", IRModeOff, true}, {"0", IRModeOff, true},
		{float64(2), 2, true}, {true, IRModeAuto, true}, {false, IRModeOff, true},
		{"ultra-infrared", 0, false}, {float64(7), 0, false},
	}
	for _, c := range cases {
		got, ok := ParseIRMode(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseIRMode(%v) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
	for mode, name := range map[int]string{0: "off", 1: "auto", 2: "auto"} {
		if IRModeName(mode) != name {
			t.Errorf("IRModeName(%d) = %s want %s", mode, IRModeName(mode), name)
		}
	}
}
