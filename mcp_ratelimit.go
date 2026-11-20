package main

// Rate limits for the HTTP endpoints: failGate for guessing a secret, regLimiter for
// unauthenticated OAuth client registration, and sourceOf for telling callers apart.

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// failGate is the one counter for guessing a secret over HTTP: wrong passwords on the login
// page and wrong bearers on /mcp. It was once a single global lockout on the page alone, so
// ten guesses a quarter of an hour from anybody kept the owner out for good, and /mcp
// counted nothing. Now each source backs off on its own, doubling, and a global ceiling
// stops a spread-out guess. One source cannot hold that ceiling: its own backoff admits
// about nine failures in a window against a ceiling of gateGlobal.
type failGate struct {
	mu     sync.Mutex
	per    map[string]*sourceFails
	recent []time.Time // every failure inside gateWindow, oldest first
	now    func() time.Time
}

type sourceFails struct {
	n     int
	last  time.Time
	until time.Time
}

const (
	gateFree   = 5 // failures a source makes before it waits
	gateBase   = time.Minute
	gateMax    = time.Hour
	gateWindow = 15 * time.Minute
	gateGlobal = 100
	gateForget = 24 * time.Hour // a source quiet this long starts again from nothing
	gateKeep   = 4096           // sources remembered before quiet ones are dropped
)

func newFailGate() *failGate {
	return &failGate{per: map[string]*sourceFails{}, now: time.Now}
}

// trim drops failures older than the window and, once too many sources are remembered,
// the ones that are neither waiting nor recent, so the map cannot grow without bound.
func (g *failGate) trim(now time.Time) {
	i := 0
	for i < len(g.recent) && now.Sub(g.recent[i]) >= gateWindow {
		i++
	}
	g.recent = g.recent[i:]
	if len(g.per) > gateKeep {
		for k, f := range g.per {
			if now.After(f.until) && now.Sub(f.last) >= gateWindow {
				delete(g.per, k)
			}
		}
	}
}

// wait is how long src must wait before it may try again; 0 means now. While it waits,
// nothing is compared, right or wrong, so a lockout cannot be used to test guesses.
func (g *failGate) wait(src string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.trim(now)
	if f := g.per[src]; f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	if len(g.recent) >= gateGlobal {
		return g.recent[0].Add(gateWindow).Sub(now)
	}
	return 0
}

// fail records one wrong guess from src. After gateFree of them the source waits
// gateBase, doubling with each further failure up to gateMax.
func (g *failGate) fail(src string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	f := g.per[src]
	if f == nil || now.Sub(f.last) >= gateForget {
		f = &sourceFails{}
		g.per[src] = f
	}
	f.n++
	f.last = now
	if f.n >= gateFree {
		f.until = now.Add(backoff(f.n - gateFree))
	}
	g.recent = append(g.recent, now)
	g.trim(now)
}

// backoff is gateBase doubled shift times, capped at gateMax. The shift is capped before
// it is applied so a long run of failures cannot overflow the duration.
func backoff(shift int) time.Duration {
	d := gateMax
	if shift < 6 {
		d = gateBase << shift
	}
	if d > gateMax {
		d = gateMax
	}
	return d
}

// ok forgets src's failures after it got the secret right.
func (g *failGate) ok(src string) {
	g.mu.Lock()
	delete(g.per, src)
	g.mu.Unlock()
}

// regLimiter admits registrations per source and in all within regWindow. Registration needs
// no secret, so unlike failGate it counts every success: a table that anybody can append to at
// line rate is a table anybody can churn the owner's pending registration out of.
type regLimiter struct {
	mu     sync.Mutex
	per    map[string][]time.Time
	recent []time.Time
	now    func() time.Time
}

func newRegLimiter() *regLimiter {
	return &regLimiter{per: map[string][]time.Time{}, now: time.Now}
}

// inWindow drops the timestamps older than regWindow from the front of ts (oldest first).
func inWindow(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= regWindow {
		i++
	}
	return ts[i:]
}

// allow records one registration from src and returns 0, or how long until one would be
// admitted, recording nothing.
func (l *regLimiter) allow(src string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.recent = inWindow(l.recent, now)
	if len(l.per) > gateKeep {
		for k, ts := range l.per {
			if len(inWindow(ts, now)) == 0 {
				delete(l.per, k)
			}
		}
	}
	mine := inWindow(l.per[src], now)
	switch {
	case len(mine) >= regPerSource:
		l.per[src] = mine
		return mine[0].Add(regWindow).Sub(now)
	case len(l.recent) >= regGlobal:
		l.per[src] = mine
		return l.recent[0].Add(regWindow).Sub(now)
	}
	l.per[src] = append(mine, now)
	l.recent = append(l.recent, now)
	return 0
}

// sourceOf is the address a guess came from. Behind a proxy on loopback (Tailscale Funnel,
// Caddy) every request arrives from 127.0.0.1, so the address that proxy appended to
// X-Forwarded-For is used instead. Only the LAST address is trusted, because everything
// before it was written by the client. A proxy that sends none leaves every caller one
// source, and the gate then behaves as a single shared lockout.
func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	return host
}
