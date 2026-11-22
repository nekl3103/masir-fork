package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
)

func appLanguage() string {
	if i18n.Locale() == "ru" {
		return "ru"
	}
	return "en"
}

func setAppLanguage(tag string) {
	if tag == "en" || tag == "ru" {
		mygo.App.SetLocale(tag)
	}
}

// tr translates app copy. Protocols, paths, rule IDs and names supplied by the
// user or provider stay in their original form.
func tr(english string) string {
	if appLanguage() == "ru" {
		if russian, ok := russianText[english]; ok {
			return russian
		}
	}
	return english
}

func (a *app) refreshLocalizedContent() {
	clear(a.rulesMetas)
	for _, key := range []string{"domain", "ip"} {
		a.localizeRuleGeo(key)
	}
	a.requestHeight()
}
