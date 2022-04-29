package mcsftp

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/pkg/sftp"
)

type MCStores struct {
	fileStore       *store.FileStore
	projectStore    *store.ProjectStore
	conversionStore *store.ConversionStore
}

type MCFile struct {
	user         *mcmodel.User
	file         *mcmodel.File
	project      *mcmodel.Project
	stores       *MCStores
	fileHandle   *os.File
	path         string
	openForWrite bool
	hasher       hash.Hash
}

type MCHandler struct {
	user   *mcmodel.User
	stores *MCStores

	// Protects files
	mu    sync.Mutex
	files map[string]*MCFile
}

func NewMCHandler(user *mcmodel.User, stores *MCStores) *MCHandler {
	return &MCHandler{
		user:   user,
		stores: stores,
		files:  make(map[string]*MCFile),
	}
}

func (h *MCHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}
	return nil, nil
}

func (h *MCHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if !flags.Write {
		return nil, os.ErrInvalid
	}

	openFlags := os.O_RDWR
	if flags.Trunc {
		openFlags &= os.O_TRUNC
	}

	if flags.Creat {
		openFlags &= os.O_CREATE
	}

	if flags.Append {
		openFlags &= os.O_APPEND
	}

	mcFile := &MCFile{
		user:   h.user,
		stores: h.stores,
	}
	var err error
	mcFile.fileHandle, err = os.OpenFile("stuff", openFlags, 0777)
	mcFile.hasher = md5.New()
	if err != nil {
		return nil, err
	}

	// somehow return WriterAt
	return mcFile, nil
}

func (h *MCHandler) Filecmd(r *sftp.Request) error {
	return nil
}

func (h *MCHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	return nil, nil
}

// Close handles updating the metadata on a file stored in Materials Commons as well as
// closing the underlying file handle. The metadata is only updated if the file was
// open for write. Close always returns nil, even if there was an error. Errors
// are logged as there is nothing that can be done about an error at this point.
func (f *MCFile) Close() error {
	defer func() {
		if err := f.fileHandle.Close(); err != nil {
			log.Errorf("Error closing file %d: %s", f.file.ID, err)
		}
	}()

	if f.isOpenForRead() {
		// If open for read then nothing to do.
		return nil
	}

	// If we are here then the file was open for write, so lets update the metadata
	// that Materials Commons is tracker.

	finfo, err := f.fileHandle.Stat()
	if err != nil {
		log.Errorf("Unable to update file %d metadata: %s", f.file.ID, err)
		return nil
	}

	checksum := fmt.Sprintf("%x", f.hasher.Sum(nil))
	if err := f.stores.fileStore.UpdateMetadataForFileAndProject(f.file, checksum, f.project.ID, finfo.Size()); err != nil {
		log.Errorf("Failure updating file (%d) and project (%d) metadata: %s", f.file.ID, f.project.ID, err)
	}

	return nil
}

// WriteAt takes care of writing to the file and updating the hasher that is
// incrementally creating the checksum.
func (f *MCFile) WriteAt(b []byte, offset int64) (int, error) {
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
func (f *MCFile) ReadAt(b []byte, offset int64) (int, error) {
	n, err := f.fileHandle.ReadAt(b, offset)
	if err != nil {
		log.Errorf("Error reading from file %d: %s", f.file.ID, err)
	}

	return n, err
}

func (f *MCFile) isOpenForRead() bool {
	return f.openForWrite == false
}

// FileInfo interface

func (f *MCFile) size() int64 {
	return f.file.ToFileInfo().Size()
}

func (f *MCFile) Name() string {
	return f.file.ToFileInfo().Name()
}

func (f *MCFile) Mode() os.FileMode {
	return f.file.ToFileInfo().Mode()
}

func (f *MCFile) ModTime() time.Time {
	return f.file.ToFileInfo().ModTime()
}

func (f *MCFile) IsDir() bool {
	return f.file.ToFileInfo().IsDir()
}

func (f *MCFile) Sys() interface{} {
	return f.file.ToFileInfo().Sys()
}
