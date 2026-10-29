package main

// Test harness: run the real pacchain handler (OttoEngine + PAC) in front of
// fake upstream proxies, all in-process on random localhost ports.

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
	"github.com/0xnashi/pacchain/pac"
)

// newPacchain starts pacchain's proxy handler evaluating pacSrc with the real
// JavaScript engine, exactly as the binary does.
func newPacchain(t *testing.T, pacSrc string) *httptest.Server {
	t.Helper()
	engine := pac.NewOttoEngine(pac.OttoStringLoader(pacSrc))
	if err := engine.Start(); err != nil {
		t.Fatalf("start PAC engine: %v", err)
	}
	srv := httptest.NewServer(newProxyHTTPHandler(engine, &pac.FirstItemSelector{}, nil))
	t.Cleanup(func() {
		srv.Close()
		engine.Stop()
	})
	return srv
}

// newFakeUpstream starts a fake upstream proxy on a random port.
func newFakeUpstream(t *testing.T, name string, mode fakeupstream.Mode) *fakeupstream.Upstream {
	t.Helper()
	u, err := fakeupstream.Start(fakeupstream.Config{Name: name, Addr: "127.0.0.1:0", Mode: mode})
	if err != nil {
		t.Fatalf("start fake upstream %s: %v", name, err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

// clientVia returns an HTTP client that uses proxy, standing in for Burp.
func clientVia(t *testing.T, proxy *httptest.Server) *http.Client {
	t.Helper()
	proxyURL, _ := url.Parse(proxy.URL)
	tr := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// fetch performs a GET and returns the response with its body read.
func fetch(t *testing.T, c *http.Client, rawURL string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}
