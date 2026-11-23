package media

import (
	"os"

	"golang.org/x/sys/unix"
)

func publish(src, dst string) error {
	if err := unix.RenamexNp(src, dst, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "move", Old: src, New: dst, Err: err}
	}
	return nil
}
