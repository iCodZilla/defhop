package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	indexLockName  = "index.lock"
	indexLockStale = 2 * time.Minute
	indexLockPoll  = 100 * time.Millisecond
)

// AcquireIndexLock exclusively owns cacheDir for full index / wipe.
// Uses atomic create of index.lock; steals locks whose holder PID is dead
// or whose mtime is older than 2 minutes.
// release removes the lock file (safe to call multiple times).
func AcquireIndexLock(cacheDir string, wait time.Duration) (release func(), err error) {
	if cacheDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(cacheDir, indexLockName)
	deadline := time.Now().Add(wait)
	if wait <= 0 {
		deadline = time.Now().Add(15 * time.Second)
	}
	loggedWait := false
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().Unix())
			_ = f.Close()
			var released bool
			return func() {
				if released {
					return
				}
				released = true
				_ = os.Remove(lockPath)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if stealStaleLock(lockPath) {
			continue
		}
		if time.Now().After(deadline) {
			holder := readLockHolder(lockPath)
			return nil, fmt.Errorf("defhop cache busy (%s); another window may be indexing", holder)
		}
		if !loggedWait {
			loggedWait = true
			fmt.Fprintf(os.Stderr, "[progress] waiting for cache lock (%s)\n", readLockHolder(lockPath))
		}
		time.Sleep(indexLockPoll)
	}
}

func stealStaleLock(lockPath string) bool {
	// Dead holder: steal immediately (crash / kill mid-INIT left the file behind).
	if pid := lockPID(lockPath); pid > 0 && pid != os.Getpid() && !processAlive(pid) {
		_ = os.Remove(lockPath)
		return true
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) < indexLockStale {
		return false
	}
	_ = os.Remove(lockPath)
	return true
}

func lockPID(lockPath string) int {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0
	}
	parts := strings.SplitN(string(data), "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	return pid
}

func readLockHolder(lockPath string) string {
	pid := lockPID(lockPath)
	if pid <= 0 {
		return lockPath
	}
	return fmt.Sprintf("pid=%d", pid)
}
