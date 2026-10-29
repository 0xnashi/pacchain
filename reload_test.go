package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
	"github.com/0xnashi/pacchain/pac"
)

// --- file watcher ----------------------------------------------------------

func TestWatchPACDetectsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pac")
	os.WriteFile(path, []byte("one"), 0o600)

	var changes int64
	stop := watchPAC(path, 10*time.Millisecond, nil, func() { atomic.AddInt64(&changes, 1) })
	defer stop()

	waitChanges := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for atomic.LoadInt64(&changes) < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := atomic.LoadInt64(&changes); got != want {
			t.Fatalf("changes = %d, want %d", got, want)
		}
	}

	// In-place edit.
	os.WriteFile(path, []byte("two"), 0o600)
	waitChanges(1)

	// Editor-style save: write a temp file, rename it over the original.
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte("three"), 0o600)
	os.Rename(tmp, path)
	waitChanges(2)

	// Rewriting identical content is not a change.
	os.WriteFile(path, []byte("three"), 0o600)
	time.Sleep(100 * time.Millisecond)
	waitChanges(2)
}

// A missing file (e.g. mid-save) never triggers a reload of nothing.
func TestWatchPACIgnoresMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pac")
	os.WriteFile(path, []byte("one"), 0o600)

	var changes int64
	logBuf := &syncBuffer{}
	stop := watchPAC(path, 10*time.Millisecond, newConsole(logBuf), func() { atomic.AddInt64(&changes, 1) })
	defer stop()

	os.Remove(path)
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt64(&changes); n != 0 {
		t.Fatalf("missing file triggered %d reloads", n)
	}
	waitForLog(t, logBuf, "[!]") // said so, once
	if n := strings.Count(logBuf.String(), "[!]"); n != 1 {
		t.Errorf("warned %d times, want once:\n%s", n, logBuf)
	}
}

func TestIsPACFile(t *testing.T) {
	tests := map[string]bool{
		"examples/dev.pac":               true,
		"/Users/me/pac/env-a.pac":          true,
		"https://pac.example.test/p.pac": false,
		pac.DirectPAC:                    false,
		"PROXY proxy.example.test:8080":  false,
	}
	for in, want := range tests {
		if got := isPACFile(in); got != want {
			t.Errorf("isPACFile(%q) = %v, want %v", in, got, want)
		}
	}
}

// --- reload end to end -----------------------------------------------------

type reloadEnv struct {
	path   string
	chain  *httptest.Server
	logBuf *syncBuffer
}

// startReloadEnv runs pacchain from a PAC file with the watcher active.
func startReloadEnv(t *testing.T, pacSrc string) *reloadEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.pac")
	if err := os.WriteFile(path, []byte(pacSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := pac.NewOttoEngine(pac.OttoLoader(pac.FileLoader(path)))
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	logBuf := &syncBuffer{}
	con := newConsole(logBuf)
	h := newProxyHTTPHandler(engine, nil)
	h.con = con
	h.pacName = "routes.pac"
	rl := &reloader{engine: engine, handler: h, con: con, name: "routes.pac"}

	stop := watchPAC(path, 20*time.Millisecond, con, func() { rl.reload("file changed") })
	chain := httptest.NewServer(h)
	t.Cleanup(func() {
		stop()
		chain.Close()
		engine.Stop()
	})
	return &reloadEnv{path: path, chain: chain, logBuf: logBuf}
}

func routeTo(u *fakeupstream.Upstream) string {
	return fmt.Sprintf(`function FindProxyForURL(url, host) { return "PROXY %s"; }`, u.Addr())
}

// openTunnel opens a CONNECT tunnel through pacchain and keeps it open.
func openTunnel(t *testing.T, chain *httptest.Server, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", chain.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	req, _ := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	conn.SetReadDeadline(time.Time{})
	return conn
}

// Switching env-a → env-b: new requests follow the new PAC, and tunnels opened
// under the old PAC are closed so they cannot keep using the old route.
func TestPACFileChangeReroutesAndClosesTunnels(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	env := startReloadEnv(t, routeTo(a))

	old := openTunnel(t, env.chain, "app.test:443")

	os.WriteFile(env.path, []byte(routeTo(b)), 0o600)
	waitForLog(t, env.logBuf, "[*] PAC reloaded")
	waitForLog(t, env.logBuf, "closed 1 open tunnel")

	old.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := old.Read(make([]byte, 1)); err == nil {
		t.Error("tunnel opened under the old PAC is still open")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Error("tunnel opened under the old PAC was not closed")
	}

	resp, _ := fetch(t, clientVia(t, env.chain), "https://app.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-b" {
		t.Errorf("after reload served by %q, want stg-b", got)
	}
}

// Saving a broken PAC keeps the previous rules working.
func TestBrokenPACFileKeepsPreviousRules(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	env := startReloadEnv(t, routeTo(a))

	os.WriteFile(env.path, []byte(`function FindProxyForURL(url, host) { return `), 0o600)
	waitForLog(t, env.logBuf, "[!] PAC reload")
	waitForLog(t, env.logBuf, "previous PAC")

	resp, _ := fetch(t, clientVia(t, env.chain), "https://app.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-a" {
		t.Errorf("served by %q, want stg-a (previous PAC)", got)
	}
	if strings.Contains(env.logBuf.String(), "closed") {
		t.Error("a failed reload must not close tunnels")
	}
}
