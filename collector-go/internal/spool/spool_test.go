package spool

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T, max int, age time.Duration) (*Spool, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "q"), max, age)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { clock = clock.Add(time.Second); return clock } // strictly increasing, deterministic
	return s, &clock
}

func TestAddListRemoveKeepsOrderAndContent(t *testing.T) {
	s, _ := open(t, 0, 0)
	var names []string
	for i := 0; i < 5; i++ {
		n, dropped, err := s.Add([]byte(fmt.Sprintf(`{"n":%d}`, i)))
		if err != nil || dropped != 0 {
			t.Fatal(n, dropped, err)
		}
		names = append(names, n)
	}
	items, expired, err := s.List()
	if err != nil || expired != 0 || len(items) != 5 {
		t.Fatalf("%d %d %v", len(items), expired, err)
	}
	for i, it := range items {
		if it.Name != names[i] || string(it.Payload) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Errorf("item %d out of order or altered: %+v", i, it)
		}
	}
	if err := s.Remove(items[0].Name); err != nil || s.Len() != 4 {
		t.Errorf("remove: %v len=%d", err, s.Len())
	}
	if err := s.Remove(items[0].Name); err != nil {
		t.Errorf("removing twice is harmless: %v", err)
	}
	if err := s.Remove("../../etc/passwd"); err != nil {
		t.Errorf("path tricks are neutralised: %v", err)
	}
}

func TestFilesAreWrittenAtomicallyAndPrivately(t *testing.T) {
	s, _ := open(t, 0, 0)
	name, _, _ := s.Add([]byte(`{"a":1}`))
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(s.Dir(), name))
		di, _ := os.Stat(s.Dir())
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("results are private to the user: file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	// a crash mid-write leaves a temp file; it must be ignored, not mistaken for a result
	_ = os.WriteFile(filepath.Join(s.Dir(), ".tmp-12345"), []byte(`{"half`), 0o600)
	_ = os.WriteFile(filepath.Join(s.Dir(), "notes.txt"), []byte("hi"), 0o600)
	_ = os.WriteFile(filepath.Join(s.Dir(), "garbage.json"), []byte("{}"), 0o600)
	items, _, _ := s.List()
	if len(items) != 1 || items[0].Name != name {
		t.Errorf("only real results are listed: %v", items)
	}
	entries, _ := os.ReadDir(s.Dir())
	temps := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") && e.Name() != ".tmp-12345" {
			temps++
		}
	}
	if temps != 0 {
		t.Errorf("Add leaves no temp files behind: %d", temps)
	}
}

func TestWhenFullTheOldestAreDropped(t *testing.T) {
	s, _ := open(t, 3, 0)
	for i := 0; i < 3; i++ {
		s.Add([]byte(fmt.Sprintf(`{"n":%d}`, i))) //nolint:errcheck
	}
	_, dropped, err := s.Add([]byte(`{"n":3}`))
	if err != nil || dropped != 1 {
		t.Fatalf("dropped=%d %v", dropped, err)
	}
	_, dropped, _ = s.Add([]byte(`{"n":4}`))
	if dropped != 1 {
		t.Errorf("%d", dropped)
	}
	items, _, _ := s.List()
	if len(items) != 3 || string(items[0].Payload) != `{"n":2}` || string(items[2].Payload) != `{"n":4}` {
		t.Errorf("the newest three remain: %v", items)
	}
}

func TestOldResultsExpire(t *testing.T) {
	s, clock := open(t, 0, 24*time.Hour)
	s.Add([]byte(`{"old":true}`)) //nolint:errcheck
	*clock = clock.Add(23 * time.Hour)
	s.Add([]byte(`{"mid":true}`))     //nolint:errcheck
	*clock = clock.Add(2 * time.Hour) // the first is now 25h old, the second 2h
	items, expired, _ := s.List()
	if expired != 1 || len(items) != 1 || string(items[0].Payload) != `{"mid":true}` {
		t.Errorf("expired=%d items=%v", expired, items)
	}
	if _, expired, _ := s.List(); expired != 0 {
		t.Error("an expired result is gone for good, not counted twice")
	}
}

func TestRejectMovesAsideWithTheReasonAndKeepsOnlyTheNewest(t *testing.T) {
	s, _ := open(t, 0, 0)
	n, _, _ := s.Add([]byte(`{"bad":1}`))
	if err := s.Reject(n, "refused (HTTP 400): result.download.bandwidth is required"); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Error("a rejected result leaves the queue")
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(), "rejected", n+".reason.txt"))
	if err != nil || !strings.Contains(string(b), "bandwidth is required") {
		t.Errorf("the reason is kept next to the result: %v %q", err, b)
	}
	for i := 0; i < maxRejected+5; i++ {
		n, _, _ := s.Add([]byte(`{}`))
		_ = s.Reject(n, "x")
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir(), "rejected"))
	jsons := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsons++
		}
	}
	if jsons != maxRejected {
		t.Errorf("only the newest %d rejected results are kept, got %d", maxRejected, jsons)
	}
}

func TestConcurrentAddsNeverCollide(t *testing.T) {
	s, _ := open(t, 1000, 0)
	s.now = time.Now // real, possibly identical, timestamps: the random suffix must keep names unique
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := s.Add([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if s.Len() != 50 {
		t.Errorf("lost results under concurrency: %d", s.Len())
	}
}

func TestOpenCreatesTheDirectoryAndExplainsFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if _, err := Open(filepath.Join(file, "sub"), 0, 0); err == nil || !strings.Contains(err.Error(), "cannot create the spool directory") {
		t.Errorf("%v", err)
	}
}
