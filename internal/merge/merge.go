// Package merge combines session lists from every provider into a single,
// deduplicated, sorted view for the picker.
package merge

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jkuhn12/agent-session-manager/internal/provider"
)

// untitled is shown for sessions whose agent store has no usable title.
const untitled = "Untitled session"

// result is one provider's outcome. skip marks a provider that is simply not
// installed; that is normal and must not become an error.
type result struct {
	name     string
	sessions []provider.Session
	err      error
	skip     bool
}

// query runs one provider, converting a panic into an error so a broken
// integration can never take down the picker.
func query(p provider.Provider, fallbackName string) (r result) {
	r.name = fallbackName
	defer func() {
		if rec := recover(); rec != nil {
			r.err = fmt.Errorf("panic: %v", rec)
		}
	}()

	r.name = p.Name()
	if r.name == "" {
		r.name = fallbackName
	}
	if !p.Available() {
		r.skip = true
		return r
	}
	r.sessions, r.err = p.Sessions()
	return r
}

// Sessions queries all providers concurrently, dedupes, flags missing
// directories, and returns sessions sorted by TimeUpdated descending.
//
// Provider failures never abort the picker: they are returned as
// "<provider>: <error>" while any sessions collected from that provider (and
// all other providers) are still included.
func Sessions(providers []provider.Provider) ([]provider.Session, []error) {
	sessions := make([]provider.Session, 0)
	errs := make([]error, 0)

	// Keep results indexed by provider so dedupe tie-breaking ("keep the
	// first") is deterministic regardless of goroutine scheduling.
	results := make([]result, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		if p == nil {
			continue
		}
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			results[i] = query(p, fmt.Sprintf("provider[%d]", i))
		}(i, p)
	}
	wg.Wait()

	type key struct {
		agent string
		id    string
	}
	index := make(map[key]int)
	for _, r := range results {
		if r.skip {
			continue
		}
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.name, r.err))
		}
		for _, s := range r.sessions {
			k := key{agent: s.Agent, id: s.ID}
			if i, ok := index[k]; ok {
				if newer(s, sessions[i]) {
					sessions[i] = s
				}
				continue
			}
			index[k] = len(sessions)
			sessions = append(sessions, s)
		}
	}

	for i := range sessions {
		if title := provider.CleanTitle(sessions[i].Title); title != "" {
			sessions[i].Title = title
		} else {
			sessions[i].Title = untitled
		}
		sessions[i].DirExists = provider.DirExists(sessions[i].Directory)
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if !a.TimeUpdated.Equal(b.TimeUpdated) {
			return a.TimeUpdated.After(b.TimeUpdated)
		}
		if !a.TimeCreated.Equal(b.TimeCreated) {
			return a.TimeCreated.After(b.TimeCreated)
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})

	return sessions, errs
}

// newer reports whether a should replace b for the same (agent, id) pair:
// later update wins, then later creation, otherwise the existing row stays.
func newer(a, b provider.Session) bool {
	if !a.TimeUpdated.Equal(b.TimeUpdated) {
		return a.TimeUpdated.After(b.TimeUpdated)
	}
	return a.TimeCreated.After(b.TimeCreated)
}
