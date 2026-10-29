package main

// Failover: try the PAC's options in order.
//
//	unusable proxy (unreachable, hangs up, no answer)  → try the next option
//	proxy answers with an error (407, 502, ...)        → stop, log it, and relay
//	                                                      the proxy's own response
//	DIRECT after a proxy option failed                 → blocked unless
//	                                                      --allow-direct-fallback
//
// A proxy that answers is working; failing over could route the request into
// a different environment. Its response is relayed unchanged (it may explain
// the refusal) with X-Pacchain-Upstream naming the proxy. DIRECT after a
// failed STG proxy sends a production hostname straight to production, so it
// needs an explicit opt-in. When no option answered at all, the client gets
// pacchain's own 502 listing every attempt.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0xnashi/pacchain/pac"
)

const (
	// upstreamAnswerTimeout bounds the wait for an upstream's CONNECT reply.
	upstreamAnswerTimeout = 10 * time.Second
	// maxReplayBody is the largest HTTP request body buffered so it can be
	// resent to the next option. Larger bodies are streamed, not retried.
	maxReplayBody = 1 << 20
	// maxRelayedErrorBody caps an upstream error page relayed to the client.
	maxRelayedErrorBody = 64 << 10
	// upstreamHeader names the proxy whose own response is being relayed.
	upstreamHeader = "X-Pacchain-Upstream"
)

// upstreamAnswerError: the upstream proxy is up and refused the request.
// pacchain never fails over on it, and relays the response it carries.
type upstreamAnswerError struct {
	status string // e.g. "407 Proxy Authentication Required"
	code   int
	header http.Header
	body   []byte
}

func (e *upstreamAnswerError) Error() string { return "answered " + e.status }

// relay writes the upstream's own response to the client, marked with the
// proxy it came from. Hop-by-hop and length headers are recomputed.
func (e *upstreamAnswerError) relay(w http.ResponseWriter, from pac.Proxy) {
	wh := w.Header()
	for k, vs := range e.header {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer",
			"Transfer-Encoding", "Upgrade", "Content-Length":
			continue
		}
		for _, v := range vs {
			wh.Add(k, v)
		}
	}
	wh.Set(upstreamHeader, from.String())
	w.WriteHeader(e.code)
	w.Write(e.body)
}

// answer returns the upstream response that ended the request, if any.
func (f *failover) answer() (*upstreamAnswerError, pac.Proxy, bool) {
	if len(f.attempts) == 0 {
		return nil, pac.Proxy{}, false
	}
	last := f.attempts[len(f.attempts)-1]
	var a *upstreamAnswerError
	if errors.As(last.err, &a) {
		return a, last.proxy, true
	}
	return nil, pac.Proxy{}, false
}

var errDirectFallbackBlocked = errors.New("DIRECT fallback blocked: a proxy option failed, " +
	"and DIRECT for this host may reach production; start pacchain with --allow-direct-fallback to permit it")

// attempt records one option that did not work.
type attempt struct {
	proxy pac.Proxy
	err   error
}

// failover walks a route's options in order and records what failed.
//
//	f := newFailover(rt.options, allowDirect)
//	for p, ok := f.next(); ok; p, ok = f.next() {
//		err := ... try p (or errDirectFallbackBlocked if f.blocked(p)) ...
//		if err == nil { success }
//		f.failed(p, err)        // then log; f.more() says if another try follows
//	}
type failover struct {
	options     pac.Proxies
	allowDirect bool
	i           int
	proxyFailed bool // a PROXY option failed, so a later DIRECT is a fallback
	stopped     bool // an upstream answered, or the request cannot be repeated
	attempts    []attempt
}

func newFailover(options pac.Proxies, allowDirect bool) *failover {
	return &failover{options: options, allowDirect: allowDirect}
}

// next returns the next option to try.
func (f *failover) next() (pac.Proxy, bool) {
	if f.stopped || f.i >= len(f.options) {
		return pac.Proxy{}, false
	}
	p := f.options[f.i]
	f.i++
	return p, true
}

// blocked reports whether p is a DIRECT fallback that is not permitted.
func (f *failover) blocked(p pac.Proxy) bool {
	return p == pac.DirectProxy && f.proxyFailed && !f.allowDirect
}

// failed records that p did not work and decides whether to keep going.
func (f *failover) failed(p pac.Proxy, err error) {
	f.attempts = append(f.attempts, attempt{p, err})
	var answered *upstreamAnswerError
	if errors.As(err, &answered) {
		f.stopped = true
	}
	if p != pac.DirectProxy {
		f.proxyFailed = true
	}
}

// stop prevents further attempts (e.g. the request body cannot be resent).
func (f *failover) stop() { f.stopped = true }

// more reports whether another option will be tried.
func (f *failover) more() bool {
	return !f.stopped && f.i < len(f.options)
}

// fellBackToDirect reports whether p is DIRECT reached after a proxy failed.
func (f *failover) fellBackToDirect(p pac.Proxy) bool {
	return p == pac.DirectProxy && f.proxyFailed
}

// report is the 502 body sent to the client when every option failed.
func (f *failover) report(target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pacchain: could not reach %s\n", target)
	for _, a := range f.attempts {
		fmt.Fprintf(&b, "  %s: %v\n", a.proxy, a.err)
	}
	return b.String()
}

// logFailure writes one line for a failed attempt: [!] when another option
// follows, [-] (with the decision detail) when it ends the request.
func (h *proxyHTTPHandler) logFailure(f *failover, method, target string, p pac.Proxy, err error, detail string) {
	msg := fmt.Sprintf("%s %s → %s failed: %v", method, target, p, err)
	if errors.Is(err, errDirectFallbackBlocked) {
		msg = fmt.Sprintf("%s %s → DIRECT fallback blocked (use --allow-direct-fallback to permit)", method, target)
	}
	if f.more() {
		h.con.warnf("%s, trying next option", msg)
	} else {
		h.con.errorf("%s%s", msg, detail)
	}
}

// openTunnel connects to target through p and returns the server side of the
// tunnel. For a proxy it sends the CONNECT and waits for the proxy's answer;
// only a 200 counts as success. The returned reader must be used for reading:
// it may already hold bytes received after the proxy's 200.
func (h *proxyHTTPHandler) openTunnel(r *http.Request, p pac.Proxy) (net.Conn, io.Reader, error) {
	if p == pac.DirectProxy {
		conn, err := h.dialer.Dial("tcp", r.URL.Host)
		if err != nil {
			return nil, nil, err
		}
		return conn, conn, nil
	}

	conn, err := h.dialer.Dial("tcp", proxyAddr(p))
	if err != nil {
		return nil, nil, err
	}
	conn.SetDeadline(time.Now().Add(upstreamAnswerTimeout))
	if err := r.Write(conn); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("send CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		conn.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, nil, fmt.Errorf("no answer to CONNECT within %s", upstreamAnswerTimeout)
		}
		return nil, nil, fmt.Errorf("closed the connection without answering CONNECT (%v)", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer conn.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxRelayedErrorBody))
		return nil, nil, &upstreamAnswerError{
			status: resp.Status, code: resp.StatusCode, header: resp.Header, body: body,
		}
	}
	conn.SetDeadline(time.Time{}) // the tunnel itself has no deadline
	return conn, br, nil
}

// replayableBody buffers an HTTP request body so it can be resent to the next
// option. It only does so when there is a next option to resend to.
func replayableBody(r *http.Request, severalOptions bool) (body []byte, canRetry bool, err error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, true, nil
	}
	if !severalOptions {
		return nil, false, nil // stream it; nothing to retry anyway
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxReplayBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > maxReplayBody {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), r.Body))
		return nil, false, nil
	}
	return b, true, nil
}

// unwrapURLError drops the *url.Error wrapper added by http.Client: its
// message repeats the full request URL, query string included.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
