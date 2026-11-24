package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func replaceProject(from, to string) error { return os.Rename(from, to) }

func lockRecovery(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
