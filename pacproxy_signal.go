//go:build !plan9

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// initSignalNotify reloads the PAC on SIGHUP (`kill -HUP <pid>`). A failed
// reload is logged by reload and keeps the previous PAC; it never panics.
func initSignalNotify(reload func(reason string)) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP)
	go func() {
		for range sigChan {
			reload("SIGHUP")
		}
	}()
}
