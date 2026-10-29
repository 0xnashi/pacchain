package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// console prints the status lines the user always sees, with or without -v:
//
//	15:04:05 [+] connection routed
//	15:04:05 [*] info
//	15:04:05 [!] warning
//	15:04:05 [-] error
//
// Timestamps use local time so they line up with Burp and the terminal clock.
type console struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func newConsole(w io.Writer) *console {
	return &console{w: w, now: time.Now}
}

func (c *console) connf(format string, args ...any)  { c.printf("[+]", format, args...) }
func (c *console) infof(format string, args ...any)  { c.printf("[*]", format, args...) }
func (c *console) warnf(format string, args ...any)  { c.printf("[!]", format, args...) }
func (c *console) errorf(format string, args ...any) { c.printf("[-]", format, args...) }

// printf writes one line. A nil console discards everything, so code paths
// such as tests can run without a log.
func (c *console) printf(prefix, format string, args ...any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.w, "%s %s %s\n", c.now().Format("15:04:05"), prefix, fmt.Sprintf(format, args...))
}

// logWriter routes the standard log package (used by pac/ in -v mode)
// through the console so every line has the same timestamp format.
type logWriter struct{ c *console }

func (w logWriter) Write(p []byte) (int, error) {
	w.c.printf("[*]", "%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
