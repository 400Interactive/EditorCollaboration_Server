package main

import "syscall"

const detachedProcess = 0x00000008

// detachedProcAttr starts the child without console window, so closing the terminal does not stop it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
