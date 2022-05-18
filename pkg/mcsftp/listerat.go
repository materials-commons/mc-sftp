package mcsftp

import (
	"io"
	"os"
)

type listerat []os.FileInfo

// ListAt verifies that the particular index exists in the files array.
func (f listerat) ListAt(files []os.FileInfo, offset int64) (int, error) {
	var n int
	if offset >= int64(len(f)) {
		return 0, io.EOF
	}
	n = copy(files, f[offset:])
	if n < len(files) {
		return n, io.EOF
	}

	return n, nil
}
