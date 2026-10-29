package main

// Test harness: run the real pacchain handler (OttoEngine + PAC) in front of
// fake upstream proxies, all in-process on random localhost ports.

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// newLoggedPacchain is newPacchain with the connection log captured.
func newLoggedPacchain(t *testing.T, pacSrc string, verbose bool) (*httptest.Server, *syncBuffer) {
	t.Helper()
	engine := pac.NewOttoEngine(pac.OttoStringLoader(pacSrc))
	if err := engine.Start(); err != nil {
		t.Fatalf("start PAC engine: %v", err)
	}
	logBuf := &syncBuffer{}
	h := newProxyHTTPHandler(engine, &pac.FirstItemSelector{}, nil)
	h.con = newConsole(logBuf)
	h.verbose = verbose
	h.pacName = "test.pac"
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		engine.Stop()
	})
	return srv, logBuf
}

// syncBuffer is a bytes.Buffer safe for concurrent writers and readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLog polls until the log contains want, or fails after 2 seconds.
// Tunnel teardown is asynchronous, so some lines appear after the response.
func waitForLog(t *testing.T, logBuf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log never contained %q; log:\n%s", want, logBuf.String())
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
