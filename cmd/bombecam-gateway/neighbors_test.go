package main

import "testing"

func TestParseArpOutput_WindowsAndMac(t *testing.T) {
	win := "\r\nInterface: 192.168.1.10 --- 0x5\r\n  Internet Address      Physical Address      Type\r\n  192.168.1.1           a0-b1-c2-d3-e4-f5     dynamic\r\n  192.168.1.57          10-2c-6b-aa-bb-cc     dynamic\r\n"
	got := parseArpOutput(win)
	if got["192.168.1.57"] != "10-2c-6b-aa-bb-cc" || got["192.168.1.1"] != "a0-b1-c2-d3-e4-f5" || len(got) != 2 {
		t.Fatalf("windows parse = %v", got)
	}
	mac := "? (10.0.0.241) at 02:00:00:12:34:56 on en0 ifscope [ethernet]\n"
	if got := parseArpOutput(mac); got["10.0.0.241"] != "02:00:00:12:34:56" {
		t.Fatalf("mac parse = %v", got)
	}
}
