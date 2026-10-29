package fakeupstream

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T, mode Mode) *Upstream {
	t.Helper()
	u, err := Start(Config{Name: "test-" + string(mode), Addr: "127.0.0.1:0", Mode: mode})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

// rawConnect sends a CONNECT to the upstream and returns the connection and
// the status line it answered with ("" if it closed without answering).
func rawConnect(t *testing.T, u *Upstream, target string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", u.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"))
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	return conn, br, strings.TrimSpace(line)
}

func TestParseMode(t *testing.T) {
	for _, s := range []string{"ok", "407", "502", "hangup", " OK "} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q) failed: %v", s, err)
		}
	}
	if _, err := ParseMode("teapot"); err == nil {
		t.Error("ParseMode(teapot) should fail")
	}
}

func TestConnectStatusPerMode(t *testing.T) {
	tests := []struct {
		mode Mode
		want string // expected status line prefix; "" = closed without reply
	}{
		{ModeOK, "HTTP/1.1 200"},
		{ModeReject407, "HTTP/1.1 407"},
		{ModeReject502, "HTTP/1.1 502"},
		{ModeHangup, ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			u := start(t, tc.mode)
			_, _, line := rawConnect(t, u, "app.example.test:443")
			if tc.want == "" && line != "" {
				t.Fatalf("expected hang-up, got %q", line)
			}
			if !strings.HasPrefix(line, tc.want) {
				t.Fatalf("expected %q, got %q", tc.want, line)
			}
			if got := u.Connects(); len(got) != 1 || got[0] != "app.example.test:443" {
				t.Errorf("Connects() = %v", got)
			}
		})
	}
}

// After a 200, the tunnel must lead to a TLS backend that names the upstream,
// whatever host was requested.
func TestConnectTunnelReachesBackend(t *testing.T) {
	u := start(t, ModeOK)
	conn, br, line := rawConnect(t, u, "anything.example.test:443")
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("expected 200, got %q", line)
	}
	for { // skip the rest of the CONNECT response headers
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "anything.example.test"})
	req, _ := http.NewRequest("GET", "https://anything.example.test/path", nil)
	if err := req.Write(tlsConn); err != nil {
		t.Fatalf("write over tunnel: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), req)
	if err != nil {
		t.Fatalf("read over tunnel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-Fake-Upstream") != u.Name() {
		t.Errorf("X-Fake-Upstream = %q, want %q", resp.Header.Get("X-Fake-Upstream"), u.Name())
	}
	if !strings.Contains(string(body), "anything.example.test") {
		t.Errorf("body does not mention requested host: %s", body)
	}
}

func TestAbsoluteFormRequest(t *testing.T) {
	u := start(t, ModeOK)
	conn, err := net.DialTimeout("tcp", u.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("GET http://web.example.test/x?secret=1 HTTP/1.1\r\nHost: web.example.test\r\nConnection: close\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Fake-Upstream") != u.Name() {
		t.Errorf("status %d, X-Fake-Upstream %q", resp.StatusCode, resp.Header.Get("X-Fake-Upstream"))
	}
	if got := u.Requests(); len(got) != 1 || got[0] != "http://web.example.test/x" {
		t.Errorf("Requests() = %v (query string must not be recorded)", got)
	}
}
