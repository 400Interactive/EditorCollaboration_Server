//go:build windows

package main

import "golang.org/x/sys/windows"

func windowsUILanguage() string {
	languages, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(languages) == 0 {
		return "en"
	}
	return languages[0]
}
