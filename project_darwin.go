package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func replaceProject(from, to string) error { return os.Rename(from, to) }

func createProject(from, to string) error { return unix.RenamexNp(from, to, unix.RENAME_EXCL) }

func lockProjectSave(path string) (*os.File, error) {
	f, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	// The directory inode remains stable across atomic project replacements.
	if err := lockRecovery(f); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errProjectConflict
		}
		return nil, err
	}
	return f, nil
}

func lockRecovery(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
