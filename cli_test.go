package main

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/0xnashi/pacchain/pac"
)

// runCLI runs pacchain's command line in-process and captures its output.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionFlag(t *testing.T) {
	for _, flagName := range []string{"--version", "-version"} {
		code, out, _ := runCLI(flagName)
		if code != 0 {
			t.Errorf("%s: exit code %d, want 0", flagName, code)
		}
		if want := Name + " " + Version + "\n"; out != want {
			t.Errorf("%s: stdout %q, want %q", flagName, out, want)
		}
	}
}

func TestMissingPACIsUsageError(t *testing.T) {
	code, _, errOut := runCLI()
	if code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "Missing required flag -c") {
		t.Errorf("stderr does not explain the problem: %q", errOut)
	}
}

// 8080 belongs to Burp; pacchain must not fight over it, and must not be
// reachable from other machines by default.
func TestDefaultListenIsLoopback8888(t *testing.T) {
	if DefaultListen != "127.0.0.1:8888" {
		t.Errorf("DefaultListen = %q, want 127.0.0.1:8888", DefaultListen)
	}
	if !isLoopbackListen(DefaultListen) {
		t.Error("default listen address must be loopback-only")
	}
}

// A busy port is an error with a hint, never a silent switch to another port.
func TestListenPortInUseFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	code, _, errOut := runCLI("-c", pac.DirectPAC, "-l", busy.Addr().String())
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	for _, want := range []string{"[-]", busy.Addr().String(), "already in use", "-l"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q: %q", want, errOut)
		}
	}
}

func TestIsLoopbackListen(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:8888":   true,
		"localhost:8888":   true,
		"[::1]:8888":       true,
		":8888":            false, // all interfaces
		"0.0.0.0:8888":     false,
		"[::]:8888":        false,
		"192.168.1.5:8888": false,
		"not-an-address":   false,
	}
	for addr, want := range tests {
		if got := isLoopbackListen(addr); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestDescribePACSource(t *testing.T) {
	tests := map[string]string{
		"examples/dev.pac":                      "examples/dev.pac",
		"https://pac.example.test/p.pac?k=v":    "https://pac.example.test/p.pac", // never echo query strings
		pac.DirectPAC:                           "inline script",
		"PROXY proxy.example.test:8080; DIRECT": "inline result",
	}
	for in, want := range tests {
		if got := describePACSource(in); got != want {
			t.Errorf("describePACSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveFlag(t *testing.T) {
	src := `function FindProxyForURL(url, host) { return "PROXY 127.0.0.1:18432; DIRECT"; }`
	code, out, _ := runCLI("-c", src, "-r", "https://app.a.stg.test/")
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if out != "PROXY 127.0.0.1:18432\nDIRECT\n" {
		t.Errorf("stdout %q", out)
	}
}

// Without a scheme there is no host for the PAC to match; fail loudly.
func TestResolveFlagRejectsURLWithoutScheme(t *testing.T) {
	code, _, errOut := runCLI("-c", pac.DirectPAC, "-r", "app.a.stg.test")
	if code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(errOut, "https://") {
		t.Errorf("stderr should show an example URL: %q", errOut)
	}
}

// Errors must be visible without -v (the original only logged them in -v mode).
func TestMissingPACFileFailsVisibly(t *testing.T) {
	code, _, errOut := runCLI("-c", "does-not-exist.pac")
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(errOut, "[-]") || !strings.Contains(errOut, "does-not-exist.pac") {
		t.Errorf("stderr does not explain the problem: %q", errOut)
	}
}

func TestAllowDirectFallbackFlagIsAccepted(t *testing.T) {
	code, _, errOut := runCLI("-c", pac.DirectPAC, "--allow-direct-fallback", "-r", "https://a.test/")
	if code != 0 {
		t.Errorf("exit code %d: %s", code, errOut)
	}
}
