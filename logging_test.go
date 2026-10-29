package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

func singleUpstreamPAC(u *fakeupstream.Upstream) string {
	return fmt.Sprintf(`function FindProxyForURL(url, host) {
		if (dnsDomainIs(host, ".a.stg.test")) return "PROXY %s";
		return "DIRECT";
	}`, u.Addr())
}

// Every CONNECT gets one always-visible line: target, decision, PAC name.
func TestLogConnectDecision(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, singleUpstreamPAC(a), false)

	fetch(t, clientVia(t, chain), "https://app.a.stg.test/")

	waitForLog(t, logBuf, "[+] CONNECT app.a.stg.test:443 → PROXY "+a.Addr())
	if !strings.Contains(logBuf.String(), "test.pac") {
		t.Errorf("log line should name the PAC: %s", logBuf)
	}
}

// HTTP lines show method, URL without query string, decision and status.
func TestLogHTTPDecisionWithoutQueryString(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, singleUpstreamPAC(a), true)

	fetch(t, clientVia(t, chain), "http://web.a.stg.test/page?token=SECRET123")

	waitForLog(t, logBuf, "[+] GET http://web.a.stg.test/page → PROXY "+a.Addr())
	waitForLog(t, logBuf, "200")
	if strings.Contains(logBuf.String(), "SECRET123") || strings.Contains(logBuf.String(), "token=") {
		t.Errorf("query string leaked into the log:\n%s", logBuf)
	}
}

// Headers (cookies, credentials) must never be logged, even with -v.
func TestLogNeverContainsHeaders(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, singleUpstreamPAC(a), true)
	client := clientVia(t, chain)

	for _, target := range []string{"http://web.a.stg.test/", "https://app.a.stg.test/"} {
		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("Cookie", "SESSION=cookie-s3cr3t")
		req.Header.Set("Authorization", "Bearer bearer-s3cr3t")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	waitForLog(t, logBuf, "CONNECT app.a.stg.test:443")
	if strings.Contains(logBuf.String(), "s3cr3t") {
		t.Errorf("header values leaked into the log:\n%s", logBuf)
	}
}

// A dead upstream is logged as an error naming the upstream.
func TestLogDialFailure(t *testing.T) {
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close() // nothing listens here any more

	chain, logBuf := newLoggedPacchain(t, fmt.Sprintf(
		`function FindProxyForURL(url, host) { return "PROXY %s"; }`, deadAddr), false)

	fetchAllowError(clientVia(t, chain), "https://x.down.test/")
	waitForLog(t, logBuf, "[-] CONNECT x.down.test:443 → PROXY "+deadAddr+" failed")
}

// When the PAC offers several options, the line shows the raw answer too.
func TestLogShowsRawPACWhenSeveralOptions(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, fmt.Sprintf(
		`function FindProxyForURL(url, host) { return "PROXY %s; DIRECT"; }`, a.Addr()), false)

	fetch(t, clientVia(t, chain), "https://app.a.stg.test/")
	waitForLog(t, logBuf, fmt.Sprintf(`(test.pac: "PROXY %s; DIRECT"`, a.Addr()))
}

// -v adds a line when a tunnel closes, with duration and bytes moved.
func TestVerboseLogsTunnelClose(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, singleUpstreamPAC(a), true)

	fetch(t, clientVia(t, chain), "https://app.a.stg.test/")
	waitForLog(t, logBuf, "[*] CONNECT app.a.stg.test:443 closed after")
	waitForLog(t, logBuf, "received")
}

// Without -v there is no per-tunnel close line: one line per connection.
func TestQuietModeIsOneLinePerConnection(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, logBuf := newLoggedPacchain(t, singleUpstreamPAC(a), false)

	fetch(t, clientVia(t, chain), "https://app.a.stg.test/")
	waitForLog(t, logBuf, "[+] CONNECT")
	if n := strings.Count(logBuf.String(), "\n"); n != 1 {
		t.Errorf("want exactly 1 line, got %d:\n%s", n, logBuf)
	}
}

// fetchAllowError is fetch for requests that are expected to fail.
func fetchAllowError(c *http.Client, rawURL string) (*http.Response, error) {
	resp, err := c.Get(rawURL)
	if err == nil {
		resp.Body.Close()
	}
	return resp, err
}
