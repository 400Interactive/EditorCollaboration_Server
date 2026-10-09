//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func replaceExecutable(newFile, dest string) error {
	mode := os.FileMode(0o755)
	if info, err := os.Stat(dest); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(newFile, mode); err != nil {
		return err
	}
	return os.Rename(newFile, dest)
}

func reexec() {
	exe, err := currentExecutable()
	if err != nil {
		return
	}
	args := append([]string{exe}, restartArgs()...)
	_ = syscall.Exec(exe, args, os.Environ())
}

func restartSystemdIfActive() bool {
	if os.Geteuid() != 0 {
		return false
	}
	if _, err := os.Stat("/etc/systemd/system/collab-server.service"); err != nil {
		return false
	}
	if exec.Command("systemctl", "is-active", "--quiet", "collab-server").Run() != nil {
		return false
	}
	return exec.Command("systemctl", "restart", "collab-server").Run() == nil
}

func showUpdateBalloon(title, body string) {}
