package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireIndexLockExclusive(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireIndexLock(dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = AcquireIndexLock(dir, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected second lock to fail while held")
	}
	release()

	release2, err := AcquireIndexLock(dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release2()
	if _, err := os.Stat(filepath.Join(dir, indexLockName)); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed: %v", err)
	}
}

func TestAcquireIndexLockStealsStale(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, indexLockName)
	if err := os.WriteFile(lockPath, []byte("1\n0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-indexLockStale - time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireIndexLock(dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAcquireIndexLockStealsDeadPID(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, indexLockName)
	// PID 1 may or may not exist; use a very high unused pid.
	if err := os.WriteFile(lockPath, []byte("2147483000\n0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireIndexLock(dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
