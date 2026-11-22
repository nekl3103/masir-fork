package main

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
)

func TestLanguageSwitchRedrawsSettingsAndKeepsVersionsAboveQuit(t *testing.T) {
	// Supply the locale directly for headless frames, without writing a system preference.
	previous := i18n.Locale()
	var russian atomic.Bool
	i18n.SetLocaleFunc(func() string {
		if russian.Load() {
			return "ru"
		}
		return "en"
	})
	t.Cleanup(func() { i18n.SetLocaleFunc(func() string { return previous }) })

	f := ukServers()
	f.status.AmneziawgVersion = "3.1.20260828"
	a := newTestApp(f)
	tt := tester(a)
	tt.Frame()
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !slices.Contains(tt.Texts(), "Language") {
		t.Fatalf("missing English language picker: %q", tt.Texts())
	}

	russian.Store(true)
	a.refreshLocalizedContent()
	tt.Frame()
	texts := tt.Texts()
	for _, want := range []string{"Настройки", "Язык", "Русский", "Блокировка при сбое VPN"} {
		if !slices.Contains(texts, want) {
			t.Errorf("missing Russian UI text %q", want)
		}
	}
	appVersion := slices.Index(texts, "Masir Fork v0.1.0")
	engineVersion := slices.Index(texts, "AmneziaWG v3.1.20260828")
	quit := slices.Index(texts, "Выйти из Masir")
	if appVersion < 0 || engineVersion <= appVersion || quit <= engineVersion {
		t.Errorf("versions should precede Quit: %q", texts)
	}
	if number, unit := speedParts(1024); number != "1" || unit != " КБ/с" {
		t.Errorf("Russian speed = %q %q", number, unit)
	}

	russian.Store(false)
	a.refreshLocalizedContent()
	tt.Frame()
	if !slices.Contains(tt.Texts(), "Quit Masir") {
		t.Errorf("switching back did not restore English: %q", tt.Texts())
	}
}
