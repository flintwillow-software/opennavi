//go:build unix

package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// acquireLock takes an advisory exclusive lock on <root>/.lock, so a second
// process pointed at the same root fails loudly instead of silently
// interleaving writes. The kernel releases the lock when the process exits,
// so a crash never leaves a stale lock behind.
func (b *Backend) acquireLock() error {
	path := filepath.Join(b.root, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("fs: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return fmt.Errorf("fs: state root %s is already in use by another process (one writer per root): %w", b.root, err)
	}
	b.lock = f
	return nil
}

func (b *Backend) releaseLock() error {
	if b.lock == nil {
		return nil
	}
	defer b.lock.Close()
	return syscall.Flock(int(b.lock.Fd()), syscall.LOCK_UN)
}
