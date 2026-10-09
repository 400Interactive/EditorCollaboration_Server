package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
)

//go:embed lang/*.json
var langFS embed.FS

// currentLanguage returns the language code to use for the server UI and logs.
// Priority: COLLAB_LANG env var, then OS language, then English.
func currentLanguage() string {
	if lang := os.Getenv("COLLAB_LANG"); lang != "" {
		return normalizeLang(lang)
	}
	if runtime.GOOS == "windows" {
		return normalizeLang(windowsUILanguage())
	}
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if lang := os.Getenv(env); lang != "" {
			return normalizeLang(lang)
		}
	}
	return "en"
}

func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.ReplaceAll(lang, "_", "-"))
	if idx := strings.Index(lang, "."); idx != -1 {
		lang = lang[:idx]
	}
	if idx := strings.Index(lang, "@"); idx != -1 {
		lang = lang[:idx]
	}
	switch lang {
	case "pt-br", "pt-br-x", "pt-br-u":
		return "pt-BR"
	case "zh-hans", "zh-cn", "zh-sg":
		return "zh-Hans"
	case "es-419", "es-mx", "es-ar", "es-cl", "es-co", "es-pe", "es-ve", "es-uy", "es-py", "es-bo", "es-ec", "es-sv", "es-hn", "es-ni", "es-cr", "es-pa", "es-do", "es-gt", "es-cu", "es-pr":
		return "es-419"
	}
	if strings.HasPrefix(lang, "zh-") {
		return "zh-Hans"
	}
	if strings.HasPrefix(lang, "pt") {
		return "pt-BR"
	}
	if strings.HasPrefix(lang, "es") {
		return "es"
	}
	if idx := strings.Index(lang, "-"); idx != -1 {
		lang = lang[:idx]
	}
	return lang
}

// T returns the translated string for the current language, or the English fallback.
func T(key string, args ...any) string {
	lang := currentLanguage()
	if translations[lang] == nil {
		lang = "en"
	}
	format := translations[lang][key]
	if format == "" {
		format = translations["en"][key]
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

var translations = loadTranslations()

func loadTranslations() map[string]map[string]string {
	out := map[string]map[string]string{}
	entries, err := langFS.ReadDir("lang")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := langFS.ReadFile("lang/" + name)
		if err != nil {
			panic(err)
		}
		var table map[string]string
		if err := json.Unmarshal(raw, &table); err != nil {
			panic(err)
		}
		out[strings.TrimSuffix(name, ".json")] = table
	}
	if out["en"] == nil {
		panic("lang/en.json is missing")
	}
	return out
}
