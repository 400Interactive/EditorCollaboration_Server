//go:build windows

package main

import (
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func replaceExecutable(newFile, dest string) error {
	old := dest + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dest, old); err != nil {
		return err
	}
	if err := os.Rename(newFile, dest); err != nil {
		_ = os.Rename(old, dest)
		return err
	}
	return nil
}

func reexec() {
	exe, err := currentExecutable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, restartArgs()...)
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
}

func restartSystemdIfActive() bool { return false }

const (
	nimAdd    = 0x0
	nimDelete = 0x2
	nifIcon   = 0x2
	nifTip    = 0x4
	nifInfo   = 0x10
	niifInfo  = 0x1
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procLoadIconW       = user32.NewProc("LoadIconW")
	shellNotifyIcon     = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
)

func showUpdateBalloon(title, body string) {
	className, err := windows.UTF16PtrFromString("STATIC")
	if err != nil {
		return
	}
	windowName, err := windows.UTF16PtrFromString("Editor Collaboration")
	if err != nil {
		return
	}
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return
	}
	const idiInformation = 32516
	icon, _, _ := procLoadIconW.Call(0, uintptr(idiInformation))

	var data notifyIconData
	data.CbSize = uint32(unsafe.Sizeof(data))
	data.HWnd = windows.HWND(hwnd)
	data.UID = 1
	data.UFlags = nifIcon | nifTip | nifInfo
	data.HIcon = windows.Handle(icon)
	data.DwInfoFlags = niifInfo
	copyUTF16(data.SzTip[:], "Editor Collaboration")
	copyUTF16(data.SzInfoTitle[:], title)
	copyUTF16(data.SzInfo[:], body)
	shellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	time.AfterFunc(8*time.Second, func() {
		shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
		procDestroyWindow.Call(hwnd)
	})
}

func copyUTF16(dst []uint16, text string) {
	encoded, err := windows.UTF16FromString(text)
	if err != nil || len(encoded) == 0 {
		return
	}
	if len(encoded) > len(dst) {
		encoded = encoded[:len(dst)-1]
		encoded = append(encoded, 0)
	}
	copy(dst, encoded)
}
