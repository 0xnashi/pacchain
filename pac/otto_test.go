package pac

import (
	"bytes"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
)

func assertOtto(t *testing.T, pac string, u string, p []Proxy, e string) {
	o := NewOttoEngine(
		OttoStringLoader(pac),
	)
	if err := o.Start(); err != nil {
		t.Errorf("failed to start otto: %q", err)
		return
	}
	assertOttoFind(t, o, u, p, e)
	if err := o.Stop(); err != nil {
		t.Errorf("failed to stop otto: %q", err)
		return
	}
}

func assertOttoFind(t *testing.T, o *OttoEngine, u string, p []Proxy, e string) {
	url, err := url.Parse(u)
	if err != nil {
		t.Errorf("failed to parse url: %q", err)
		return
	}
	proxies, err := o.FindProxyForURL(url)
	if e == "" {
		if err != nil {
			t.Errorf("unexpected error: %q", err)
		}
	} else {
		if err == nil {
			t.Errorf("expecting error %q, got nil", e)
		} else if err.Error() != e {
			t.Errorf("expecting error %q, got %q", e, err)
		}
	}
	if len(p) != len(proxies) {
		t.Errorf("expecting %d proxies, got %d", len(p), len(proxies))
	}
	maxI := len(p)
	if len(proxies) > maxI {
		maxI = len(proxies)
	}
	for i := 0; i < maxI; i++ {
		a := "undefined"
		b := "undefined"
		if len(p) > i {
			a = p[i].String()
		}
		if len(proxies) > i {
			b = proxies[i].String()
		}
		if a != b {
			t.Errorf("expecting proxy item at offset %d to be %q, got %q", i, a, b)
		}
	}
}

func TestOttoWithoutPac(t *testing.T) {
	o := NewOttoEngine()
	err := o.Start()
	if err == nil {
		t.Errorf("expecting an error on start")
		return
	}
	e := "pac loader has not been configured"
	if err.Error() != e {
		t.Errorf("expecting error %q, got %q", e, err)
	}
}

func TestOttoFindProxyForURL(t *testing.T) {
	assertOtto(
		t,
		"function FindProxyForURL(url, host){ return 1234; }",
		"http://www.example.com/page.html",
		[]Proxy{},
		"unsupported PAC command \"1234\"",
	)
}

func TestOttoWithUndefinedFindProxyForURLFunction(t *testing.T) {
	assertOtto(
		t,
		"1 + 1",
		"http://www.example.com/page.html",
		[]Proxy{},
		"ReferenceError: 'FindProxyForURL' is not defined",
	)
}

func TestOttoWithNonFindProxyForURLFunction(t *testing.T) {
	assertOtto(
		t,
		"FindProxyForURL = 1234",
		"http://www.example.com/page.html",
		[]Proxy{},
		"TypeError: \"FindProxyForURL\" is not a function",
	)
}

func TestOttoWithFindProxyForURLFunctionThatReturnsInvalidValue(t *testing.T) {
	assertOtto(
		t,
		"function FindProxyForURL(url, host){ return 1234; }",
		"http://www.example.com/page.html",
		[]Proxy{},
		"unsupported PAC command \"1234\"",
	)
}

func TestOttoWithDirectPAC(t *testing.T) {
	assertOtto(
		t,
		DirectPAC,
		"http://www.example.com/page.html",
		[]Proxy{DirectProxy},
		"",
	)
}

func TestOttoWithFindProxyForURLFunctionThatReturnsMultipleValues(t *testing.T) {
	assertOtto(
		t,
		"function FindProxyForURL(url, host){ return 'PROXY proxy.example.com:8080; DIRECT'; }",
		"http://www.example.com/page.html",
		[]Proxy{Proxy{"proxy.example.com", 8080}, DirectProxy},
		"",
	)
}

// -v must not print the PAC source: real PAC files list internal hostnames.
func TestStartDoesNotLogPACSource(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	o := NewOttoEngine(OttoStringLoader(
		`function FindProxyForURL(url, host) { if (host == "internal-secret.example.test") return "DIRECT"; return "DIRECT"; }`))
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	defer o.Stop()
	if strings.Contains(buf.String(), "internal-secret") {
		t.Errorf("PAC source leaked into the log:\n%s", buf.String())
	}
}

// Loading from a URL must not log its query string (it may carry a token).
func TestHTTPLoaderDoesNotLogQueryString(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	u, _ := url.Parse("http://127.0.0.1:1/proxy.pac?token=SECRET123")
	HTTPLoader(u)() // fails to connect; only the log line matters
	if strings.Contains(buf.String(), "SECRET123") {
		t.Errorf("query string leaked into the log:\n%s", buf.String())
	}
}
