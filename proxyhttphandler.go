package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0xnashi/pacchain/pac"
)

type proxyHTTPHandler struct {
	proxyFinder     pac.ProxyFinder
	httpClient      *http.Client
	dialer          *net.Dialer
	nonProxyHandler http.Handler

	con     *console // connection log; nil disables it
	verbose bool     // -v: tunnel close stats and extra detail
	pacName string   // shown in log lines, e.g. "env-a.pac"

	// allowDirectFallback permits DIRECT after a proxy option failed
	// (--allow-direct-fallback). Off by default: it can reach production.
	allowDirectFallback bool

	tunnelsMu sync.Mutex
	tunnels   map[net.Conn]net.Conn // open CONNECT tunnels: client → server
}

// route is the routing decision for one request.
type route struct {
	target  string        // what the client asked for, safe to log (no query string)
	options pac.Proxies   // everything the PAC returned, in order
	proxy   pac.Proxy     // the option being tried / used (pac.DirectProxy for DIRECT)
	took    time.Duration // PAC evaluation time
}

// routeKey carries a route from doHTTPProxy to the transport's Proxy hook,
// so the PAC is evaluated once per request.
type routeKey struct{}

func newProxyHTTPHandler(
	proxyFinder pac.ProxyFinder,
	nonProxyHandler http.Handler,
) *proxyHTTPHandler {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 10 * time.Second,
	}
	transport := &http.Transport{
		DisableKeepAlives: false,
		// Never add "Accept-Encoding: gzip" or decompress responses: pacchain
		// relays bytes as they are, so Burp sees what the server sent.
		DisableCompression:    true,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       5 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
	}
	handler := &proxyHTTPHandler{
		proxyFinder: proxyFinder,
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Don't follow redirects, but do return their contents.
				return http.ErrUseLastResponse
			},
			Jar: nil,
		},
		dialer:          dialer,
		nonProxyHandler: nonProxyHandler,
	}
	transport.Proxy = handler.transportProxy
	return handler
}

func (h *proxyHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.ToUpper(r.Method) == "CONNECT" {
		h.doConnectProxy(w, r)
	} else if r.URL.IsAbs() {
		h.doHTTPProxy(w, r)
	} else if h.nonProxyHandler != nil {
		h.nonProxyHandler.ServeHTTP(w, r)
	} else {
		http.Error(w, "", http.StatusBadRequest)
	}
}

// decide runs the PAC for r. An empty PAC answer means DIRECT.
func (h *proxyHTTPHandler) decide(r *http.Request) (route, error) {
	start := time.Now()
	proxies, err := h.proxyFinder.FindProxyForURL(r.URL)
	rt := route{target: logTarget(r), took: time.Since(start)}
	if err != nil {
		return rt, err
	}
	if len(proxies) == 0 {
		proxies = pac.Proxies{pac.DirectProxy}
	}
	rt.options = proxies
	return rt, nil
}

// transportProxy is the http.Transport Proxy hook for the HTTP path. It uses
// the option doHTTPProxy is currently trying, passed in the request context.
func (h *proxyHTTPHandler) transportProxy(r *http.Request) (*url.URL, error) {
	rt, ok := r.Context().Value(routeKey{}).(route)
	if !ok {
		var err error
		if rt, err = h.decide(r); err != nil {
			return nil, err
		}
		rt.proxy = rt.options[0]
	}
	if rt.proxy == pac.DirectProxy {
		return nil, nil
	}
	proxyURL := &url.URL{Host: proxyAddr(rt.proxy)}
	if proxyAuth := r.Header.Get("Proxy-Authorization"); proxyAuth != "" {
		if u, p, ok := parseBasicAuth(proxyAuth); ok {
			proxyURL.User = url.UserPassword(u, p)
		}
	}
	return proxyURL, nil
}

func (h *proxyHTTPHandler) doConnectProxy(w http.ResponseWriter, r *http.Request) {
	rt, err := h.decide(r)
	if err != nil {
		h.con.errorf("CONNECT %s → PAC error: %v", rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	removeProxyHeaders(r)
	detail := h.decisionDetail(rt)

	// Open the tunnel before answering the client: pacchain says 200 only
	// once an upstream said 200 (or DIRECT connected).
	var (
		serverConn   net.Conn
		serverReader io.Reader
	)
	f := newFailover(rt.options, h.allowDirectFallback)
	for p, ok := f.next(); ok; p, ok = f.next() {
		var err error
		if f.blocked(p) {
			err = errDirectFallbackBlocked
		} else {
			serverConn, serverReader, err = h.openTunnel(r, p)
		}
		if err == nil {
			rt.proxy = p
			break
		}
		f.failed(p, err)
		h.logFailure(f, "CONNECT", rt.target, p, err, detail)
	}
	if serverConn == nil {
		if answer, from, ok := f.answer(); ok {
			answer.relay(w, from) // the upstream's own refusal, source marked
		} else {
			http.Error(w, f.report(rt.target), http.StatusBadGateway)
		}
		return
	}
	defer serverConn.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		h.con.errorf("CONNECT %s: unable to get hijacker", rt.target)
		http.Error(w, "unable to get hijacker", http.StatusBadGateway)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		h.con.errorf("CONNECT %s: %v", rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer clientConn.Close()
	h.trackTunnel(clientConn, serverConn)
	defer h.untrackTunnel(clientConn)
	clientConn.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))

	if h.verbose {
		detail += " remote=" + serverConn.RemoteAddr().String()
	}
	if f.fellBackToDirect(rt.proxy) {
		h.con.warnf("CONNECT %s → DIRECT after a proxy failed: connected to the target directly, not through a proxy%s", rt.target, detail)
	} else {
		h.con.connf("CONNECT %s → %s%s", rt.target, rt.proxy, detail)
	}

	opened := time.Now()
	var sent, received int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		received, _ = io.Copy(clientConn, serverReader)
		clientConn.SetDeadline(time.Now().Add(10 * time.Millisecond))
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		// clientBuf holds anything the client sent right after its CONNECT.
		sent, _ = io.Copy(serverConn, clientBuf.Reader)
		serverConn.SetDeadline(time.Now().Add(10 * time.Millisecond))
	}()
	wg.Wait()

	if h.verbose {
		h.con.infof("CONNECT %s closed after %s (sent %s, received %s)",
			rt.target, time.Since(opened).Round(time.Millisecond), humanBytes(sent), humanBytes(received))
	}
}

func (h *proxyHTTPHandler) doHTTPProxy(w http.ResponseWriter, r *http.Request) {
	rt, err := h.decide(r)
	if err != nil {
		h.con.errorf("%s %s → PAC error: %v", r.Method, rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	removeProxyHeaders(r)
	detail := h.decisionDetail(rt)

	body, canRetry, err := replayableBody(r, len(rt.options) > 1)
	if err != nil {
		h.con.errorf("%s %s: read request body: %v", r.Method, rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	var (
		resp     *http.Response
		answered bool // resp is a proxy's own 407: relay it, marked
	)
	f := newFailover(rt.options, h.allowDirectFallback)
	for p, ok := f.next(); ok; p, ok = f.next() {
		var err error
		if f.blocked(p) {
			err = errDirectFallbackBlocked
		} else {
			rt.proxy = p
			req := r.WithContext(context.WithValue(r.Context(), routeKey{}, rt))
			if body != nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
			}
			resp, err = h.httpClient.Do(req)
			if err != nil {
				err = unwrapURLError(err)
			} else if resp.StatusCode == http.StatusProxyAuthRequired && p != pac.DirectProxy {
				// 407 only ever comes from a proxy: log it as the proxy's
				// answer, don't fail over, relay it with its source marked.
				f.failed(p, &upstreamAnswerError{status: resp.Status, code: resp.StatusCode})
				h.logFailure(f, r.Method, rt.target, p, f.attempts[len(f.attempts)-1].err, detail)
				answered = true
				break
			}
		}
		if err == nil {
			break
		}
		if !canRetry {
			f.stop() // the body was streamed and cannot be sent again
		}
		f.failed(p, err)
		h.logFailure(f, r.Method, rt.target, p, err, detail)
	}
	if resp == nil {
		http.Error(w, f.report(rt.target), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	switch {
	case answered:
		// already logged as the proxy's answer by logFailure
	case f.fellBackToDirect(rt.proxy):
		h.con.warnf("%s %s → DIRECT after a proxy failed: sent to the target directly, not through a proxy%s %d",
			r.Method, rt.target, detail, resp.StatusCode)
	default:
		h.con.connf("%s %s → %s%s %d", r.Method, rt.target, rt.proxy, detail, resp.StatusCode)
	}

	wh := w.Header()
	clearHeaders(wh)
	copyHeaders(wh, resp.Header)
	if answered {
		wh.Set(upstreamHeader, rt.proxy.String())
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (h *proxyHTTPHandler) trackTunnel(client, server net.Conn) {
	h.tunnelsMu.Lock()
	defer h.tunnelsMu.Unlock()
	if h.tunnels == nil {
		h.tunnels = make(map[net.Conn]net.Conn)
	}
	h.tunnels[client] = server
}

func (h *proxyHTTPHandler) untrackTunnel(client net.Conn) {
	h.tunnelsMu.Lock()
	defer h.tunnelsMu.Unlock()
	delete(h.tunnels, client)
}

// closeTunnels closes every open CONNECT tunnel and idle upstream HTTP
// connection, so nothing keeps a route chosen by a previous PAC. It returns
// the number of tunnels closed.
func (h *proxyHTTPHandler) closeTunnels() int {
	h.tunnelsMu.Lock()
	n := len(h.tunnels)
	for client, server := range h.tunnels {
		client.Close()
		server.Close()
	}
	h.tunnels = nil
	h.tunnelsMu.Unlock()
	h.httpClient.CloseIdleConnections()
	return n
}

// decisionDetail is the parenthesised part of a log line:
// the PAC name, the raw PAC answer when it offered more than one option
// (or always with -v), and the PAC evaluation time.
func (h *proxyHTTPHandler) decisionDetail(rt route) string {
	var parts []string
	pacPart := h.pacName
	if len(rt.options) > 1 || h.verbose {
		label := h.pacName
		if label == "" {
			label = "PAC"
		}
		pacPart = fmt.Sprintf("%s: %q", label, rt.options.String())
	}
	if pacPart != "" {
		parts = append(parts, pacPart)
	}
	parts = append(parts, formatDuration(rt.took))
	return " (" + strings.Join(parts, ", ") + ")"
}

// logTarget describes what the client asked for without anything secret:
// "host:port" for CONNECT, scheme://host/path for HTTP (no query, fragment
// or userinfo).
func logTarget(r *http.Request) string {
	if r.Method == http.MethodConnect {
		return r.Host
	}
	return r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
}

func proxyAddr(p pac.Proxy) string {
	return net.JoinHostPort(p.Hostname, strconv.Itoa(p.Port))
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	}
	return d.Round(time.Millisecond).String()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func removeProxyHeaders(r *http.Request) {
	// this must be reset when serving a request with the client
	r.RequestURI = ""
	// curl can add that, see
	// http://homepage.ntlworld.com/jonathan.deboynepollard/FGA/web-proxy-connection-header.html
	r.Header.Del("Proxy-Connection")
	// Connection is single hop Header:
	// http://www.w3.org/Protocols/rfc2616/rfc2616.txt
	// 14.10 Connection
	//   The Connection general-header field allows the sender to specify
	//   options that are desired for that particular connection and MUST NOT
	//   be communicated by proxies over further connections.
	r.Header.Del("Connection")
}

func clearHeaders(dst http.Header) {
	for k := range dst {
		dst.Del(k)
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// parseBasicAuth parses an HTTP Basic Authentication string.
// "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==" returns ("Aladdin", "open sesame", true).
func parseBasicAuth(auth string) (username, password string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return
	}
	cs := string(c)
	s := strings.IndexByte(cs, ':')
	if s < 0 {
		return
	}
	return cs[:s], cs[s+1:], true
}
