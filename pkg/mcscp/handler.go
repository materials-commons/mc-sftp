package mcscp

import (
	"crypto/md5"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/apex/log"
	"github.com/charmbracelet/wish/scp"
	"github.com/gliderlabs/ssh"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/materials-commons/mc-ssh/pkg/mc"
	"gorm.io/gorm"
)

type mcfsHandler struct {
	// The project that this scp instance is using. It gets loaded from the path the user specified. See
	// loadProjectIntoHandler and pkg/mc/util mc.*ProjectSlug* methods for how this is handled.
	project *mcmodel.Project

	fileStore       *store.FileStore
	projectStore    *store.ProjectStore
	conversionStore *store.ConversionStore

	// Each call has to attempt to load the project. The project gets loaded once in the mcfsHandler. However,
	// it's possible that the project is invalid and that multiple calls may be made to retrieve it. We don't
	// want to hit the database each time trying to retrieve an invalid project, so invalidProject tracks this
	// state. See the method loadProjectIntoHandler which is called at the top of each of the callbacks for
	// details on loading the project into the mcfsHandler struct, and for how invalidProject is used.
	invalidProject bool

	// This is the root where files get stored in Materials Commons. This path is needed for creating
	// new (in the file system) files, as well as for setting up the fileStore.
	mcfsRoot string
}

func NewMCFSHandler(db *gorm.DB, mcfsRoot string) scp.Handler {
	return &mcfsHandler{
		fileStore:       store.NewFileStore(db, mcfsRoot),
		projectStore:    store.NewProjectStore(db),
		conversionStore: store.NewConversionStore(db),
		invalidProject:  false,
		mcfsRoot:        mcfsRoot,
	}
}

// Implement Glob, Walkdir, NewDirEntry and NewFileEntry for the scp.CopyToClientHandler interface

// Glob Don't support Glob for now...
func (h *mcfsHandler) Glob(_ ssh.Session, pattern string) ([]string, error) {
	fmt.Println("scp Glob:", pattern)
	return []string{pattern}, nil
}

func (h *mcfsHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	fmt.Println("scp Walkdir:", path)
	if true {
		return fmt.Errorf("not implemented")
	}
	user := s.Context().Value("mcuser").(*mcmodel.User)
	if err := h.loadProjectIntoHandler(path, user.ID); err != nil {
		return err
	}
	cleanedPath := mc.RemoveProjectSlugFromPath(path, h.project.Name)
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
	fmt.Println("scp: NewDirEntry")
	if true {
		return nil, fmt.Errorf("not implemented")
	}
	user := s.Context().Value("mcuser").(*mcmodel.User)
	if err := h.loadProjectIntoHandler(name, user.ID); err != nil {
		return nil, err
	}

	path := mc.RemoveProjectSlugFromPath(name, h.project.Slug)
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

func (h *mcfsHandler) NewFileEntry(s ssh.Session, name string) (*scp.FileEntry, func() error, error) {
	fmt.Println("scp NewFileEntry:", name)
	user := s.Context().Value("mcuser").(*mcmodel.User)
	if err := h.loadProjectIntoHandler(name, user.ID); err != nil {
		return nil, nil, err
	}

	path := mc.RemoveProjectSlugFromPath(name, h.project.Slug)
	file, err := h.fileStore.FindFileByPath(h.project.ID, path)
	if err != nil {
		log.Errorf("Unable to find file %q in project %d: %s", path, h.project.ID, err)
		return nil, nil, fmt.Errorf("unable to find file '%s' in project %d: %s", path, h.project.ID, err)
	}

	f, err := os.Open(file.ToUnderlyingFilePath(h.mcfsRoot))
	if err != nil {
		log.Errorf("Failed to open file %q: %s", path, err)
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

// Implement Mkdir and Write for the scp.CopyFromClientHandler interface

// Mkdir will create missing directories in the upload. **Note** this callback is only
// called when a recursive upload is specified. So the Write callback below also needs
// to handle directory creation for individual files that are being written to a
// directory that doesn't exist.
func (h *mcfsHandler) Mkdir(s ssh.Session, entry *scp.DirEntry) error {
	// See passwordHandler in cmd/mc-sshd/cmd/root for setting the "mcuser" key.
	user := s.Context().Value("mcuser").(*mcmodel.User)

	if err := h.loadProjectIntoHandler(entry.Filepath, user.ID); err != nil {
		return err
	}

	path := mc.RemoveProjectSlugFromPath(entry.Filepath, h.project.Slug)

	if _, err := h.fileStore.FindOrCreateDirPath(h.project.ID, user.ID, path); err != nil {
		return fmt.Errorf("unable to find dir '%s' for project %d: %s", path, h.project.ID, err)
	}

	return nil
}

// Write will create a new file version in the project and write the data to the physical file.
// **Note** The Mkdir callback above is only called on recursive uploads. That means this method
// also has to handle directory creation.
func (h *mcfsHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	var (
		err  error
		dir  *mcmodel.File
		file *mcmodel.File
	)

	// After writing the file if we determine that a file matching its checksum already exists then
	// we can delete the file just written (because we updated the file in the database to point at
	// the file with the matching checksum)
	deleteFile := false

	// See passwordHandler in cmd/mc-sshd/cmd/root for setting the "mcuser" key.
	user := s.Context().Value("mcuser").(*mcmodel.User)

	if err := h.loadProjectIntoHandler(entry.Filepath, user.ID); err != nil {
		return 0, err
	}

	path := mc.RemoveProjectSlugFromPath(entry.Filepath, h.project.Slug)

	// First steps - Find or create the directories in the path
	if dir, err = h.fileStore.FindOrCreateDirPath(h.project.ID, user.ID, filepath.Dir(path)); err != nil {
		return 0, fmt.Errorf("unable to find dir '%s' for project %d: %s", filepath.Dir(path), h.project.ID, err)
	}

	// Create a file that isn't set as current. This way the file doesn't show up until it's
	// data has been written.
	if file, err = h.fileStore.CreateFile(entry.Name, h.project.ID, dir.ID, user.ID, mc.GetMimeType(entry.Name)); err != nil {
		log.Errorf("Error creating file %s in project %d, in directory %d for user %d: %s", entry.Name, h.project.ID, dir.ID, user.ID, err)
		return 0, fmt.Errorf("unable to create file '%s' in dir %d for project %d: %s", entry.Name, dir.ID, h.project.ID, err)
	}

	// Create the directory path where the file will be written to
	if err := os.MkdirAll(file.ToUnderlyingDirPath(h.mcfsRoot), 0777); err != nil {
		log.Errorf("Error creating directory path %s: %s", file.ToUnderlyingDirPath(h.mcfsRoot), err)
		return 0, err
	}

	f, err := os.OpenFile(file.ToUnderlyingFilePath(h.mcfsRoot), os.O_TRUNC|os.O_RDWR|os.O_CREATE, entry.Mode)
	if err != nil {
		log.Errorf("Failed to open file %d path '%s': %s", file.ID, file.ToUnderlyingFilePath(h.mcfsRoot), err)
		return 0, fmt.Errorf("failed to open file %d path '%s': %s", file.ID, file.ToUnderlyingFilePath(h.mcfsRoot), err)
	}

	// The file is written into in one go in the io.Copy. So we can safely close the file when this
	// method finishes.
	defer func() {
		if err := f.Close(); err != nil {
			log.Errorf("error closing file (%d) at '%s': %s", file.ID, file.ToUnderlyingFilePath(h.mcfsRoot), err)
		}

		// A file matching this files checksum already exists in the system so delete the file we just
		// uploaded. See the call to h.fileStore.PointAtExistingIfExists towards the end of this method.
		if deleteFile {
			_ = os.Remove(file.ToUnderlyingFilePath(h.mcfsRoot))
		}
	}()

	// Each file in Materials Commons has a checksum associated with it. Create a TeeReader so that as the file is
	// read it goes to two separate destinations. One is the file we just opened, and the second is the hasher that
	// is computing the hash.
	hasher := md5.New()
	teeReader := io.TeeReader(entry.Reader, hasher)

	written, err := io.Copy(f, teeReader)
	if err != nil {
		log.Errorf("failure writing to file %d: %s", file.ID, err)
	}

	// Mark the file as current and update all the associated metadata for the file and the project.
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if err := h.fileStore.UpdateMetadataForFileAndProject(file, checksum, h.project.ID, written); err != nil {
		log.Errorf("failure updating file (%d) and project (%d) metadata: %s", file.ID, h.project.ID, err)
	}

	// Check if there is a file with matching checksum, and if so have the file point at it and set
	// deleteFile to true so that the defer call above that will close the file we wrote to will
	// also delete it.
	if switched, err := h.fileStore.PointAtExistingIfExists(file); err == nil && switched {
		// There was no error returned and switched is set to true. This means there was an existing
		// file that we pointed at so the file we wrote to can be deleted.
		deleteFile = true
	}

	// Check if file type is one we do a conversion on to make viewable on the web, and if it is
	// then schedule a conversion to run.
	if file.IsConvertible() {
		// Queue up a conversion job
		if _, err := h.conversionStore.AddFileToConvert(file); err != nil {
			log.Errorf("failed adding file %d to be converted: %s", file.ID, err)
		}
	}

	return written, nil
}

// loadProjectIntoHandler will check if the handler (h.project) is nil. If it isn't then it will return
// nil (no error). If it is then it will attempt to look up the project from its slug in the path and
// will also check that the user has access to the project. If either of these fail then the h.project
// entry won't be set and an error will be returned.
func (h *mcfsHandler) loadProjectIntoHandler(path string, userID int) error {
	if h.project != nil {
		// We've already retrieved it
		return nil
	}

	// Look up the project by the slug in the path. Each path needs to have the project slug encoded in it
	// so that we know which project the user is accessing.
	projectSlug := mc.GetProjectSlugFromPath(path)

	if h.invalidProject {
		// Already tried looking up the project slug and either it doesn't exist or the user
		// didn't have access. No need to try again, just return an error.
		return fmt.Errorf("no such project %s", projectSlug)
	}

	project, err := h.projectStore.GetProjectBySlug(projectSlug)
	if err != nil {
		log.Errorf("No such project slug %s", projectSlug)

		// Mark invalidProject as true so we don't attempt to look it up again.
		h.invalidProject = true
		return err
	}

	// Once we have the project we need to check that the user has access to the project.
	if !h.projectStore.UserCanAccessProject(userID, project.ID) {
		log.Errorf("User %d doesn't have access to project %d (%s)", userID, h.project.ID, h.project.Slug)

		// Mark invalidProject as true so we don't attempt to look it up again.
		h.invalidProject = true
		return fmt.Errorf("no such project %s", projectSlug)
	}

	h.project = project

	return nil
}
