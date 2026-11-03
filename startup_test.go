package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xnashi/pacchain/pac"
)

// startServer runs pacchain's startup (everything listen does except block)
// on a random port with the log captured.
func startServer(t *testing.T, o options) (addr string, logBuf *syncBuffer) {
	t.Helper()
	engine := pac.NewOttoEngine(pac.OttoLoader(pac.SmartLoader(o.pac)))
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logBuf = &syncBuffer{}
	srv, cleanup := setupServer(engine, o, newConsole(logBuf), ln)
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		cleanup()
		engine.Stop()
	})
	return ln.Addr().String(), logBuf
}

func writePAC(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "current.pac")
	if err := os.WriteFile(path, []byte(pac.DirectPAC), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Tell the user that editing or switching the PAC file takes effect live.
func TestStartupAnnouncesWatchedFile(t *testing.T) {
	path := writePAC(t)
	_, logBuf := startServer(t, options{pac: path, idleWarnAfter: time.Hour})
	waitForLog(t, logBuf, "[*] watching "+path+" for changes")
}

func TestStartupDoesNotWatchInlinePAC(t *testing.T) {
	_, logBuf := startServer(t, options{pac: pac.DirectPAC, idleWarnAfter: time.Hour})
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(logBuf.String(), "watching") {
		t.Errorf("an inline PAC has no file to watch:\n%s", logBuf)
	}
}

// No traffic at all usually means Burp's upstream setting is not in effect:
// say so once, with what to check.
func TestIdleReminderWhenNoTraffic(t *testing.T) {
	addr, logBuf := startServer(t, options{pac: writePAC(t), idleWarnAfter: 50 * time.Millisecond})
	waitForLog(t, logBuf, "[!] no requests received")
	for _, want := range []string{addr, "Upstream proxy servers", "Temporary project"} {
		if !strings.Contains(logBuf.String(), want) {
			t.Errorf("reminder does not mention %q:\n%s", want, logBuf)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if n := strings.Count(logBuf.String(), "no requests received"); n != 1 {
		t.Errorf("reminder printed %d times, want once", n)
	}
}

func TestNoIdleReminderAfterTraffic(t *testing.T) {
	addr, logBuf := startServer(t, options{pac: writePAC(t), idleWarnAfter: 200 * time.Millisecond})
	resp, err := http.Get("http://" + addr + "/favicon.ico") // any request counts
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(400 * time.Millisecond)
	if strings.Contains(logBuf.String(), "no requests received") {
		t.Errorf("reminder printed although a request arrived:\n%s", logBuf)
	}
}

func TestDefaultIdleWarnAfter(t *testing.T) {
	if idleWarnAfterDefault != 30*time.Second {
		t.Errorf("idleWarnAfterDefault = %s, want 30s", idleWarnAfterDefault)
	}
}
