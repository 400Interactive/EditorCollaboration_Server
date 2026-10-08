//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// startTray shows the server in the notification area of the taskbar (hidden icons), with its status and a menu.
// The returned function removes the icon. Nothing is shown without an interactive desktop (service, session 0).
func startTray(s *server) func() {
	if s.cfg.noTray || !hasInteractiveDesktop() {
		return func() {}
	}
	texts := trayTextsFor(currentLanguage())
	ready := make(chan struct{})
	go func() {
		// The icon window belongs to the thread running its message loop.
		runtime.LockOSThread()
		systray.Run(func() {
			setupTray(s, texts)
			close(ready)
		}, nil)
	}()
	return func() {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
		}
		systray.Quit()
	}
}

func hasInteractiveDesktop() bool {
	var session uint32
	return windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session) == nil && session != 0
}


type trayTexts struct {
	clients  func(clients, rooms int) string
	port     string
	openLogs string
	openData string
	stop     string
}

func trayTextsFor(language string) trayTexts {
	_ = language
	return trayTexts{
		clients: func(clients, rooms int) string {
			switch {
			case clients == 0:
				return T("tray.noClients")
			case clients == 1:
				return T("tray.oneClient")
			case rooms <= 1:
				return T("tray.manyClients", clients)
			}
			return T("tray.manyRooms", clients, rooms)
		},
		port:     "Port %s",
		openLogs: T("tray.openLogs"),
		openData: T("tray.openData"),
		stop:     T("tray.stop"),
	}
}

func setupTray(s *server, texts trayTexts) {
	systray.SetIcon(trayIcon())

	title := systray.AddMenuItem("Editor Collaboration "+version, "")
	title.Disable()
	status := systray.AddMenuItem("", "")
	status.Disable()
	port := systray.AddMenuItem(fmt.Sprintf(texts.port, strings.TrimPrefix(portSuffix(s.cfg.addr), ":")), "")
	port.Disable()
	systray.AddSeparator()

	var openLogs *systray.MenuItem
	logsClicked := make(chan struct{})
	if s.cfg.logFile != "" {
		openLogs = systray.AddMenuItem(texts.openLogs, s.cfg.logFile)
		logsClicked = openLogs.ClickedCh
	}
	dataDir, _ := filepath.Abs(s.cfg.dataDir)
	openData := systray.AddMenuItem(texts.openData, dataDir)
	systray.AddSeparator()
	stop := systray.AddMenuItem(texts.stop, "")

	refresh := func() {
		clients, rooms := s.counts()
		text := texts.clients(clients, rooms)
		status.SetTitle(text)
		systray.SetTooltip("Editor Collaboration - " + text)
	}
	refresh()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				refresh()
			case <-logsClicked:
				shellOpen(s.cfg.logFile)
			case <-openData.ClickedCh:
				shellOpen(dataDir)
			case <-stop.ClickedCh:
				log.Print(T("server.stopTray"))
				s.requestShutdown()
				return
			case <-s.shutdownRequested:
				return
			}
		}
	}()
}

func shellOpen(path string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		log.Print(T("server.cannotOpen", path, err))
	}
}

// trayIcon draws two collaborators on a blue disc, as a .ico with 16 and 32 pixel images.
func trayIcon() []byte {
	sizes := []int{16, 32}
	images := make([][]byte, len(sizes))
	for index, size := range sizes {
		images[index] = iconDIB(size)
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for index, size := range sizes {
		binary.Write(&out, binary.LittleEndian, struct {
			Width, Height, Colors, Reserved uint8
			Planes, BitCount                uint16
			Bytes, Offset                   uint32
		}{uint8(size), uint8(size), 0, 0, 1, 32, uint32(len(images[index])), uint32(offset)})
		offset += len(images[index])
	}
	for _, image := range images {
		out.Write(image)
	}
	return out.Bytes()
}

// iconDIB renders one icon image: a 32-bit bottom-up bitmap followed by an empty AND mask (alpha is used instead).
func iconDIB(size int) []byte {
	const samples = 4
	background := [3]float64{0x25, 0x63, 0xEB}

	var out bytes.Buffer
	maskRow := (size + 31) / 32 * 4
	binary.Write(&out, binary.LittleEndian, struct {
		Size                  uint32
		Width, Height         int32
		Planes, BitCount      uint16
		Compression, SizeImg  uint32
		XPels, YPels          int32
		ColorsUsed, Important uint32
	}{40, int32(size), int32(size * 2), 1, 32, 0, uint32(size*size*4 + maskRow*size), 0, 0, 0, 0})

	for row := size - 1; row >= 0; row-- {
		for col := 0; col < size; col++ {
			var disc, white float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					x := (float64(col) + (float64(sx)+0.5)/samples) / float64(size)
					y := (float64(row) + (float64(sy)+0.5)/samples) / float64(size)
					if math.Hypot(x-0.5, y-0.5) > 0.5 {
						continue
					}
					disc++
					if isPersonPixel(x, y) {
						white++
					}
				}
			}
			coverage := disc / (samples * samples)
			mix := 0.0
			if disc > 0 {
				mix = white / disc
			}
			pixel := [4]byte{}
			for channel := 0; channel < 3; channel++ {
				pixel[2-channel] = byte(background[channel]*(1-mix) + 255*mix)
			}
			pixel[3] = byte(255 * coverage)
			out.Write(pixel[:])
		}
	}
	out.Write(make([]byte, maskRow*size))
	return out.Bytes()
}

// isPersonPixel draws a head and shoulders on each side of the disc, in unit coordinates (y down).
func isPersonPixel(x, y float64) bool {
	for _, centerX := range []float64{0.35, 0.65} {
		if math.Hypot(x-centerX, y-0.36) < 0.13 {
			return true
		}
		dx, dy := (x-centerX)/0.21, (y-0.86)/0.32
		if y < 0.8 && dx*dx+dy*dy < 1 {
			return true
		}
	}
	return false
}
