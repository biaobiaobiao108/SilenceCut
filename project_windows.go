package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func replaceProject(from, to string) error {
	return moveProject(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func createProject(from, to string) error {
	return moveProject(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func moveProject(from, to string, flags uint32) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(src, dst, flags)
}

func lockRecovery(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func lockProjectSave(path string) (*os.File, error) {
	// Windows cannot replace an open destination with MoveFileEx. Lock a
	// sibling that the OS removes automatically when its owner exits.
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Base(path))))
	lockPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".silencecut-save-%x", digest[:16]))
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE,
		0, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, errProjectConflict
	}
	if err != nil {
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), lockPath), nil
}
