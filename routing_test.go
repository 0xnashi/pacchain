package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

// twoUpstreamPAC routes *.a.stg.test and *.b.stg.test to two upstreams.
func twoUpstreamPAC(a, b *fakeupstream.Upstream) string {
	return fmt.Sprintf(`function FindProxyForURL(url, host) {
		if (dnsDomainIs(host, ".a.stg.test")) return "PROXY %s";
		if (dnsDomainIs(host, ".b.stg.test")) return "PROXY %s";
		return "DIRECT";
	}`, a.Addr(), b.Addr())
}

func TestPACRoutesToSelectedUpstream(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	client := clientVia(t, newPacchain(t, twoUpstreamPAC(a, b)))

	tests := []struct {
		url  string
		want string // upstream that must serve the request
	}{
		{"https://app.a.stg.test/", "stg-a"},
		{"https://app.b.stg.test/", "stg-b"},
		{"http://web.a.stg.test/page", "stg-a"},
		{"http://web.b.stg.test/page", "stg-b"},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			resp, body := fetch(t, client, tc.url)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, body %q", resp.StatusCode, body)
			}
			if got := resp.Header.Get("X-Fake-Upstream"); got != tc.want {
				t.Errorf("served by %q, want %q", got, tc.want)
			}
		})
	}

	// Each upstream saw only its own hosts.
	if got := a.Connects(); len(got) != 1 || got[0] != "app.a.stg.test:443" {
		t.Errorf("stg-a CONNECTs = %v", got)
	}
	if got := b.Connects(); len(got) != 1 || got[0] != "app.b.stg.test:443" {
		t.Errorf("stg-b CONNECTs = %v", got)
	}
	if got := a.Requests(); len(got) != 1 || got[0] != "http://web.a.stg.test/page" {
		t.Errorf("stg-a HTTP requests = %v", got)
	}
}

func TestPACDirectBypassesUpstreams(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	b := newFakeUpstream(t, "stg-b", fakeupstream.ModeOK)
	client := clientVia(t, newPacchain(t, twoUpstreamPAC(a, b)))

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("direct-target"))
	}))
	defer target.Close()

	resp, body := fetch(t, client, target.URL)
	if resp.StatusCode != http.StatusOK || body != "direct-target" {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	if n := len(a.Connects()) + len(b.Connects()); n != 0 {
		t.Errorf("DIRECT traffic reached an upstream (%d CONNECTs)", n)
	}
}

// dnsDomainIs is a plain suffix match in browsers, so "evilstg.test" matches
// "stg.test". pacchain keeps that behaviour to route exactly like the browser.
func TestDnsDomainIsSuffixMatchesLikeBrowsers(t *testing.T) {
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	client := clientVia(t, newPacchain(t, fmt.Sprintf(`function FindProxyForURL(url, host) {
		if (dnsDomainIs(host, "stg.test")) return "PROXY %s";
		return "DIRECT";
	}`, a.Addr())))

	resp, _ := fetch(t, client, "https://evilstg.test/")
	if got := resp.Header.Get("X-Fake-Upstream"); got != "stg-a" {
		t.Errorf("evilstg.test served by %q, want stg-a (browser suffix semantics)", got)
	}
	if got := a.Connects(); len(got) != 1 || !strings.HasPrefix(got[0], "evilstg.test") {
		t.Errorf("stg-a CONNECTs = %v", got)
	}
}
