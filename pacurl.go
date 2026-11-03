package main

// The url argument for FindProxyForURL, built the way browsers build it.
//
// Chrome and Firefox pass only scheme, host and (non-default) port for
// HTTPS URLs, so a PAC cannot see their path or query; for plain HTTP they
// pass the full URL. Neither passes a #fragment or user:password@.
// pacchain matches that so a PAC routes the same way here as in a browser.

import (
	"net"
	"net/url"
	"strings"
)

// browserPACURL returns the url a browser would give the PAC for u.
func browserPACURL(u *url.URL) *url.URL {
	if strings.EqualFold(u.Scheme, "https") {
		return httpsPACURL(u.Hostname(), u.Port())
	}
	out := *u
	out.User = nil
	out.Fragment = ""
	out.RawFragment = ""
	return &out
}

// connectPACURL returns the url for a "CONNECT host:port" request. Over a
// proxy, browsers use CONNECT for HTTPS, so it is evaluated as https.
func connectPACURL(hostport string) *url.URL {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = strings.Trim(hostport, "[]"), ""
	}
	return httpsPACURL(host, port)
}

// httpsPACURL is "https://host/" or, for a port other than 443,
// "https://host:port/". IPv6 hosts are bracketed.
func httpsPACURL(host, port string) *url.URL {
	h := host
	if strings.Contains(host, ":") {
		h = "[" + host + "]"
	}
	if port != "" && port != "443" {
		h = net.JoinHostPort(host, port)
	}
	return &url.URL{Scheme: "https", Host: h, Path: "/"}
}
