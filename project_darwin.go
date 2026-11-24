package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func replaceProject(from, to string) error { return os.Rename(from, to) }

func createProject(from, to string) error { return unix.RenamexNp(from, to, unix.RENAME_EXCL) }

func lockProjectSave(path string) (*os.File, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errProjectConflict
	}
	if err != nil {
		return nil, err
	}
	if err := lockRecovery(f); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errProjectConflict
		}
		return nil, err
	}
	// A save can replace the inode between open and flock. Verify that the
	// lock still protects the file at path before checking its revision.
	locked, err := f.Stat()
	if err == nil {
		var current os.FileInfo
		current, err = os.Stat(path)
		if errors.Is(err, os.ErrNotExist) || err == nil && !os.SameFile(locked, current) {
			err = errProjectConflict
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func lockRecovery(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
