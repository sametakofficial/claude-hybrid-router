//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// tryTruncateLog truncates the log file while holding an exclusive flock,
// preventing races between concurrent instances that also rotate on startup.
func tryTruncateLog(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
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
