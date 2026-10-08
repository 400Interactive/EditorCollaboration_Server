//go:build !windows

package main

// startTray only exists on Windows; Linux and Mac servers usually run headless.
func startTray(*server) func() {
	return func() {}
}
