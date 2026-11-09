//go:build unix

package atomicfile

import (
	"errors"
	"os"
	"syscall"
)

// syncDir fsyncs the directory dir so that a rename inside it is durable.
// File systems that cannot fsync a directory report EINVAL; that is not an
// error here, as there is nothing more that can be done.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		err = nil
	}
	return errors.Join(err, d.Close())
}
