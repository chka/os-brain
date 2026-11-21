package netguard

import (
	"testing"
)

// Whether an address is loopback decides how much the server demands before it listens,
// so the wildcard binds (0.0.0.0, ::, an empty host) and LAN addresses must never pass.
func TestLoopbackDetection(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7777", "localhost:7777", "[::1]:7777"} {
		if !IsLoopback(addr) {
			t.Errorf("%s should be loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:7777", "192.168.1.10:7777", ":7777", "[::]:7777"} {
		if IsLoopback(addr) {
			t.Errorf("%s is NOT loopback", addr)
		}
	}
}
