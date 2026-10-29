package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// console prints the status lines the user always sees, with or without -v:
//
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

func (c *console) infof(format string, args ...any)  { c.printf("[*]", format, args...) }
func (c *console) warnf(format string, args ...any)  { c.printf("[!]", format, args...) }
func (c *console) errorf(format string, args ...any) { c.printf("[-]", format, args...) }

func (c *console) printf(prefix, format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.w, "%s %s %s\n", c.now().Format("15:04:05"), prefix, fmt.Sprintf(format, args...))
}
