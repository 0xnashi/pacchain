package main

// pacchain must not rewrite HTTP traffic: what Burp sends is what the server
// receives, and what the server answers is what Burp sees. Go's http.Transport
// adds "Accept-Encoding: gzip" to requests without one and then transparently
// decompresses the response (dropping Content-Encoding and Content-Length);
// pacchain used to strip Accept-Encoding, which triggered exactly that.

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

// rawClientVia is a client that sends headers exactly as given and never
// decompresses: it stands in for Burp, which shows bytes as received.
func rawClientVia(t *testing.T, proxy *httptest.Server) *http.Client {
	t.Helper()
	proxyURL, _ := url.Parse(proxy.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

func getWithEncoding(t *testing.T, c *http.Client, rawURL, acceptEncoding string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", rawURL, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func viaUpstream(t *testing.T) (*fakeupstream.Upstream, *http.Client) {
	t.Helper()
	a := newFakeUpstream(t, "stg-a", fakeupstream.ModeOK)
	chain := newPacchain(t, fmt.Sprintf(`function FindProxyForURL(url, host) { return "PROXY %s"; }`, a.Addr()))
	return a, rawClientVia(t, chain)
}

// The server receives Burp's Accept-Encoding unchanged — including none at all.
func TestHTTPAcceptEncodingPassedThrough(t *testing.T) {
	_, client := viaUpstream(t)
	for _, sent := range []string{"gzip, deflate, br", "br", "identity", ""} {
		t.Run(fmt.Sprintf("%q", sent), func(t *testing.T) {
			resp, _ := getWithEncoding(t, client, "http://web.a.stg.test/", sent)
			if got := resp.Header.Get("X-Fake-Accept-Encoding"); got != sent {
				t.Errorf("server received Accept-Encoding %q, Burp sent %q", got, sent)
			}
		})
	}
}

// A gzip response reaches Burp exactly as the server sent it.
func TestHTTPGzipResponseRelayedUnchanged(t *testing.T) {
	_, client := viaUpstream(t)
	for _, sent := range []string{"gzip, deflate, br", ""} {
		t.Run(fmt.Sprintf("Accept-Encoding %q", sent), func(t *testing.T) {
			resp, body := getWithEncoding(t, client, "http://web.a.stg.test"+fakeupstream.GzipPath, sent)
			assertGzipUnchanged(t, resp, body)
		})
	}
}

// Same over the DIRECT path, against a server that is not a fake upstream.
func TestHTTPGzipResponseRelayedUnchangedDirect(t *testing.T) {
	plain := []byte("hello from a gzip server")
	gz := fakeupstream.GzipBytes(plain)
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz)
	}))
	defer target.Close()
	client := rawClientVia(t, newPacchain(t, `function FindProxyForURL(url, host) { return "DIRECT"; }`))

	resp, body := getWithEncoding(t, client, target.URL, "")
	if received != "" {
		t.Errorf("server received Accept-Encoding %q, Burp sent none", received)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(body, gz) {
		t.Errorf("Content-Encoding %q, body %q; want the server's gzip bytes unchanged",
			resp.Header.Get("Content-Encoding"), body)
	}
}

func assertGzipUnchanged(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip (server's header removed)", got)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("Content-Length = %d, body is %d bytes (server's length not kept)", resp.ContentLength, len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("body is not gzip (decompressed on the way?): %v; body %q", err, body)
	}
	plain, _ := io.ReadAll(zr)
	if !bytes.Contains(plain, []byte("fake upstream: stg-a")) {
		t.Errorf("unexpected content after gunzip: %q", plain)
	}
}
