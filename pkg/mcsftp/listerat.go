package mcsftp

import (
	"io"
	"os"
)

// listerat is an array of objects that implement the os.FileInfo interface
type listerat []os.FileInfo

// ListAt verifies that the particular index exists in the files array.
func (f listerat) ListAt(files []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(f)) {
		// offset is greater than the number of entries in f, signal this by return error io.EOF
		return 0, io.EOF
	}

	n := copy(files, f[offset:])
	if n < len(files) {
		// The number copied is all the remaining entries in f, so nothing left
		// to copy after this. So return io.EOF to signal this.
		return n, io.EOF
	}

	// Copied entries, but some left, so don't return EOF.
	return n, nil
}
