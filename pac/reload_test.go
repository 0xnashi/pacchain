package pac

// Reload must never leave the engine without a working PAC: the new PAC is
// fully loaded and checked first, and only then replaces the old one.

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// switchingLoader serves whatever PAC source it currently holds.
type switchingLoader struct {
	mu  sync.Mutex
	src string
}

func (l *switchingLoader) set(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.src = src
}

func (l *switchingLoader) load() (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.src, nil
}

func answering(answer string) string {
	return `function FindProxyForURL(url, host) { return "` + answer + `"; }`
}

func startSwitching(t *testing.T, initial string) (*OttoEngine, *switchingLoader) {
	t.Helper()
	l := &switchingLoader{src: initial}
	o := NewOttoEngine(OttoLoader(l.load), OttoTimeout(200*time.Millisecond))
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Stop() })
	return o, l
}

func lookup(t *testing.T, o *OttoEngine) string {
	t.Helper()
	p, err := o.FindProxyForURL(&url.URL{Scheme: "https", Host: "app.test"})
	if err != nil {
		t.Fatalf("FindProxyForURL: %v", err)
	}
	return p.String()
}

func TestReloadSwitchesToNewPAC(t *testing.T) {
	o, l := startSwitching(t, answering("PROXY a.test:1"))
	l.set(answering("PROXY b.test:2"))
	if err := o.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := lookup(t, o); got != "PROXY b.test:2" {
		t.Errorf("after reload: %s", got)
	}
}

func TestReloadKeepsPreviousPACWhenNewOneIsBroken(t *testing.T) {
	broken := map[string]string{
		"syntax error":        `function FindProxyForURL(url, host) { return "PROXY b.test:2" `,
		"no FindProxyForURL":  `var hosts = ["b.test"];`,
		"loops while loading": `while (true) {}` + answering("PROXY b.test:2"),
	}
	for name, src := range broken {
		t.Run(name, func(t *testing.T) {
			o, l := startSwitching(t, answering("PROXY a.test:1"))
			l.set(src)
			if err := o.Reload(); err == nil {
				t.Fatal("Reload accepted a broken PAC")
			}
			if got := lookup(t, o); got != "PROXY a.test:1" {
				t.Errorf("previous PAC lost: %s", got)
			}
		})
	}
}

func TestStartRejectsPACWithoutFindProxyForURL(t *testing.T) {
	o := NewOttoEngine(OttoStringLoader(`var hosts = [];`))
	err := o.Start()
	if err == nil || !strings.Contains(err.Error(), "FindProxyForURL") {
		t.Fatalf("Start err = %v, want an error naming FindProxyForURL", err)
	}
}

// Lookups running during reloads must always get an answer from the old or
// the new PAC — never an error from a half-swapped engine.
func TestLookupsDuringReloadNeverFail(t *testing.T) {
	o, l := startSwitching(t, answering("PROXY a.test:1"))
	stop := make(chan struct{})
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p, err := o.FindProxyForURL(&url.URL{Scheme: "https", Host: "app.test"})
			if err == nil && p.String() != "PROXY a.test:1" && p.String() != "PROXY b.test:2" {
				err = errors.New("unexpected answer " + p.String())
			}
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 50; i++ {
		if i%2 == 0 {
			l.set(answering("PROXY b.test:2"))
		} else {
			l.set(answering("PROXY a.test:1"))
		}
		if err := o.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("lookup failed during reload: %v", err)
	default:
	}
}
