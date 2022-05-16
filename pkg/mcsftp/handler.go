package mcsftp

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/apex/log"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/mc-ssh/pkg/mc"
	"github.com/pkg/sftp"
)

type MCFile struct {
	user *mcmodel.User
	file *mcmodel.File
	mcmodel.FileInfo
	dir          *mcmodel.File
	project      *mcmodel.Project
	stores       *mc.Stores
	fileHandle   *os.File
	ftpPath      string
	projectPath  string
	openForWrite bool
	hasher       hash.Hash
	mcfsRoot     string
}

type mcfsHandler struct {
	user     *mcmodel.User
	stores   *mc.Stores
	mcfsRoot string

	// Protects files, projects, and projectsWithoutAccess
	mu sync.Mutex

	// Tracks all the files the user has opened. The key is the path with the project slug.
	files map[string]*MCFile

	// Tracks all the projects the user has accessed that they also have rights to.
	// The key is the project slug.
	projects map[string]*mcmodel.Project

	// Tracks all the project the user has accessed that they *DO NOT* have rights to.
	// The key is the project slug.
	projectsWithoutAccess map[string]bool
}

func NewMCFSHandler(user *mcmodel.User, stores *mc.Stores, mcfsRoot string) *mcfsHandler {
	return &mcfsHandler{
		user:                  user,
		stores:                stores,
		files:                 make(map[string]*MCFile),
		projects:              make(map[string]*mcmodel.Project),
		projectsWithoutAccess: make(map[string]bool),
		mcfsRoot:              mcfsRoot,
	}
}

func (h *mcfsHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}

	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	if mcFile.file, err = h.stores.FileStore.GetFileByPath(mcFile.project.ID, getPathFromRequest(r)); err != nil {
		return nil, os.ErrNotExist
	}

	if mcFile.fileHandle, err = os.Open(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot)); err != nil {
		return nil, os.ErrNotExist
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[r.Filepath] = mcFile

	return mcFile, nil
}

func getPathFromRequest(r *sftp.Request) string {
	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)
	return mc.RemoveProjectSlugFromPath(r.Filepath, projectSlug)
}

func (h *mcfsHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if !flags.Write {
		return nil, os.ErrInvalid
	}

	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	fileName := filepath.Base(mcFile.projectPath)
	mcFile.file, err = h.stores.FileStore.CreateFile(fileName, mcFile.project.ID, mcFile.dir.ID, h.user.ID, mc.GetMimeType(fileName))
	if err != nil {
		return nil, os.ErrNotExist
	}

	mcFile.FileInfo = mcFile.file.ToFileInfo()

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
func (h *mcfsHandler) createMCFileFromRequest(r *sftp.Request) (*MCFile, error) {
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	path := mc.RemoveProjectSlugFromPath(r.Filepath, project.Name)

	dir, err := h.stores.FileStore.GetDirByPath(project.ID, filepath.Dir(path))
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

func (h *mcfsHandler) Filecmd(r *sftp.Request) error {
	project, err := h.getProject(r)
	if err != nil {
		return err
	}

	switch r.Method {
	case "Mkdir":
		path := mc.RemoveProjectSlugFromPath(r.Filepath, getPathFromRequest(r))
		_, err := h.stores.FileStore.GetOrCreateDirPath(project.ID, h.user.ID, path)
		return err
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

func (h *mcfsHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	path := mc.RemoveProjectSlugFromPath(r.Filepath, getPathFromRequest(r))
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	switch r.Method {
	case "List":
		files, err := h.stores.FileStore.ListDirectoryByPath(project.ID, path)
		if err != nil {
			return nil, os.ErrNotExist
		}
		var fileList []os.FileInfo
		for _, f := range files {
			fileList = append(fileList, f.ToFileInfo())
		}

		return listerat(fileList), nil
	case "Stat":
		file, err := h.stores.FileStore.GetFileByPath(project.ID, path)
		if err != nil {
			return nil, os.ErrNotExist
		}
		return listerat{file.ToFileInfo()}, nil
	case "Readlink":
		return nil, fmt.Errorf("unsupported command: 'Readlink'")
	default:
		return nil, fmt.Errorf("unsupport command: '%s'", r.Method)
	}
	return nil, nil
}

func (h *mcfsHandler) Realpath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if !filepath.IsAbs(p) {
		return filepath.Join("/", p)
	}

	return p
}

func (h *mcfsHandler) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	path := mc.RemoveProjectSlugFromPath(r.Filepath, getPathFromRequest(r))
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}
	file, err := h.stores.FileStore.GetFileByPath(project.ID, path)
	if err != nil {
		return nil, os.ErrNotExist
	}
	return listerat{file.ToFileInfo()}, nil
}

type listerat []os.FileInfo

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

func (h *mcfsHandler) getProject(r *sftp.Request) (*mcmodel.Project, error) {
	var (
		ok      bool
		project *mcmodel.Project
		err     error
	)

	h.mu.Lock()
	defer h.mu.Unlock()

	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)
	if project, ok = h.projects[projectSlug]; ok {
		return project, nil
	}

	if _, ok := h.projectsWithoutAccess[projectSlug]; ok {
		return nil, fmt.Errorf("no such project: %s", projectSlug)
	}

	if project, err = h.stores.ProjectStore.GetProjectBySlug(projectSlug); err != nil {
		h.projectsWithoutAccess[projectSlug] = true
		return nil, err
	}

	if !h.stores.ProjectStore.UserCanAccessProject(h.user.ID, project.ID) {
		h.projectsWithoutAccess[projectSlug] = true
		return nil, fmt.Errorf("no such project: %s", projectSlug)
	}

	h.projects[projectSlug] = project

	return project, err
}

// Close handles updating the metadata on a file stored in Materials Commons as well as
// closing the underlying file handle. The metadata is only updated if the file was
// open for write. Close always returns nil, even if there was an error. Errors
// are logged as there is nothing that can be done about an error at this point.
func (f *MCFile) Close() error {
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
	// that Materials Commons is tracker.

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
