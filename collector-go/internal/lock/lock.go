// Package lock makes sure only one speed test runs on a device at a time: two at once would share the connection and each
// report a speed that is too low.
package lock

import (
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrBusy = errors.New("another speed test is already running on this device")

// Acquire takes the lock file. A lock older than stale is assumed to belong to a process that died, and is replaced.
// The returned function releases it.
func Acquire(path string, stale time.Duration) (release func(), err error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue // it vanished between the two calls: try again
		}
		if time.Since(info.ModTime()) <= stale {
			return nil, ErrBusy
		}
		_ = os.Remove(path) // stale: the owner is long gone
	}
	return nil, ErrBusy
}
