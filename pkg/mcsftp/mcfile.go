package mcsftp

import (
	"bytes"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/apex/log"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/mc-ssh/pkg/mc"
)

// MCFile represents a single SFTP file read or write request. It handles the ReadAt, WriteAt and Close
// interfaces for SFTP file handling.
type mcfile struct {
	// file is the underlying Materials Commons file that is being accessed.
	file *mcmodel.File

	// dir is the underlying Materials Commons directory that the file (above) is in.
	dir *mcmodel.File

	// project is the Materials Commons project that the file is in.
	project *mcmodel.Project

	// stores are the various stores to update
	stores *mc.Stores

	// The real underlying handle to read/write the file.
	fileHandle *os.File

	// openForWrite is true when the file was opened for write. This is used in MCFile.Close() to
	// determine if file statistics and checksum handling should be done.
	openForWrite bool

	// hasher tracks the checksum for files that were opened for write.
	hasher hash.Hash

	// mcfsRoot is the directory path where Materials Commons files are being read from/written to.
	mcfsRoot string
}

// WriteAt takes care of writing to the file and updating the hasher that is
// incrementally creating the checksum.
func (f *mcfile) WriteAt(b []byte, offset int64) (int, error) {
	var (
		n   int
		err error
	)

	if n, err = f.fileHandle.WriteAt(b, offset); err != nil {
		log.Errorf("Error writing to file %d: %s", f.file.ID, err)
		return n, err
	}

	if _, err = io.Copy(f.hasher, bytes.NewBuffer(b[:n])); err != nil {
		log.Errorf("Error updating the checksum for file %d: %s", f.file.ID, err)
	}

	return n, nil
}

// ReadAt reads from the underlying handle. It's just a pass through to the file handle
// ReadAt plus a bit of extra error logging.
func (f *mcfile) ReadAt(b []byte, offset int64) (int, error) {
	n, err := f.fileHandle.ReadAt(b, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		log.Errorf("Error reading from file %d: %s", f.file.ID, err)
	}

	return n, err
}

// isOpenForRead returns true if the file was opened for read. It exists for readability purposes.
func (f *mcfile) isOpenForRead() bool {
	return f.openForWrite == false
}

// Close handles updating the metadata on a file stored in Materials Commons as well as
// closing the underlying file handle. The metadata is only updated if the file was
// open for write. Close always returns nil, even if there was an error. Errors
// are logged as there is nothing that can be done about an error at this point.
func (f *mcfile) Close() error {
	deleteFile := false

	defer func() {
		if err := f.fileHandle.Close(); err != nil {
			log.Errorf("Error closing file %d: %s", f.file.ID, err)
		}

		if deleteFile {
			// A file matching this file's checksum already exists in the system so delete the file we just
			// uploaded. See the call to h.stores.FileStore.PointAtExistingIfExists towards the end of this method.
			_ = os.Remove(f.file.ToUnderlyingFilePath(f.mcfsRoot))
		}
	}()

	if f.isOpenForRead() {
		// If open for read then nothing to do.
		return nil
	}

	// If we are here then the file was open for write, so lets update the metadata
	// that Materials Commons is tracking.

	finfo, err := f.fileHandle.Stat()
	if err != nil {
		log.Errorf("Unable to update file %d metadata: %s", f.file.ID, err)
		return nil
	}

	checksum := fmt.Sprintf("%x", f.hasher.Sum(nil))

	// Note deleteFile. DoneWritingToFile will switch the file if there was an existing file that had the
	// same checksum. Here is where deleteFile gets set so that it can delete the file that was just written
	// if this switch occurred.
	if deleteFile, err = f.stores.FileStore.DoneWritingToFile(f.file, checksum, finfo.Size(), f.stores.ConversionStore); err != nil {
		log.Errorf("Failure updating file (%d) and project (%d) metadata: %s", f.file.ID, f.project.ID, err)
	}

	return nil
}
