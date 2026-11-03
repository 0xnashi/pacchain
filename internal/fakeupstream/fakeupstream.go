// Package fakeupstream runs fake upstream HTTP proxies for developing and
// testing pacchain without a VPN or real STG proxies.
//
// A fake upstream behaves like an STG proxy: whatever host the client asks
// for, the request is answered by the upstream's own built-in backend. The
// response names the upstream (X-Fake-Upstream header and page body), so a
// tester can see which upstream a request actually went through.
//
// Modes simulate upstream failures that pacchain must handle:
//
//	ok      accept CONNECT (200) and absolute-form requests
//	407     reply 407 Proxy Authentication Required
//	502     reply 502 Bad Gateway (upstream cannot reach the target)
//	hangup  accept the TCP connection, then close it without replying
package fakeupstream

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode selects how a fake upstream answers proxy requests.
type Mode string

const (
	ModeOK        Mode = "ok"
	ModeReject407 Mode = "407"
	ModeReject502 Mode = "502"
	ModeHangup    Mode = "hangup"
)

// ParseMode converts a mode name ("ok", "407", "502", "hangup") into a Mode.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeOK, ModeReject407, ModeReject502, ModeHangup:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q (want ok, 407, 502 or hangup)", s)
}

// Config describes one fake upstream.
type Config struct {
	Name string // shown in responses and logs, e.g. "stg-a"
	Addr string // listen address, e.g. "127.0.0.1:18431" or "127.0.0.1:0"
	Mode Mode   // defaults to ModeOK
	// Logf, when set, receives one line per proxy request.
	Logf func(format string, args ...any)
}

// Upstream is a running fake upstream proxy.
type Upstream struct {
	name string
	mode Mode
	logf func(format string, args ...any)

	ln        net.Listener // proxy listener (what pacchain dials)
	backendLn net.Listener // TLS backend that CONNECT tunnels lead to
	proxy     *http.Server
	backend   *http.Server

	mu       sync.Mutex
	connects []string              // CONNECT targets, e.g. "app.a.stg.test:443"
	requests []string              // absolute-form URLs, e.g. "http://app.a.stg.test/"
	tunnels  map[net.Conn]struct{} // hijacked connections, closed by Close
}

// Start launches a fake upstream and returns once it is listening.
func Start(cfg Config) (*Upstream, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeOK
	}
	if _, err := ParseMode(string(cfg.Mode)); err != nil {
		return nil, err
	}
	cert, err := selfSignedCert()
	if err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen backend: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		backendLn.Close()
		return nil, err
	}

	u := &Upstream{
		name:      cfg.Name,
		mode:      cfg.Mode,
		logf:      cfg.Logf,
		ln:        ln,
		backendLn: backendLn,
		tunnels:   make(map[net.Conn]struct{}),
	}
	u.backend = &http.Server{
		Handler:           http.HandlerFunc(u.servePage),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
		ReadHeaderTimeout: 5 * time.Second,
	}
	u.proxy = &http.Server{
		Handler:           http.HandlerFunc(u.serveProxy),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go u.backend.ServeTLS(backendLn, "", "")
	go u.proxy.Serve(ln)
	return u, nil
}

// Name returns the upstream's display name.
func (u *Upstream) Name() string { return u.name }

// Mode returns the upstream's mode.
func (u *Upstream) Mode() Mode { return u.mode }

// Addr returns the proxy listen address as "host:port".
func (u *Upstream) Addr() string { return u.ln.Addr().String() }

// Connects returns the CONNECT targets received so far.
func (u *Upstream) Connects() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.connects...)
}

// Requests returns the absolute-form request URLs received so far.
func (u *Upstream) Requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.requests...)
}

// Close stops the upstream and closes any open tunnels.
func (u *Upstream) Close() error {
	err := u.proxy.Close()
	u.backend.Close()
	u.mu.Lock()
	for c := range u.tunnels {
		c.Close()
	}
	u.mu.Unlock()
	return err
}

// serveProxy handles requests arriving at the proxy listener.
func (u *Upstream) serveProxy(w http.ResponseWriter, r *http.Request) {
	var target string
	switch {
	case r.Method == http.MethodConnect:
		target = r.Host // "host:port"
		u.mu.Lock()
		u.connects = append(u.connects, target)
		u.mu.Unlock()
	case r.URL.IsAbs():
		target = r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
		u.mu.Lock()
		u.requests = append(u.requests, target)
		u.mu.Unlock()
	default:
		http.Error(w, "fakeupstream: expected CONNECT or an absolute-form request", http.StatusBadRequest)
		return
	}

	switch u.mode {
	case ModeReject407:
		u.log("%s %s %s → 407", u.name, r.Method, target)
		w.Header().Set("Proxy-Authenticate", `Basic realm="fakeupstream"`)
		http.Error(w, "fakeupstream "+u.name+": proxy authentication required", http.StatusProxyAuthRequired)
	case ModeReject502:
		u.log("%s %s %s → 502", u.name, r.Method, target)
		http.Error(w, "fakeupstream "+u.name+": cannot reach target", http.StatusBadGateway)
	case ModeHangup:
		u.log("%s %s %s → hang up", u.name, r.Method, target)
		if conn, _, err := hijack(w); err == nil {
			conn.Close()
		}
	default:
		u.log("%s %s %s → ok", u.name, r.Method, target)
		if r.Method == http.MethodConnect {
			u.tunnel(w)
		} else {
			u.servePage(w, r)
		}
	}
}

// tunnel answers a CONNECT with 200 and pipes the client to the TLS backend,
// regardless of which host the client asked for — like an STG proxy that
// maps production hostnames onto staging servers.
func (u *Upstream) tunnel(w http.ResponseWriter) {
	client, clientBuf, err := hijack(w)
	if err != nil {
		return
	}
	backend, err := net.Dial("tcp", u.backendLn.Addr().String())
	if err != nil {
		client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		client.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		backend.Close()
		return
	}

	u.track(client, backend)
	defer u.untrack(client, backend)

	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, clientBuf); done <- struct{}{} }()
	go func() { io.Copy(client, backend); done <- struct{}{} }()
	<-done // one side finished: close both so the other copy returns too
	client.Close()
	backend.Close()
	<-done
}

// servePage is the backend every request ends up at. It reports which
// upstream served it, with a per-upstream background colour for browsers.
//
// Test helpers in the response headers:
//
//	X-Fake-Body-Bytes       request body size (a retried request arrived intact?)
//	X-Fake-Accept-Encoding  the Accept-Encoding this upstream received ("" if none)
//
// GzipPath answers with a gzip-compressed body and Content-Encoding: gzip,
// whatever the client accepts, so tests can see whether a proxy in between
// decompressed it.
func (u *Upstream) servePage(w http.ResponseWriter, r *http.Request) {
	n, _ := io.Copy(io.Discard, r.Body)
	w.Header().Set("X-Fake-Upstream", u.name)
	w.Header().Set("X-Fake-Body-Bytes", strconv.FormatInt(n, 10))
	w.Header().Set("X-Fake-Accept-Encoding", r.Header.Get("Accept-Encoding"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:monospace;background:hsl(%d,70%%,85%%);padding:2em">
<h1>fake upstream: %s</h1>
<p>host: %s<br>method: %s<br>path: %s</p>
</body></html>
`, hue(u.name),
		html.EscapeString(u.name),
		html.EscapeString(r.Host),
		html.EscapeString(r.Method),
		html.EscapeString(r.URL.Path))

	if r.URL.Path == GzipPath {
		body := GzipBytes([]byte(page))
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
		return
	}
	io.WriteString(w, page)
}

// GzipPath is the path at which servePage answers with a gzip body.
const GzipPath = "/gzip"

// GzipBytes returns b gzip-compressed.
func GzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func (u *Upstream) log(format string, args ...any) {
	if u.logf != nil {
		u.logf(format, args...)
	}
}

func (u *Upstream) track(conns ...net.Conn) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range conns {
		u.tunnels[c] = struct{}{}
	}
}

func (u *Upstream) untrack(conns ...net.Conn) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range conns {
		delete(u.tunnels, c)
	}
}

// hijack takes over the raw connection. The returned reader includes any
// bytes the HTTP server had already buffered from the client.
func hijack(w http.ResponseWriter) (net.Conn, *bufio.Reader, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return conn, rw.Reader, nil
}

// hue maps a name to a stable colour so each upstream's page looks different.
func hue(name string) int {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int(h.Sum32() % 360)
}

// selfSignedCert creates a throwaway certificate for the TLS backend.
// Clients must skip verification (Burp does so for upstream servers by default).
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "pacchain fake upstream"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"*.test", "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
