package main

// Reloading the PAC while pacchain runs: by editing/replacing the PAC file
// (watched once a second) or with `kill -HUP <pid>`.
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
const pacWatchInterval = time.Second

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
	last, _ := fileHash(path)
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
			if h != last {
				last = h
				onChange()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
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
