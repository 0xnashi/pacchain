package main

// Failover rules (agreed during requirements):
//
//   - Try PAC options in order.
//   - A proxy that cannot be used (unreachable, hangs up, no answer) → try next.
//   - A proxy that answers with an error (407, 502, ...) → stop and report it;
//     it works, and another proxy might route to a different environment.
//   - DIRECT after a failed proxy is blocked unless --allow-direct-fallback:
//     with STG proxies, "DIRECT" for a production hostname reaches production.
//   - pacchain replies 200 to the client only after the upstream said 200,
//     and never passes an upstream's CONNECT error through unexplained.

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

func pacReturning(answer string) string {
	return fmt.Sprintf(`function FindProxyForURL(url, host) { return %q; }`, answer)
}

// rawCONNECT sends a CONNECT through pacchain and returns the status line
// and the response body (empty for a successful tunnel).
func rawCONNECT(t *testing.T, chain *httptest.Server, target string) (status, body string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", chain.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	req, _ := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		var sb strings.Builder
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		sb.Write(buf[:n])
		body = sb.String()
	}
	return resp.Status, body
}

// directTarget is a TLS server reachable without any proxy; it counts hits
// so a test can prove that traffic did (or did not) go DIRECT.
func directTarget(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte("direct-target"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// --- CONNECT (HTTPS) -------------------------------------------------------

func TestFailoverWhenFirstProxyUnreachable(t *testing.T) {
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	dead := deadAddr(t)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+dead+"; PROXY "+b.Addr()), nil)

	resp, _ := fetch(t, clientVia(t, chain), "https://x.failover.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-b" {
		t.Fatalf("served by %q, want stg-b", got)
	}
	waitForLog(t, logBuf, "[!] CONNECT x.failover.test:443 → PROXY "+dead+" failed")
	waitForLog(t, logBuf, "[+] CONNECT x.failover.test:443 → PROXY "+b.Addr())
}

func TestFailoverWhenFirstProxyHangsUp(t *testing.T) {
	h := newFakeUpstream(t, "hangup", fakeupstream.ModeHangup)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	chain, _ := newPacchainWith(t, pacReturning("PROXY "+h.Addr()+"; PROXY "+b.Addr()), nil)

	resp, _ := fetch(t, clientVia(t, chain), "https://x.failover.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-b" {
		t.Fatalf("served by %q, want stg-b", got)
	}
}

// The core fix for blind relaying: an upstream's 407 used to be piped to the
// client unexplained while pacchain logged nothing.
func TestUpstreamRejectionIsReportedNotRelayed(t *testing.T) {
	deny := newFakeUpstream(t, "deny", fakeupstream.ModeReject407)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+deny.Addr()), nil)

	status, body := rawCONNECT(t, chain, "x.deny.test:443")
	if !strings.HasPrefix(status, "502") {
		t.Fatalf("status %q, want 502 from pacchain (not the upstream's 407)", status)
	}
	for _, want := range []string{deny.Addr(), "407"} {
		if !strings.Contains(body, want) {
			t.Errorf("502 body missing %q:\n%s", want, body)
		}
	}
	waitForLog(t, logBuf, "[-] CONNECT x.deny.test:443 → PROXY "+deny.Addr()+" failed: answered 407")
}

func TestNoFailoverWhenProxyAnswersWithError(t *testing.T) {
	for _, mode := range []fakeupstream.Mode{fakeupstream.ModeReject407, fakeupstream.ModeReject502} {
		t.Run(string(mode), func(t *testing.T) {
			first := newFakeUpstream(t, "first", mode)
			b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
			chain, _ := newPacchainWith(t, pacReturning("PROXY "+first.Addr()+"; PROXY "+b.Addr()), nil)

			status, _ := rawCONNECT(t, chain, "x.test:443")
			if !strings.HasPrefix(status, "502") {
				t.Errorf("status %q, want 502", status)
			}
			if n := len(b.Connects()); n != 0 {
				t.Errorf("failed over to stg-b after the first proxy answered %s", mode)
			}
		})
	}
}

func TestAllOptionsFailedListsEveryAttempt(t *testing.T) {
	dead1, dead2 := deadAddr(t), deadAddr(t)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+dead1+"; PROXY "+dead2), nil)

	status, body := rawCONNECT(t, chain, "x.down.test:443")
	if !strings.HasPrefix(status, "502") {
		t.Fatalf("status %q, want 502", status)
	}
	for _, want := range []string{dead1, dead2} {
		if !strings.Contains(body, want) {
			t.Errorf("502 body missing attempt %s:\n%s", want, body)
		}
	}
	waitForLog(t, logBuf, "[!] CONNECT x.down.test:443 → PROXY "+dead1+" failed")
	waitForLog(t, logBuf, "[-] CONNECT x.down.test:443 → PROXY "+dead2+" failed")
}

func TestDirectFallbackBlockedByDefault(t *testing.T) {
	target, hits := directTarget(t)
	dead := deadAddr(t)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+dead+"; DIRECT"), nil)

	status, body := rawCONNECT(t, chain, target.Listener.Addr().String())
	if !strings.HasPrefix(status, "502") {
		t.Fatalf("status %q, want 502", status)
	}
	if !strings.Contains(body, "--allow-direct-fallback") {
		t.Errorf("502 body should explain how to allow it:\n%s", body)
	}
	if atomic.LoadInt64(hits) != 0 {
		t.Error("traffic reached the target DIRECT")
	}
	waitForLog(t, logBuf, "DIRECT fallback blocked")
}

func TestDirectFallbackAllowedWithFlag(t *testing.T) {
	target, hits := directTarget(t)
	dead := deadAddr(t)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+dead+"; DIRECT"),
		func(h *proxyHTTPHandler) { h.allowDirectFallback = true })

	_, body := fetch(t, clientVia(t, chain), target.URL)
	if body != "direct-target" || atomic.LoadInt64(hits) != 1 {
		t.Fatalf("body %q, hits %d", body, atomic.LoadInt64(hits))
	}
	// Reaching a target DIRECT after a proxy failed is risky: warn, not [+].
	waitForLog(t, logBuf, "[!] CONNECT "+target.Listener.Addr().String()+" → DIRECT")
}

// DIRECT as the first option is ordinary routing, not a fallback.
func TestDirectFirstIsNotAFallback(t *testing.T) {
	target, hits := directTarget(t)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	chain, logBuf := newPacchainWith(t, pacReturning("DIRECT; PROXY "+b.Addr()), nil)

	_, body := fetch(t, clientVia(t, chain), target.URL)
	if body != "direct-target" || atomic.LoadInt64(hits) != 1 {
		t.Fatalf("body %q, hits %d", body, atomic.LoadInt64(hits))
	}
	waitForLog(t, logBuf, "[+] CONNECT "+target.Listener.Addr().String()+" → DIRECT")
}

// --- HTTP (absolute-form) --------------------------------------------------

func TestHTTPFailoverWhenFirstProxyUnreachable(t *testing.T) {
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	chain, _ := newPacchainWith(t, pacReturning("PROXY "+deadAddr(t)+"; PROXY "+b.Addr()), nil)

	resp, _ := fetch(t, clientVia(t, chain), "http://web.failover.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-b" {
		t.Fatalf("served by %q, want stg-b", got)
	}
}

// A retried POST must arrive with its complete body.
func TestHTTPFailoverReplaysRequestBody(t *testing.T) {
	h := newFakeUpstream(t, "hangup", fakeupstream.ModeHangup)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	chain, _ := newPacchainWith(t, pacReturning("PROXY "+h.Addr()+"; PROXY "+b.Addr()), nil)

	resp, err := clientVia(t, chain).Post("http://web.failover.test/form", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Fake-Upstream") != "stg-b" || resp.Header.Get("X-Fake-Body-Bytes") != "5" {
		t.Fatalf("served by %q with %s body bytes, want stg-b with 5",
			resp.Header.Get("X-Fake-Upstream"), resp.Header.Get("X-Fake-Body-Bytes"))
	}
}

func TestHTTPDirectFallbackBlockedByDefault(t *testing.T) {
	var hits int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
	}))
	defer target.Close()
	chain, _ := newPacchainWith(t, pacReturning("PROXY "+deadAddr(t)+"; DIRECT"), nil)

	resp, body := fetch(t, clientVia(t, chain), target.URL)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "--allow-direct-fallback") {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	if atomic.LoadInt64(&hits) != 0 {
		t.Error("traffic reached the target DIRECT")
	}
}

// 407 can only come from a proxy, so on the HTTP path it is reported like on
// the CONNECT path instead of being handed to Burp as if from the target.
func TestHTTPUpstream407IsReported(t *testing.T) {
	deny := newFakeUpstream(t, "deny", fakeupstream.ModeReject407)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	chain, logBuf := newPacchainWith(t, pacReturning("PROXY "+deny.Addr()+"; PROXY "+b.Addr()), nil)

	resp, body := fetch(t, clientVia(t, chain), "http://web.deny.test/")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "407") {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	if len(b.Requests()) != 0 {
		t.Error("failed over after a 407")
	}
	waitForLog(t, logBuf, "[-] GET http://web.deny.test/ → PROXY "+deny.Addr()+" failed: answered 407")
}
