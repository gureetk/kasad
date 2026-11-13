// Package atomicfile replaces a file's contents in one step.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces the file at path with data in one step: it writes a
// temporary file beside it and renames that into place. If the program stops
// part-way through, the previous file is left as it was, not half-written.
//
// The file is readable and writable by its owner only.
func Write(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name()) // no effect once the rename has happened

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
