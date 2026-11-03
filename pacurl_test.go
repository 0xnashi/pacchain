package main

// The url argument pacchain passes to FindProxyForURL must match what a
// browser passes, or a PAC that looks at url routes differently in pacchain
// than in Chrome:
//
//	https://app.test/path?q   → "https://app.test/"       (scheme + host only)
//	https://app.test:8443/x   → "https://app.test:8443/"
//	http://web.test/p?q=1     → "http://web.test/p?q=1"   (full URL)
//
// Over a proxy, an HTTPS request arrives as "CONNECT app.test:443": pacchain
// never sees the path, so the browser's stripped form is also all it has.

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

// urlMatchPAC routes to u only when the PAC's url argument equals want.
func urlMatchPAC(u *fakeupstream.Upstream, want string) string {
	return fmt.Sprintf(`function FindProxyForURL(url, host) {
		if (url == %q) return "PROXY %s";
		return "PROXY 127.0.0.1:1";   // anything else: a dead proxy, so the test fails loudly
	}`, want, u.Addr())
}

func TestPACURLForConnectMatchesBrowser(t *testing.T) {
	tests := []struct {
		target string // CONNECT target
		want   string // url the PAC must receive
	}{
		{"app.test:443", "https://app.test/"},
		{"app.test:8443", "https://app.test:8443/"},
		{"[::1]:443", "https://[::1]/"},
		{"[::1]:8443", "https://[::1]:8443/"},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
			chain, _ := newPacchainWith(t, urlMatchPAC(a, tc.want), nil)
			status, _, body := rawCONNECT(t, chain, tc.target)
			if status != "200 OK" {
				t.Fatalf("PAC did not receive url %q: status %q %s", tc.want, status, body)
			}
		})
	}
}

// HTTP keeps the full URL, query included, like the browser.
func TestPACURLForHTTPIsFullURL(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, _ := newPacchainWith(t, urlMatchPAC(a, "http://web.test/p?q=1"), nil)
	resp, _ := fetch(t, clientVia(t, chain), "http://web.test/p?q=1")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Fake-Upstream") != "stg-a" {
		t.Fatalf("PAC did not receive the full URL: status %d", resp.StatusCode)
	}
}

// A common real-world PAC pattern that depends on url, not host.
func TestPACSchemeMatchOnURL(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain, _ := newPacchainWith(t, fmt.Sprintf(`function FindProxyForURL(url, host) {
		if (shExpMatch(url, "https://*")) return "PROXY %s";
		return "PROXY 127.0.0.1:1";
	}`, a.Addr()), nil)
	resp, _ := fetch(t, clientVia(t, chain), "https://app.test/")
	if resp.Header.Get("X-Fake-Upstream") != "stg-a" {
		t.Fatal(`shExpMatch(url, "https://*") did not match an HTTPS request`)
	}
}

// -r must predict what pacchain does with real traffic, so an HTTPS URL is
// reduced the same way (its path never reaches pacchain over CONNECT).
func TestResolveUsesBrowserURL(t *testing.T) {
	src := `function FindProxyForURL(url, host) {
		if (url == "https://app.test/") return "PROXY 127.0.0.1:18431";
		return "DIRECT";
	}`
	code, out, errOut := runCLI("-c", src, "-r", "https://app.test/some/path?x=1")
	if code != 0 || out != "PROXY 127.0.0.1:18431\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestBrowserPACURL(t *testing.T) {
	tests := map[string]string{
		"https://app.test/a/b?c=d#frag": "https://app.test/",
		"https://app.test:443/a":        "https://app.test/",
		"https://app.test:8443/a":       "https://app.test:8443/",
		"HTTPS://App.Test/a":            "https://App.Test/",
		"http://web.test/p?q=1#frag":    "http://web.test/p?q=1",
		"http://web.test":               "http://web.test",
	}
	for in, want := range tests {
		u, _ := url.Parse(in)
		if got := browserPACURL(u).String(); got != want {
			t.Errorf("browserPACURL(%q) = %q, want %q", in, got, want)
		}
	}
}
