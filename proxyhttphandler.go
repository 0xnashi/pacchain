package main

import (
	"context"
	"encoding/base64"
	"errors"
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
	proxySelector   pac.ProxySelector
	httpClient      *http.Client
	dialer          *net.Dialer
	nonProxyHandler http.Handler

	con     *console // connection log; nil disables it
	verbose bool     // -v: tunnel close stats and extra detail
	pacName string   // shown in log lines, e.g. "env-a.pac"
}

// route is the routing decision for one request.
type route struct {
	target  string        // what the client asked for, safe to log (no query string)
	options pac.Proxies   // everything the PAC returned
	proxy   pac.Proxy     // the option pacchain uses (pac.DirectProxy for DIRECT)
	took    time.Duration // PAC evaluation time
}

// routeKey carries a route from doHTTPProxy to the transport's Proxy hook,
// so the PAC is evaluated once per request.
type routeKey struct{}

func newProxyHTTPHandler(
	proxyFinder pac.ProxyFinder,
	proxySelector pac.ProxySelector,
	nonProxyHandler http.Handler,
) *proxyHTTPHandler {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 10 * time.Second,
	}
	transport := &http.Transport{
		DisableKeepAlives:     false,
		DisableCompression:    false,
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
		proxyFinder:   proxyFinder,
		proxySelector: proxySelector,
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

// decide runs the PAC for r and selects the proxy to use.
func (h *proxyHTTPHandler) decide(r *http.Request) (route, error) {
	start := time.Now()
	proxies, err := h.proxyFinder.FindProxyForURL(r.URL)
	rt := route{target: logTarget(r), took: time.Since(start)}
	if err != nil {
		return rt, err
	}
	rt.options = proxies
	rt.proxy = h.proxySelector.SelectProxy(proxies)
	return rt, nil
}

// transportProxy is the http.Transport Proxy hook for the HTTP path.
func (h *proxyHTTPHandler) transportProxy(r *http.Request) (*url.URL, error) {
	rt, ok := r.Context().Value(routeKey{}).(route)
	if !ok {
		var err error
		if rt, err = h.decide(r); err != nil {
			return nil, err
		}
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
	var (
		clientConn net.Conn
		serverConn net.Conn
		err        error
	)

	rt, err := h.decide(r)
	if err != nil {
		h.con.errorf("CONNECT %s → PAC error: %v", rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	if rt.proxy == pac.DirectProxy {
		serverConn, err = h.dialer.Dial("tcp", r.URL.Host)
	} else {
		serverConn, err = h.dialer.Dial("tcp", proxyAddr(rt.proxy))
	}
	if err != nil {
		h.con.errorf("CONNECT %s → %s failed: %v%s", rt.target, rt.proxy, err, h.decisionDetail(rt))
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer serverConn.Close()
	if rt.proxy != pac.DirectProxy {
		removeProxyHeaders(r)
		r.Write(serverConn) // forward the CONNECT to the upstream proxy
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		err = errors.New("unable to get hijacker")
		h.con.errorf("CONNECT %s: %v", rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	clientConn, _, err = hj.Hijack()
	if err != nil {
		h.con.errorf("CONNECT %s: %v", rt.target, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer clientConn.Close()
	if rt.proxy == pac.DirectProxy {
		clientConn.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))
	}

	detail := h.decisionDetail(rt)
	if h.verbose {
		detail += " remote=" + serverConn.RemoteAddr().String()
	}
	h.con.connf("CONNECT %s → %s%s", rt.target, rt.proxy, detail)

	opened := time.Now()
	var sent, received int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		received, _ = io.Copy(clientConn, serverConn)
		clientConn.SetDeadline(time.Now().Add(10 * time.Millisecond))
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		sent, _ = io.Copy(serverConn, clientConn)
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
	resp, err := h.httpClient.Do(r.WithContext(context.WithValue(r.Context(), routeKey{}, rt)))
	if err != nil && resp == nil {
		h.con.errorf("%s %s → %s failed: %v%s", r.Method, rt.target, rt.proxy, err, h.decisionDetail(rt))
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	h.con.connf("%s %s → %s%s %d", r.Method, rt.target, rt.proxy, h.decisionDetail(rt), resp.StatusCode)

	wh := w.Header()
	clearHeaders(wh)
	copyHeaders(wh, resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
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
	// If no Accept-Encoding header exists, Transport will add the headers it can accept
	// and would wrap the response body with the relevant reader.
	r.Header.Del("Accept-Encoding")
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
