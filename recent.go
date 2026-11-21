package main

// Recent: the "other ends" this computer has tested (or been tested by),
// kept in a small file so users can pick them again next time.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxRecent = 10

type Recent struct {
	Target  string `json:"target"`  // what to test: typed name, IP or ip:port
	Host    string `json:"host"`    // computer name it reported
	IP      string `json:"ip"`      // address used last time (for the online check)
	Verdict string `json:"verdict"` // good / fair / poor / "" (tested us)
	When    string `json:"when"`
	Online  bool   `json:"online"`
}

type recentStore struct {
	mu   sync.Mutex
	list []Recent
	path string
}

func recentPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Radio Bandwidth", "recent.json")
}

func (s *recentStore) load() {
	s.path = recentPath()
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var l []Recent
	if json.Unmarshal(b, &l) == nil {
		if len(l) > maxRecent {
			l = l[:maxRecent]
		}
		for i := range l {
			l[i].Online = false // unknown until checked
		}
		s.list = l
	}
}

func (s *recentStore) saveLocked() {
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	b, _ := json.MarshalIndent(s.list, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// add puts an entry at the top, replacing an older one for the same
// computer (same address or same typed target).
func (s *recentStore) add(r Recent) {
	if r.Target == "" {
		return
	}
	r.When = time.Now().Format("2006-01-02 15:04")
	r.Online = true
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Recent{r}
	for _, o := range s.list {
		if strings.EqualFold(o.Target, r.Target) || (o.IP != "" && o.IP == r.IP) {
			if r.Verdict == "" { // being tested by them: keep our last verdict
				out[0].Verdict = o.Verdict
				if o.Target != "" {
					out[0].Target = o.Target
				}
			}
			continue
		}
		out = append(out, o)
	}
	if len(out) > maxRecent {
		out = out[:maxRecent]
	}
	s.list = out
	s.saveLocked()
}

func (s *recentStore) remove(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Recent
	for _, o := range s.list {
		if !strings.EqualFold(o.Target, target) {
			out = append(out, o)
		}
	}
	s.list = out
	s.saveLocked()
}

func (s *recentStore) snapshot() []Recent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recent(nil), s.list...)
}

// recentLoop checks every few seconds which remembered computers are
// running Radio Bandwidth right now.
func (a *App) recentLoop() {
	for {
		for _, r := range a.recent.snapshot() {
			host, port := a.splitTarget(r.Target) // keeps a non-standard port
			if r.IP != "" {
				host = r.IP // no name lookup every 10 seconds
			}
			h, err := a.hello(host, port, 1200*time.Millisecond)
			online := err == nil && h.ID != a.id
			a.recent.mu.Lock()
			for i := range a.recent.list {
				if a.recent.list[i].Target == r.Target {
					a.recent.list[i].Online = online
				}
			}
			a.recent.mu.Unlock()
		}
		time.Sleep(10 * time.Second)
	}
}
