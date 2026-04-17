//go:build windows

package main

import (
	"os"
	"time"
)

// tryTruncateLog on Windows opens the file exclusively via O_CREATE|O_EXCL
// on a sibling lock file. If another process already holds it, we skip
// truncation — same best-effort semantics as the Unix flock path.
func tryTruncateLog(path string) {
	lockPath := path + ".trunc.lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer func() {
		lock.Close()
		os.Remove(lockPath)
	}()

	f, err := os.OpenFile(path, os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	now := time.Now()
	modTime := info.ModTime()
	if modTime.Year() != now.Year() || modTime.YearDay() != now.YearDay() {
		f.Truncate(0)
	}
}
