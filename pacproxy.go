package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xnashi/pacchain/pac"
)

// Name of the app
const Name = "pacchain"

// Version of the app. Release builds may override it with
// -ldflags "-X main.Version=x.y.z".
var Version = "0.1.0-dev"

// About the app
const About = "A PAC-aware chain proxy for routing Burp Suite traffic through PAC-selected upstream proxies"

// Repo where the app is located
const Repo = "https://github.com/0xnashi/pacchain"

// DefaultListen is loopback-only on purpose: anyone who can reach the
// listener can send traffic through your VPN and upstream proxies.
// Port 8888 avoids Burp's default 8080.
const DefaultListen = "127.0.0.1:8888"

type options struct {
	pac        string
	listen     string
	verbose    bool
	resolveURL string
	version    bool

	allowDirectFallback bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command line program. It returns the process exit code:
// 0 success, 1 runtime failure, 2 usage error.
func run(args []string, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet(Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.pac, "c", "", "PAC file name, url or javascript to use (required)")
	fs.StringVar(&o.listen, "l", DefaultListen, "Interface and port to listen on")
	fs.BoolVar(&o.verbose, "v", false, "send verbose output to STDERR")
	fs.StringVar(&o.resolveURL, "r", "", "Resolve the proxies for the provided url to STDOUT and exit")
	fs.BoolVar(&o.version, "version", false, "Print the version and exit")
	fs.BoolVar(&o.allowDirectFallback, "allow-direct-fallback", false,
		"Allow DIRECT after a PAC proxy option failed (off by default: may reach production)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s v%s\n\n%s\n%s\n\nUsage:\n", Name, Version, About, Repo)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2 // the flag package already printed the error and usage
	}
	if o.version {
		fmt.Fprintf(stdout, "%s %s\n", Name, Version)
		return 0
	}
	if strings.TrimSpace(o.pac) == "" {
		fmt.Fprintln(stderr, "Missing required flag -c")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "Unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	con := newConsole(stderr)
	if o.verbose {
		log.SetOutput(logWriter{con})
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetPrefix("")
	log.SetFlags(0) // the console adds the timestamp
	log.Printf("Starting %s v%s", Name, Version)

	engine := pac.NewOttoEngine(
		pac.OttoLoader(pac.SmartLoader(o.pac)),
	)
	if err := engine.Start(); err != nil {
		con.errorf("load PAC from %s: %v", describePACSource(o.pac), err)
		return 1
	}
	defer engine.Stop()

	if o.resolveURL != "" {
		return resolve(engine, o.resolveURL, stdout, con)
	}

	return listen(engine, o, con)
}

// resolve prints the PAC result for one URL, one proxy per line (-r).
func resolve(engine *pac.OttoEngine, rawURL string, stdout io.Writer, con *console) int {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		con.errorf("-r needs a full URL such as https://%s/", strings.TrimPrefix(rawURL, "//"))
		return 2
	}
	// Evaluate the URL as pacchain will see it in real traffic: for HTTPS
	// that is only scheme and host (the path never reaches it over CONNECT).
	proxies, err := engine.FindProxyForURL(browserPACURL(u))
	if err != nil {
		con.errorf("PAC evaluation failed for %s: %v", u.Host, err)
		return 1
	}
	for _, p := range proxies {
		fmt.Fprintln(stdout, p)
	}
	return 0
}

func listen(engine *pac.OttoEngine, o options, con *console) int {
	ln, err := openListener(o.listen)
	if err != nil {
		con.errorf("%v", err)
		return 1
	}
	if !isLoopbackListen(o.listen) {
		con.warnf("%s is reachable from other machines: anyone who can connect can send traffic "+
			"through your VPN and upstream proxies. Use -l 127.0.0.1:<port> unless you need this.", o.listen)
	}
	con.infof("%s %s listening on %s (PAC: %s)", Name, Version, ln.Addr(), describePACSource(o.pac))

	handler := newProxyHTTPHandler(engine, newNonProxyHTTPHandler())
	handler.con = con
	handler.verbose = o.verbose
	handler.pacName = shortPACName(o.pac)
	handler.allowDirectFallback = o.allowDirectFallback

	rl := &reloader{engine: engine, handler: handler, con: con, name: shortPACName(o.pac)}
	initSignalNotify(rl.reload)
	if isPACFile(o.pac) {
		watchPAC(o.pac, pacWatchInterval, con, func() { rl.reload("file changed") })
	}
	if o.allowDirectFallback {
		con.warnf("--allow-direct-fallback: if a proxy fails and the PAC lists DIRECT next, " +
			"traffic goes straight to the target (for STG hostnames: production)")
	}

	srv := &http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       60 * time.Second,
		Handler:           handler,
	}
	if err := srv.Serve(ln); err != nil {
		con.errorf("server stopped: %v", err)
		return 1
	}
	return 0
}

// openListener binds addr. A busy port is reported, never worked around:
// silently moving to another port would leave Burp pointing at nothing.
func openListener(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("cannot listen on %s: port already in use "+
			"(Burp, Charles, Fiddler or another pacchain?). Choose another port with -l", addr)
	}
	return nil, fmt.Errorf("cannot listen on %s: %v", addr, err)
}

// isLoopbackListen reports whether addr only accepts connections from this
// machine. An empty host (":8888") means all interfaces.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// shortPACName is the compact PAC label used on every connection line:
// the file name for files ("env-a.pac"), otherwise the describePACSource text.
func shortPACName(s string) string {
	d := describePACSource(s)
	if d == s {
		return filepath.Base(s)
	}
	return d
}

// describePACSource names the -c value for status lines without echoing
// inline scripts or URL query strings (which may carry tokens).
func describePACSource(s string) string {
	if _, err := pac.ParseFindProxyString(s); err == nil {
		return "inline result"
	}
	if strings.Contains(s, "FindProxyForURL") {
		return "inline script"
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return u.Scheme + "://" + u.Host + u.Path
	}
	return s
}
