package media

import (
	"os"
	"syscall"
)

func publish(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "move", Old: src, New: dst, Err: err}
	}
	return nil
}
