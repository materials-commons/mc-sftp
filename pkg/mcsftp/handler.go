package mcsftp

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/materials-commons/mc-ssh/pkg/mc"
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
	dir          *mcmodel.File
	project      *mcmodel.Project
	stores       *MCStores
	fileHandle   *os.File
	ftpPath      string
	projectPath  string
	openForWrite bool
	hasher       hash.Hash
}

type MCHandler struct {
	user     *mcmodel.User
	stores   *MCStores
	mcfsRoot string

	// Protects files
	mu    sync.Mutex
	files map[string]*MCFile
}

func NewMCHandler(user *mcmodel.User, stores *MCStores, mcfsRoot string) *MCHandler {
	return &MCHandler{
		user:     user,
		stores:   stores,
		files:    make(map[string]*MCFile),
		mcfsRoot: mcfsRoot,
	}
}

func (h *MCHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}

	mcFile, err := h.mcfileSetup(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	//mcFile.file, err = h.stores.fileStore.

	// TODO: Need to get the file
	if mcFile.fileHandle, err = os.Open(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot)); err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[r.Filepath] = mcFile

	return mcFile, nil
}

func (h *MCHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if !flags.Write {
		return nil, os.ErrInvalid
	}

	mcFile, err := h.mcfileSetup(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	fileName := filepath.Base(mcFile.projectPath)
	mcFile.file, err = h.stores.fileStore.CreateFile(fileName, mcFile.project.ID, mcFile.dir.ID, h.user.ID, mc.GetMimeType(fileName))
	if err != nil {
		return nil, os.ErrNotExist
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

	if mcFile.fileHandle, err = os.OpenFile(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot), openFlags, 0777); err != nil {
		return nil, err
	}

	mcFile.openForWrite = true
	mcFile.hasher = md5.New()

	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[r.Filepath] = mcFile

	return mcFile, nil
}

// mcfileSetup will setup the MCFile that is used for reading/writing of files. It
// performs the actions of determining the project, setting up paths, and similar
// setup items needed to create a MCFile regardless of
func (h *MCHandler) mcfileSetup(r *sftp.Request) (*MCFile, error) {
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	path := mc.RemoveProjectSlugFromPath(r.Filepath, project.Name)

	dir, err := h.stores.fileStore.FindDirByPath(project.ID, filepath.Dir(path))
	if err != nil {
		return nil, os.ErrNotExist
	}

	return &MCFile{
		user:        h.user,
		project:     project,
		dir:         dir,
		ftpPath:     r.Filepath,
		projectPath: path,
		stores:      h.stores,
	}, nil
}

func (h *MCHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Mkdir":
		return nil
	case "Rename":
		return fmt.Errorf("unsupported command: 'Rename'")
	case "Rmdir":
		return fmt.Errorf("unsupported command: 'Rmdir'")
	case "Setstat":
		return fmt.Errorf("unsupported command: 'Setstat'")
	case "Link":
		return fmt.Errorf("unsupported command: 'Link'")
	case "Symlink":
		return fmt.Errorf("unsupported command: 'Symlink'")
	default:
		return fmt.Errorf("unsupport command: '%s'", r.Method)
	}
}

func (h *MCHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
	case "Stat":
	case "Readlink":
		return nil, fmt.Errorf("unsupported command: 'Readlink'")
	default:
		return nil, fmt.Errorf("unsupport command: '%s'", r.Method)
	}
	return nil, nil
}

func (h *MCHandler) getProject(r *sftp.Request) (*mcmodel.Project, error) {
	parts := strings.Split(r.Filepath, "/")
	// parts will be an array with the first element being an
	// empty string. For example if the path is /my-project/this/that,
	// then the array will be:
	// ["", "my-project", "this", "that"]
	// So the project slug is parts[1]
	projectSlug := parts[1]
	_ = projectSlug

	// hard code project for now until we add the slug to the database
	project, err := h.stores.projectStore.FindProject(1)
	if err != nil {
		return nil, err
	}

	if !h.stores.projectStore.UserCanAccessProject(h.user.ID, project.ID) {
		return nil, fmt.Errorf("no such project %s", projectSlug)
	}

	return project, err
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
