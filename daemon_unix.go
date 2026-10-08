//go:build !windows

package main

import "syscall"

// detachedProcAttr starts the child in its own session, so closing the terminal (SIGHUP) does not stop it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
