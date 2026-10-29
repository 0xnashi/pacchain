// Command fakeupstream starts local fake upstream proxies for testing pacchain
// without a VPN. Pair it with examples/dev.pac:
//
//	make devenv                                         # terminal 1
//	./bin/pacchain -c examples/dev.pac -l 127.0.0.1:8888 -v   # terminal 2
//	curl -k -x http://127.0.0.1:8888 https://app.a.stg.test/  # terminal 3
//
// Each -u flag adds one upstream: name=host:port[,mode]
// where mode is ok (default), 407, 502 or hangup.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/0xnashi/pacchain/internal/fakeupstream"
)

// defaults match the proxies referenced by examples/dev.pac.
// 127.0.0.1:18999 is intentionally left unused to simulate a dead proxy.
var defaults = []string{
	"stg-a=127.0.0.1:18431",
	"stg-b=127.0.0.1:18432",
	"deny=127.0.0.1:18407,407",
	"badgw=127.0.0.1:18502,502",
	"hangup=127.0.0.1:18000,hangup",
}

type specList []string

func (s *specList) String() string     { return strings.Join(*s, " ") }
func (s *specList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var specs specList
	flag.Var(&specs, "u", "upstream `name=host:port[,mode]`; repeatable (mode: ok, 407, 502, hangup)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "fakeupstream — local fake upstream proxies for pacchain development\n\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nWithout -u, starts the set used by examples/dev.pac:\n  %s\n", strings.Join(defaults, "\n  "))
	}
	flag.Parse()
	if len(specs) == 0 {
		specs = defaults
	}

	log.SetFlags(log.Ltime)
	logf := func(format string, args ...any) { log.Printf("[+] "+format, args...) }

	var running []*fakeupstream.Upstream
	for _, spec := range specs {
		cfg, err := parseSpec(spec)
		if err != nil {
			log.Fatalf("[-] %v", err)
		}
		cfg.Logf = logf
		u, err := fakeupstream.Start(cfg)
		if err != nil {
			log.Fatalf("[-] start %s on %s: %v", cfg.Name, cfg.Addr, err)
		}
		running = append(running, u)
		log.Printf("[*] %-7s listening on %s  mode=%s", u.Name(), u.Addr(), u.Mode())
	}
	log.Printf("[*] 127.0.0.1:18999 left unused (dead proxy). Ctrl+C to stop.")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	for _, u := range running {
		u.Close()
	}
	log.Printf("[*] stopped")
}

// parseSpec parses "name=host:port[,mode]".
func parseSpec(spec string) (fakeupstream.Config, error) {
	name, rest, ok := strings.Cut(spec, "=")
	if !ok || name == "" || rest == "" {
		return fakeupstream.Config{}, fmt.Errorf("invalid -u %q, want name=host:port[,mode]", spec)
	}
	addr, modeStr, _ := strings.Cut(rest, ",")
	mode := fakeupstream.ModeOK
	if modeStr != "" {
		m, err := fakeupstream.ParseMode(modeStr)
		if err != nil {
			return fakeupstream.Config{}, fmt.Errorf("invalid -u %q: %w", spec, err)
		}
		mode = m
	}
	return fakeupstream.Config{Name: name, Addr: addr, Mode: mode}, nil
}
