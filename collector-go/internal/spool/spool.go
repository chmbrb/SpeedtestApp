// Package spool keeps results on disk while the API cannot be reached, so a laptop that was offline loses nothing.
package spool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultMaxItems = 500
	DefaultMaxAge   = 14 * 24 * time.Hour
	maxRejected     = 50
)

// Item is one waiting result.
type Item struct {
	Name     string
	QueuedAt time.Time
	Payload  []byte
}

type Spool struct {
	dir      string
	maxItems int
	maxAge   time.Duration
	now      func() time.Time
}

// Open creates the spool directory (private to the user) if needed.
func Open(dir string, maxItems int, maxAge time.Duration) (*Spool, error) {
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	if err := os.MkdirAll(filepath.Join(dir, "rejected"), 0o700); err != nil {
		return nil, fmt.Errorf("cannot create the spool directory %s: %w", dir, err)
	}
	return &Spool{dir: dir, maxItems: maxItems, maxAge: maxAge, now: time.Now}, nil
}

func (s *Spool) Dir() string { return s.dir }

// names are "<unix nanoseconds, zero padded>-<random>.json": sorting them sorts by time.
func newName(t time.Time) string {
	var r [3]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf("%020d-%s.json", t.UnixNano(), hex.EncodeToString(r[:]))
}

func parseName(name string) (time.Time, bool) {
	if !strings.HasSuffix(name, ".json") || len(name) < 22 || name[20] != '-' {
		return time.Time{}, false
	}
	var ns int64
	if _, err := fmt.Sscanf(name[:20], "%020d", &ns); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// Add stores a payload and returns its name, plus how many of the oldest results had to be dropped to stay within the limit.
// The file appears atomically: a crash never leaves a half-written result for the next run to choke on.
func (s *Spool) Add(payload []byte) (name string, dropped int, err error) {
	name = newName(s.now())
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		return "", 0, err
	}
	items, _, err := s.List()
	if err != nil {
		return name, 0, err
	}
	for len(items) > s.maxItems {
		if err := os.Remove(filepath.Join(s.dir, items[0].Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return name, dropped, err
		}
		items, dropped = items[1:], dropped+1
	}
	return name, dropped, nil
}

// List returns the waiting results, oldest first. Results older than the maximum age are deleted and counted in expired.
func (s *Spool) List() (items []Item, expired int, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		at, ok := parseName(e.Name())
		if !ok {
			continue // a leftover temp file or something that is not ours
		}
		if s.now().Sub(at) > s.maxAge {
			if os.Remove(filepath.Join(s.dir, e.Name())) == nil {
				expired++
			}
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		items = append(items, Item{Name: e.Name(), QueuedAt: at, Payload: b})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, expired, nil
}

// Remove deletes a result that has been sent.
func (s *Spool) Remove(name string) error {
	if err := os.Remove(filepath.Join(s.dir, filepath.Base(name))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Reject moves a result the server refused as invalid out of the queue, keeping it for inspection (the newest few only).
func (s *Spool) Reject(name, reason string) error {
	name = filepath.Base(name)
	dst := filepath.Join(s.dir, "rejected", name)
	if err := os.Rename(filepath.Join(s.dir, name), dst); err != nil {
		return err
	}
	_ = os.WriteFile(dst+".reason.txt", []byte(reason+"\n"), 0o600)
	entries, _ := os.ReadDir(filepath.Join(s.dir, "rejected"))
	var jsons []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsons = append(jsons, e.Name())
		}
	}
	sort.Strings(jsons)
	for len(jsons) > maxRejected {
		_ = os.Remove(filepath.Join(s.dir, "rejected", jsons[0]))
		_ = os.Remove(filepath.Join(s.dir, "rejected", jsons[0]+".reason.txt"))
		jsons = jsons[1:]
	}
	return nil
}

// Len is the number of waiting results.
func (s *Spool) Len() int {
	items, _, _ := s.List()
	return len(items)
}
