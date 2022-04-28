package mcscp

import (
	"crypto/md5"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/apex/log"
	"github.com/charmbracelet/wish/scp"
	"github.com/gliderlabs/ssh"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"gorm.io/gorm"
)

type mcfsHandler struct {
	user         mcmodel.User
	project      mcmodel.Project
	fileStore    *store.FileStore
	projectStore *store.ProjectStore
	root         string
	mcfsRoot     string
}

func NewMCFSHandler(db *gorm.DB, root string, mcfsRoot string, user mcmodel.User, project mcmodel.Project) scp.Handler {
	return &mcfsHandler{
		user:         user,
		project:      project,
		fileStore:    store.NewFileStore(db, mcfsRoot),
		projectStore: store.NewProjectStore(db),
		root:         root,
		mcfsRoot:     mcfsRoot,
	}
}

// Implement the scp.CopyToClientHandler interface

// Glob Don't support Glob for now...
func (h *mcfsHandler) Glob(s ssh.Session, pattern string) ([]string, error) {
	return []string{pattern}, nil
}

func (h *mcfsHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	cleanedPath := h.removeSlugProjectNameFromPath(path)
	d, err := h.fileStore.FindDirByPath(h.project.ID, cleanedPath)
	if err != nil {
		err = fn(cleanedPath, nil, err)
	} else {
		err = h.walkDir(cleanedPath, d.ToDirEntry(), fn)
	}

	if err == filepath.SkipDir {
		return nil
	}

	return err
}

func (h *mcfsHandler) walkDir(path string, d fs.DirEntry, fn fs.WalkDirFunc) error {
	if err := fn(path, d, nil); err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			// Skipped directory
			err = nil
		}

		return err
	}

	dirs, err := h.fileStore.ListDirectoryByPath(h.project.ID, path)
	if err != nil {
		err = fn(path, d, err)
		if err != nil {
			return err
		}
	}

	for _, dir := range dirs {
		p := filepath.Join(path, dir.Name)
		dirEntry := dir.ToDirEntry()
		if err := h.walkDir(p, dirEntry, fn); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

func (h *mcfsHandler) NewDirEntry(s ssh.Session, name string) (*scp.DirEntry, error) {
	path := h.removeSlugProjectNameFromPath(name)
	dir, err := h.fileStore.FindDirByPath(h.project.ID, path)
	if err != nil {
		return nil, fmt.Errorf("failed to open dir '%s' for project %d: %s", path, h.project.ID, err)
	}

	return &scp.DirEntry{
		Children: []scp.Entry{},
		Name:     filepath.Base(path),
		Filepath: path,
		Mode:     0777,
		Mtime:    dir.UpdatedAt.Unix(),
		Atime:    dir.UpdatedAt.Unix(),
	}, nil
}

func (h *mcfsHandler) NewFileEntry(_ ssh.Session, name string) (*scp.FileEntry, func() error, error) {
	path := h.removeSlugProjectNameFromPath(name)
	file, err := h.fileStore.FindFileByPath(h.project.ID, path)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to find file '%s' in project %d: %s", path, h.project.ID, err)
	}

	f, err := os.Open(file.ToUnderlyingFilePath(h.mcfsRoot))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open %q: %w", path, err)
	}

	return &scp.FileEntry{
		Name:     file.Name,
		Filepath: path,
		Mode:     0777,
		Size:     int64(file.Size),
		Mtime:    file.UpdatedAt.Unix(),
		Atime:    file.UpdatedAt.Unix(),
		Reader:   f,
	}, f.Close, nil
}

// Implement the scp.CopyFromClientHandler interface

func (h *mcfsHandler) Mkdir(s ssh.Session, entry *scp.DirEntry) error {
	path := h.removeSlugProjectNameFromPath(entry.Filepath)
	parentPath := filepath.Dir(path)
	parentDir, err := h.fileStore.FindDirByPath(h.project.ID, parentPath)
	if err != nil {
		return fmt.Errorf("parent directory doesn't exist in project %d, parent path %s: %s", h.project.ID, parentPath, err)
	}

	// Fake up a TransferRequest for CreateDirectory, since only the projectID and OwnerID are used
	// TODO: Fix the CreateDirectory API so this doesn't need to be done.
	tr := mcmodel.TransferRequest{ProjectID: h.project.ID, OwnerID: h.user.ID}
	_, err = h.fileStore.CreateDirectory(parentDir.ID, path, filepath.Base(path), tr)
	if err != nil {
		return fmt.Errorf("unable to create directory path %s in directory %d: %s", path, parentDir.ID, err)
	}

	return nil
}

// Write will create a new file version in the project and write the data to the physical file.
func (h *mcfsHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	var (
		err  error
		dir  *mcmodel.File
		file *mcmodel.File
	)

	// First steps - Make sure the project has the directory already in. If it doesn't there is
	// a failure somewhere else as the directory should have been created.
	if dir, err = h.fileStore.FindDirByPath(h.project.ID, filepath.Dir(entry.Filepath)); err != nil {
		return 0, fmt.Errorf("unable to find dir '%s' for project %d: %s", filepath.Dir(entry.Filepath), h.project.ID, err)
	}

	// Create a file that isn't set as current. This way the file doesn't show up until it's
	// data has been written.
	if file, err = h.fileStore.CreateFile(entry.Name, h.project.ID, dir.ID, h.user.ID, getMimeType(entry.Name)); err != nil {
		return 0, fmt.Errorf("unable to create file '%s' in dir %d for project %d: %s", entry.Name, dir.ID, h.project.ID, err)
	}

	f, err := os.OpenFile(file.ToUnderlyingFilePath(h.mcfsRoot), os.O_TRUNC|os.O_RDWR|os.O_CREATE, entry.Mode)
	if err != nil {
		return 0, fmt.Errorf("failed to open file %d path '%s': %s", file.ID, file.ToUnderlyingFilePath(h.mcfsRoot), err)
	}

	// The file is written into in one go in the io.Copy. So we can safely close the file when this
	// method finishes.
	defer func() {
		if err := f.Close(); err != nil {
			log.Errorf("error closing file (%d) at '%s': %s", file.ID, file.ToUnderlyingFilePath(h.mcfsRoot), err)
		}
	}()

	// Each file in Materials Commons has a checksum associated with it. Create a TeeReader so that as the file is
	// read it goes to two separate destinations. One is the file we just opened, and the second is the hasher that
	// be computing the hash.
	hasher := md5.New()
	teeReader := io.TeeReader(entry.Reader, hasher)

	written, err := io.Copy(f, teeReader)
	if err != nil {
		log.Errorf("failure writing to file %d: %s", file.ID, err)
	}

	// Finally mark the file as current, and update all the associated metadata for the file and the project. In the
	// project we track aggregate statistics such as total project size.
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if err := h.fileStore.UpdateMetadataForFileAndProject(file, checksum, h.project.ID, written); err != nil {
		log.Errorf("failure updating file (%d) and project (%d) metadata: %s", file.ID, h.project.ID, err)
	}

	return written, nil
}

//// Utility methods

// removeSlugProjectNameFromPath removes the slug project name from the path. For example the slug
// project name might be my-project-acf4. All paths will be prefixed with /my-project-acf4. So if
// the path is /my-project-acf4/file.txt then this method will return /file.txt
func (h *mcfsHandler) removeSlugProjectNameFromPath(path string) string {
	cleanedPath := filepath.Clean(path)
	// Replace Name with slug once we've added it
	sluggedNamePath := filepath.Join("/", h.project.Name)

	if strings.HasPrefix(cleanedPath, sluggedNamePath) {
		return strings.TrimPrefix(cleanedPath, sluggedNamePath)
	}

	return cleanedPath
}

// getMimeType will determine the type of a file from its extension. It strips out the extra information
// such as the charset and just returns the underlying type. It returns "unknown" for the mime type if
// the mime package is unable to determine the type.
func getMimeType(name string) string {
	mimeType := mime.TypeByExtension(filepath.Ext(name))
	if mimeType == "" {
		return "unknown"
	}

	semicolon := strings.Index(mimeType, ";")
	if semicolon == -1 {
		return strings.TrimSpace(mimeType)
	}

	return strings.TrimSpace(mimeType[:semicolon])
}
