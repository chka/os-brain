// Package netguard holds the two checks every listener in os-brain shares: whether an
// address binds loopback only, which decides how much a server demands before it listens,
// and minting the random tokens that guard its routes.
package netguard

import (
	"crypto/rand"
	"encoding/hex"
	"net"
)

// IsLoopback reports whether addr (host:port, or a bare host) binds loopback only.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	// An empty host (":7777") is not loopback: net.Listen binds every interface for it.
	// Treating it as loopback once skipped the token requirement and served the brain to
	// the network, while the startup line said "loopback only".
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RandomToken is 32 random bytes in hex. A failing crypto/rand is not survivable: a token
// made any other way could be guessed.
func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
