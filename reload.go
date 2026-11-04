package main

// Reloading the PAC while pacchain runs: by editing/replacing the PAC file
// (checked twice a second) or with `kill -HUP <pid>`.
//
// A reload builds and checks the new PAC first; if it is broken the previous
// PAC stays in service. After a successful reload every open CONNECT tunnel
// is closed: it was routed by the old PAC and would otherwise keep using the
// old upstream. Burp reconnects and the new PAC decides again.

import (
	"crypto/sha256"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/0xnashi/pacchain/pac"
)

// pacWatchInterval is how often the PAC file is checked for changes.
const pacWatchInterval = 500 * time.Millisecond

type reloader struct {
	engine  pac.EngineManager
	handler *proxyHTTPHandler
	con     *console
	name    string // PAC name for log lines

	mu sync.Mutex // one reload at a time
}

func (rl *reloader) reload(reason string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if err := rl.engine.Reload(); err != nil {
		rl.con.warnf("PAC reload (%s) failed, still using the previous PAC: %v", reason, err)
		return
	}
	n := rl.handler.closeTunnels()
	rl.con.infof("PAC reloaded (%s): %s; closed %d open tunnel(s) so they reconnect with the new rules",
		reason, rl.name, n)
}

// watchPAC calls onChange whenever the content of the file at path changes,
// checking every interval, until the returned stop function is called.
// Comparing content (not modification time) also catches editors that save
// via rename and a symlink switched to another file. A missing or unreadable
// file never triggers onChange; it is reported once.
//
// The starting content is read before watchPAC returns, so a change made
// right after it returns is never mistaken for the starting state.
func watchPAC(path string, interval time.Duration, con *console, onChange func()) (stop func()) {
	initial, _ := fileHash(path)
	detector := newChangeDetector(initial)
	done := make(chan struct{})
	go func() {
		unreadable := false
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			h, err := fileHash(path)
			if err != nil {
				if !unreadable {
					con.warnf("cannot read PAC file %s (%v); keeping the current PAC", path, err)
					unreadable = true
				}
				continue
			}
			unreadable = false
			if detector.observe(h) {
				onChange()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// changeDetector decides when a watched file really changed. Saving often
// truncates the file before writing it, so a single check can catch it empty
// or half written. A change counts only once the content is the same on two
// checks in a row, and only if it differs from what was last reported.
type changeDetector struct {
	loaded  [sha256.Size]byte // content last reported as a change (or the start)
	pending [sha256.Size]byte // content seen on the previous check
}

func newChangeDetector(initial [sha256.Size]byte) *changeDetector {
	return &changeDetector{loaded: initial, pending: initial}
}

// observe records one check and reports whether it completes a change.
func (d *changeDetector) observe(h [sha256.Size]byte) bool {
	stable := h == d.pending
	d.pending = h
	if !stable || h == d.loaded {
		return false
	}
	d.loaded = h
	return true
}

func fileHash(path string) ([sha256.Size]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(b), nil
}

// isPACFile reports whether the -c value is a file path (and so can be
// watched), as opposed to a URL, an inline script or an inline result.
func isPACFile(s string) bool {
	if _, err := pac.ParseFindProxyString(s); err == nil {
		return false
	}
	if strings.Contains(s, "FindProxyForURL") {
		return false
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return false
	}
	return true
}
