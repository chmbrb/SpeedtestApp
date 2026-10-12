package lock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOnlyOneHolderAtATime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	release, err := Acquire(p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p, time.Hour); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second test while one runs: %v", err)
	}
	release()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("release removes the lock file")
	}
	release2, err := Acquire(p, time.Hour)
	if err != nil {
		t.Fatalf("free again after release: %v", err)
	}
	release2()
}

func TestAStaleLockFromADeadProcessIsReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	_ = os.WriteFile(p, []byte("99999 long ago\n"), 0o600)
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(p, old, old)
	release, err := Acquire(p, time.Hour)
	if err != nil {
		t.Fatalf("a lock older than the limit belongs to a dead process: %v", err)
	}
	defer release()
	// a fresh one is respected
	q := filepath.Join(t.TempDir(), "fresh.lock")
	_ = os.WriteFile(q, []byte("1 now\n"), 0o600)
	if _, err := Acquire(q, time.Hour); !errors.Is(err, ErrBusy) {
		t.Errorf("a recent lock is honoured: %v", err)
	}
}

func TestRacingCallersGetExactlyOneLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.lock")
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Acquire(p, time.Hour); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d callers got the lock", won)
	}
}

func TestAnUnwritableDirectoryIsAnErrorNotBusy(t *testing.T) {
	_, err := Acquire(filepath.Join(t.TempDir(), "missing-dir", "x.lock"), time.Hour)
	if err == nil || errors.Is(err, ErrBusy) {
		t.Errorf("%v", err)
	}
}
