package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
)

type deeplinkImport struct {
	url      string
	name     string
	interval uint32
}

// parseDeeplink parses a masir:// deep link URL.
// Supported format:
// masir://import?url=[url-encoded-url]&name=[url-encoded-name]&refresh=[number]
// name and refresh are optional.
// refresh is in minutes and minimum to 15.
func parseDeeplink(raw string) (*deeplinkImport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New(tr("empty deeplink"))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf(tr("invalid deeplink URL: %w"), err)
	}
	if !strings.EqualFold(u.Scheme, "masir") {
		return nil, fmt.Errorf(tr("unsupported URL scheme: %q"), u.Scheme)
	}

	action := strings.Trim(u.Host, "/")
	if action == "" {
		action = strings.Trim(u.Path, "/")
	}
	if action == "" && u.Opaque != "" {
		action, _, _ = strings.Cut(u.Opaque, "?")
	}
	if !strings.EqualFold(action, "import") {
		return nil, fmt.Errorf(tr("unknown deeplink action: %q"), action)
	}

	q := u.Query()
	subURL := strings.TrimSpace(q.Get("url"))
	if subURL == "" {
		return nil, errors.New(tr("missing subscription url in deeplink"))
	}
	name := strings.TrimSpace(q.Get("name"))

	var interval uint32
	if refStr := strings.TrimSpace(q.Get("refresh")); refStr != "" {
		n, err := strconv.Atoi(refStr)
		if err != nil {
			return nil, fmt.Errorf(tr("invalid refresh parameter: %w"), err)
		}
		if n < 15 {
			n = 15
		}
		interval = uint32(n)
	}

	return &deeplinkImport{
		url:      subURL,
		name:     name,
		interval: interval,
	}, nil
}

// startImportSubscription switches to the subscription import page, populates the
// fields, and triggers the import action.
func (a *app) startImportSubscription(subURL, name string, interval uint32) {
	if a.path != addSubscriptionPath {
		a.resetRouter()
		a.openPage(addSubscriptionPath)
	}
	a.subURL = subURL
	a.subSources = nil
	a.subName = name
	a.subCustomInterval = interval
	a.subError = ""
	if subURL != "" {
		a.addSubscription()
	}
}

// openImportSubscription opens the app UI and starts importing the subscription.
func (s *shell) openImportSubscription(rawURL, name string, interval uint32) {
	if !s.open {
		s.show()
	} else if s.win != nil {
		s.win.Focus()
	}
	s.a.startImportSubscription(rawURL, name, interval)
	s.sync()
}

// handleDeeplink parses a deeplink and triggers the corresponding action once the app is ready.
func (s *shell) handleDeeplink(rawURL string) {
	dl, err := parseDeeplink(rawURL)
	if err != nil {
		log.Printf("deeplink: %v", err)
		return
	}
	mygo.App.WhenReady(func() {
		s.openImportSubscription(dl.url, dl.name, dl.interval)
	})
}
