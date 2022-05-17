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

// MCFile represents a single SFTP file read or write request. It handles the ReadAt, WriteAt and Close
// interfaces for SFTP file handling.
type MCFile struct {
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

// mcfsHandler represents an SFTP connection. A connection is associated with a single user. That user
// may access multiple projects. Projects are accessed by including them in the path. Each project
// in Materials Commons has a unique project slug, which is a derived from the project name. This slug
// is included in the path when accessing files. For example, given a project with a slug 'my-project',
// then you would access a file by prepending my-project to the path, such as /my-project/dir1/file.txt.
// In this case the user is access file.txt in dir1 in project with the project slug my-project. The
// path to the file.txt file would be /dir1/file.txt in the project with slug my-project. A few things
// to note:
//
// 1. All the SFTP callbacks have to handle manipulating the path get the project slug, and to remove
//    it from the path so specify the underlying Materials Commons path.
//
// 2. Because a user can access many files, which could be in different projects, we don't want to
//    continuously look up projects. The mcfsHandler caches projects that were already looked up
//    so they can be quickly returned these are cached by project slug. It also caches failed projects
//    either because the project-slug didn't exist or the user didn't have access to the project.
//
type mcfsHandler struct {
	// user is the Materials Commons user for this SFTP session.
	user   *mcmodel.User
	stores *mc.Stores

	// mcfsRoot is the directory path where Materials Commons files are being read from/written to.
	mcfsRoot string

	// Protects files, projects, and projectsWithoutAccess
	mu sync.Mutex

	// Tracks all the projects the user has accessed that they also have rights to.
	// The key is the project slug.
	projects map[string]*mcmodel.Project

	// Tracks all the project the user has accessed that they *DO NOT* have rights to.
	// The key is the project slug.
	projectsWithoutAccess map[string]bool
}

func NewMCFSHandler(user *mcmodel.User, stores *mc.Stores, mcfsRoot string) sftp.Handlers {
	h := &mcfsHandler{
		user:                  user,
		stores:                stores,
		projects:              make(map[string]*mcmodel.Project),
		projectsWithoutAccess: make(map[string]bool),
		mcfsRoot:              mcfsRoot,
	}

	return sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	}
}

// Fileread sets up read access to an existing Materials Commons file.
func (h *mcfsHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	fmt.Printf("Fileread: %+v\n", r)
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}

	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		fmt.Println("  1")
		return nil, os.ErrNotExist
	}

	if mcFile.file, err = h.stores.FileStore.GetFileByPath(mcFile.project.ID, getPathFromRequest(r)); err != nil {
		fmt.Println("   2")
		return nil, os.ErrNotExist
	}

	fmt.Println("path = ", mcFile.file.ToUnderlyingFilePath(h.mcfsRoot))
	fmt.Printf("   for file = %+v\n", mcFile.file)
	if mcFile.fileHandle, err = os.Open(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot)); err != nil {
		fmt.Println("   3")
		return nil, os.ErrNotExist
	}

	return mcFile, nil
}

// Filewrite sets up a file for writing. It create a file or new file version in Materials Commons
// as well as the underlying real physical file to write to.
func (h *mcfsHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	fmt.Printf("Filewrite: %+v\n", r)
	flags := r.Pflags()
	if !flags.Write {
		// Pathological case, Filewrite should always have the flags.Write set to true.
		return nil, os.ErrInvalid
	}

	// Set up the initial SFTP request file state.
	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	// Create the Materials Commons file. This handles version creation.
	fileName := filepath.Base(r.Filepath)
	mcFile.file, err = h.stores.FileStore.CreateFile(fileName, mcFile.project.ID, mcFile.dir.ID, h.user.ID, mc.GetMimeType(fileName))
	if err != nil {
		return nil, os.ErrNotExist
	}

	// The flags don't matter, we will always open the file for create. Because files are versioned
	// in Materials Commons there is no appending, truncating or overwriting of files.
	openFlags := os.O_RDWR & os.O_CREATE

	if mcFile.fileHandle, err = os.OpenFile(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot), openFlags, 0777); err != nil {
		return nil, err
	}

	// Since this file was opened for writing we need to track its checksum, and for MCFile.Close() let
	// it know whether it needs to update statistics about the file (only when openForWrite is true).
	mcFile.openForWrite = true
	mcFile.hasher = md5.New()

	return mcFile, nil
}

// createMCFileFromRequest will create a new MCFile that is used for reading/writing of files. It
// performs the actions of determining the project, setting up paths, and similar
// setup items needed to create a MCFile.
func (h *mcfsHandler) createMCFileFromRequest(r *sftp.Request) (*MCFile, error) {
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	path := getPathFromRequest(r)

	dir, err := h.stores.FileStore.GetDirByPath(project.ID, filepath.Dir(path))
	if err != nil {
		return nil, os.ErrNotExist
	}

	return &MCFile{
		project: project,
		dir:     dir,
		stores:  h.stores,
	}, nil
}

// Filecmd supports various SFTP commands that manipulate a file and/or filesystem. It only supports
// Mkdir for directory creation. Deletes, renames, setting permissions, etc... are not supported.
func (h *mcfsHandler) Filecmd(r *sftp.Request) error {
	fmt.Printf("Filecmd: %+v\n", r)
	project, err := h.getProject(r)
	if err != nil {
		return err
	}

	path := getPathFromRequest(r)

	switch r.Method {
	case "Mkdir":
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

// Filelist handles the different SFTP file list type commands. We only support List (directory listing)
// and Stat. Things like Readlink don't make sense for Materials Commons.
func (h *mcfsHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	fmt.Printf("\nFilelist: %+v\n", r)
	path := getPathFromRequest(r)
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
		fi := file.ToFileInfo()
		s := listerat{&fi}
		return s, nil
	case "Readlink":
		return nil, fmt.Errorf("unsupported command: 'Readlink'")
	default:
		return nil, fmt.Errorf("unsupport command: '%s'", r.Method)
	}
}

// Realpath always returns the absolute path including the project slug.
func (h *mcfsHandler) Realpath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if !filepath.IsAbs(p) {
		return filepath.Join("/", p)
	}

	return p
}

// Lstat returns a single entry array containing the requested file, assuming it exists. It
// returns os.ErrNotExist if it doesn't exist.
func (h *mcfsHandler) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	fmt.Printf("Lstat +%v\n", r)
	path := getPathFromRequest(r)
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}
	file, err := h.stores.FileStore.GetFileByPath(project.ID, path)
	if err != nil {
		return nil, os.ErrNotExist
	}
	fi := file.ToFileInfo()
	return listerat{&fi}, nil
}

type listerat []os.FileInfo

// ListAt verifies that the particular index exists in the files array.
func (f listerat) ListAt(files []os.FileInfo, offset int64) (int, error) {
	fmt.Println("ListAt:", offset, len(files))
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

// getProject retrieves the project from the path. The r.Filepath contains the project slug as
// a part of the path. This method strips that out. The mcfsHandler has two caches for projects
// the first mcfsHandler.projects is a cache of already loaded projects, indexed by the slug. The
// second is mcfsHandler.projectsWithoutAccess which is a cache of booleans indexed by the project
// slug for project slugs that either don't exist or that the user doesn't have access to. Only
// if the slug isn't found in either of these caches is an attempt to look it up (and if the
// lookup is successful also check access) done. The lookup will fill out the appropriate
// project cache (mcfsHandler.projects or mcfsHandler.projectsWithoutAccess).
func (h *mcfsHandler) getProject(r *sftp.Request) (*mcmodel.Project, error) {
	var (
		ok      bool
		project *mcmodel.Project
		err     error
	)

	// Protect access to the two project caches.
	h.mu.Lock()
	defer h.mu.Unlock()

	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)
	if project, ok = h.projects[projectSlug]; ok {
		return project, nil
	}

	if _, ok := h.projectsWithoutAccess[projectSlug]; ok {
		return nil, fmt.Errorf("no such project: %s", projectSlug)
	}

	// If we are here then we haven't seen this project slug before.
	if project, err = h.stores.ProjectStore.GetProjectBySlug(projectSlug); err != nil {
		// Can't find it so treat as no access
		h.projectsWithoutAccess[projectSlug] = true
		return nil, err
	}

	if !h.stores.ProjectStore.UserCanAccessProject(h.user.ID, project.ID) {
		// Found it but user doesn't have access to it.
		h.projectsWithoutAccess[projectSlug] = true
		return nil, fmt.Errorf("no such project: %s", projectSlug)
	}

	// Found the project and user has access so put in the projects cache.
	h.projects[projectSlug] = project

	return project, err
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

// isOpenForRead returns true if the file was opened for read. It exists for readability purposes.
func (f *MCFile) isOpenForRead() bool {
	return f.openForWrite == false
}

// getPathFromRequest will get the path to the file from the request after it removes the
// project slug.
func getPathFromRequest(r *sftp.Request) string {
	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)
	p := mc.RemoveProjectSlugFromPath(r.Filepath, projectSlug)
	return p
}
