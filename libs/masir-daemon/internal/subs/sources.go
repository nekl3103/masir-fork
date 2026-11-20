package subs

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Gozargah/Masir/libs/masir-daemon/internal/xray"
)

const maxSources = 8

// Source keeps its own last good response so a failed refresh leaves its servers usable.
type Source struct {
	URL             string    `json:"url"`
	Title           string    `json:"title,omitempty"`
	CheckedAt       time.Time `json:"checkedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	ProviderMinutes uint32    `json:"providerMinutes,omitempty"`
	Usage           *Usage    `json:"usage,omitempty"`
	Configs         []Config  `json:"configs"`
	Failed          bool      `json:"failed,omitempty"`
}

func (s Source) due(now time.Time, interval time.Duration) bool {
	if s.Failed {
		interval = min(interval, retryAfterFailure)
	}
	return !isTUICLink(s.URL) && now.Sub(s.CheckedAt) >= interval
}

func (s *Subscription) SourceURLs() []string {
	if len(s.Sources) == 0 {
		return []string{s.URL}
	}
	out := make([]string, len(s.Sources))
	for i, source := range s.Sources {
		out[i] = source.URL
	}
	return out
}

func (s *Subscription) savedSources() []Source {
	if len(s.Sources) != 0 {
		return s.Sources
	}
	return []Source{{
		URL: s.URL, Configs: s.Configs, CheckedAt: s.CheckedAt, UpdatedAt: s.UpdatedAt,
		ProviderMinutes: s.ProviderIntervalMinutes, Usage: s.Usage, Failed: s.LastError != "",
	}}
}

func (m *Manager) prepareSources(ctx context.Context, urls []string, previous []Source) ([]Source, error) {
	if len(urls) == 0 || len(urls) > maxSources {
		return nil, fmt.Errorf("enter between 1 and %d sources: %w", maxSources, ErrInvalid)
	}
	next := make([]Source, len(urls))
	fetch := make([]bool, len(urls))
	seen := make(map[string]bool, len(urls))
	for i, raw := range urls {
		u, err := checkURL(raw)
		if err != nil {
			return nil, fmt.Errorf("source %d: %w", i+1, err)
		}
		if seen[u] {
			return nil, fmt.Errorf("source %d repeats an earlier link: %w", i+1, ErrExists)
		}
		seen[u] = true
		old := slices.IndexFunc(previous, func(source Source) bool { return source.URL == u })
		if old >= 0 {
			next[i] = previous[old]
		} else {
			next[i] = Source{URL: u}
			fetch[i] = true
		}
	}
	results := m.fetchSources(ctx, next, fetch)
	for i, err := range results {
		if err != nil {
			return nil, fmt.Errorf("could not import source %d (%s): %w", i+1, hostOf(next[i].URL), err)
		}
	}
	return next, nil
}

func (m *Manager) fetchSources(ctx context.Context, sources []Source, fetch []bool) []error {
	var wg sync.WaitGroup
	errs := make([]error, len(sources))
	for i, enabled := range fetch {
		if !enabled {
			continue
		}
		wg.Go(func() {
			source := &sources[i]
			f, configs, err := m.download(ctx, source.URL)
			source.CheckedAt = m.now()
			source.Failed = err != nil
			if err != nil {
				// Network errors may contain the private subscription URL.
				errs[i] = ErrFetch
				if strings.HasPrefix(source.URL, "tuic://") {
					errs[i] = ErrInvalid
				}
				return
			}
			source.UpdatedAt = source.CheckedAt
			source.Title, source.Usage, source.Configs = f.title, f.usage, configs
			source.ProviderMinutes = uint32(f.interval.Minutes())
		})
	}
	wg.Wait()
	return errs
}

func (m *Manager) AddSources(ctx context.Context, urls []string, name string, interval uint32) (*Subscription, error) {
	next, err := m.prepareSources(ctx, urls, nil)
	if err != nil {
		return nil, err
	}
	s := &Subscription{
		ID: newID(), Name: cmp.Or(strings.TrimSpace(name), next[0].Title, hostOf(next[0].URL)),
		CreatedAt: m.now(), CustomIntervalMinutes: interval,
	}
	s.setSources(next)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkSourcesLocked(next, ""); err != nil {
		return nil, err
	}
	if err := m.save(s); err != nil {
		return nil, err
	}
	return s, nil
}

func (m *Manager) AlterSources(ctx context.Context, id string, urls []string, name *string, interval *uint32) (*Subscription, error) {
	cur, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if name != nil && strings.TrimSpace(*name) == "" {
		return nil, fmt.Errorf("name can't be empty: %w", ErrInvalid)
	}
	next, err := m.prepareSources(ctx, urls, cur.savedSources())
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	s, err := m.load(id)
	if err == nil && !slices.Equal(s.SourceURLs(), cur.SourceURLs()) {
		err = fmt.Errorf("subscription sources changed; reopen the editor: %w", ErrInvalid)
	}
	if err == nil {
		err = m.checkSourcesLocked(next, id)
	}
	if err == nil {
		for _, latest := range s.savedSources() {
			i := slices.IndexFunc(next, func(source Source) bool { return source.URL == latest.URL })
			if i >= 0 && !latest.CheckedAt.Before(next[i].CheckedAt) {
				next[i] = latest
			}
		}
		if name != nil {
			s.Name = strings.TrimSpace(*name)
		}
		if interval != nil {
			s.CustomIntervalMinutes = *interval
		}
		s.setSources(next)
		err = m.save(s)
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.configsChanged(ctx, s)
	return s, nil
}

func (m *Manager) checkSourcesLocked(sources []Source, exceptID string) error {
	for _, source := range sources {
		if err := m.checkUniqueURLLocked(source.URL, exceptID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) updateSources(ctx context.Context, cur *Subscription, scheduled bool) (*Subscription, error) {
	next := slices.Clone(cur.Sources)
	fetch := make([]bool, len(next))
	now := m.now()
	for i, source := range next {
		fetch[i] = !isTUICLink(source.URL) && (!scheduled || source.due(now, cur.interval(m.defaultInterval())))
	}
	if !slices.Contains(fetch, true) {
		return cur, nil
	}
	m.fetchSources(ctx, next, fetch)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	s, err := m.load(cur.ID)
	if err != nil || len(s.Sources) == 0 || !slices.Equal(s.SourceURLs(), cur.SourceURLs()) {
		m.mu.Unlock()
		return s, err
	}
	for i, source := range s.Sources {
		if !fetch[i] || !source.CheckedAt.Before(next[i].CheckedAt) {
			next[i] = source
		}
	}
	s.setSources(next)
	err = m.save(s)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.configsChanged(ctx, s)
	return s, nil
}

func (s *Subscription) setSources(sources []Source) {
	s.Sources, s.URL = sources, sources[0].URL
	previous := make(map[string]Config, len(s.Configs))
	for _, config := range s.Configs {
		previous[mergeKey(config)] = config
	}
	positions := make(map[string]int)
	configs := []Config{}
	var failed []string
	var latest, checked time.Time
	var primary fetched
	for i, source := range sources {
		if source.Failed {
			failed = append(failed, fmt.Sprintf("%d (%s)", i+1, hostOf(source.URL)))
		}
		if source.UpdatedAt.After(latest) {
			latest = source.UpdatedAt
		}
		if source.CheckedAt.After(checked) {
			checked = source.CheckedAt
		}
		// One account can be present in several formats. Report its quota once.
		if primary.usage == nil && source.Usage != nil {
			primary.usage = source.Usage
		}
		for _, config := range source.Configs {
			key := mergeKey(config)
			if index, found := positions[key]; found && config.Profile != nil &&
				configs[index].Profile != nil && configs[index].ID != config.ID {
				// The same first outbound can be part of different provider routing graphs.
				key += ":" + config.ID
			}
			if index, found := positions[key]; found {
				old, had := previous[key]
				if had && old.ID == config.ID || !had && configs[index].Profile == nil && config.Profile != nil {
					configs[index] = config
				}
				continue
			}
			positions[key] = len(configs)
			configs = append(configs, config)
		}
	}
	primary.interval = time.Duration(sources[0].ProviderMinutes) * time.Minute
	s.applyFetch(primary, configs, latest)
	s.CheckedAt = checked
	if len(failed) != 0 {
		s.LastError = "Could not refresh sources " + strings.Join(failed, ", ") + ". Previously imported servers remain available."
	}
}

func mergeKey(config Config) string {
	if wire, err := xray.OutboundIdentity(config.Outbound); err == nil {
		return fmt.Sprintf("%x", sha256.Sum256(wire))
	}
	return configID(config.Outbound)
}
