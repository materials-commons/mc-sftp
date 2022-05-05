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
	"github.com/materials-commons/mc-ssh/pkg/mc"
)

type mcfsHandler struct {
	// The user is set in the context from the passwordHandler. Rather than constantly retrieving it
	// we get it one time and set it in the mcfsHandler. See loadProjectAndUserIntoHandler for details.
	user *mcmodel.User

	// The project that this scp instance is using. It gets loaded from the path the user specified. See
	// loadProjectAndUserIntoHandler and pkg/mc/util mc.*ProjectSlug* methods for how this is handled.
	project *mcmodel.Project

	// The different stores used in the handler.
	stores *mc.Stores

	// Each call has to attempt to load the project and the user. The project and user gets loaded once in the
	// mcfsHandler. However, it's possible that the project is invalid or there was an upstream error and the user
	// wasn't set. Either of these are fatal errors. The loadProjectAndUserIntoHandler can error out quickly
	// in subsequent calls if this flag is set.
	fatalErrorLoadingProjectOrUser bool

	// This is the root where files get stored in Materials Commons. This path is needed for creating
	// new (in the file system) files, as well as for setting up the fileStore.
	mcfsRoot string
}

func NewMCFSHandler(stores *mc.Stores, mcfsRoot string) scp.Handler {
	return &mcfsHandler{
		stores:                         stores,
		fatalErrorLoadingProjectOrUser: false,
		mcfsRoot:                       mcfsRoot,
	}
}

// Implement Glob, Walkdir, NewDirEntry and NewFileEntry for the scp.CopyToClientHandler interface

// Glob Don't support Glob for now...
func (h *mcfsHandler) Glob(_ ssh.Session, pattern string) ([]string, error) {
	fmt.Println("scp Glob:", pattern)
	return []string{pattern}, nil
}

func (h *mcfsHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	if err := h.loadProjectAndUserIntoHandler(s, path); err != nil {
		return err
	}

	cleanedPath := mc.RemoveProjectSlugFromPath(path, h.project.Slug)
	d, err := h.stores.FileStore.GetDirByPath(h.project.ID, cleanedPath)
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

	dirs, err := h.stores.FileStore.ListDirectoryByPath(h.project.ID, path)
	if err != nil {
		log.Errorf("Failure find path %q in project %d: %s", path, h.project.ID, err)
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
	if err := h.loadProjectAndUserIntoHandler(s, name); err != nil {
		return nil, err
	}

	path := mc.RemoveProjectSlugFromPath(name, h.project.Slug)
	dir, err := h.stores.FileStore.GetDirByPath(h.project.ID, path)
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
	if err := h.loadProjectAndUserIntoHandler(s, name); err != nil {
		return nil, nil, err
	}

	path := mc.RemoveProjectSlugFromPath(name, h.project.Slug)
	file, err := h.stores.FileStore.GetFileByPath(h.project.ID, path)
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
	if err := h.loadProjectAndUserIntoHandler(s, entry.Filepath); err != nil {
		return err
	}

	path := mc.RemoveProjectSlugFromPath(entry.Filepath, h.project.Slug)

	if _, err := h.stores.FileStore.GetOrCreateDirPath(h.project.ID, h.user.ID, path); err != nil {
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

	if err := h.loadProjectAndUserIntoHandler(s, entry.Filepath); err != nil {
		return 0, err
	}

	path := mc.RemoveProjectSlugFromPath(entry.Filepath, h.project.Slug)

	// First steps - Find or create the directories in the path
	if dir, err = h.stores.FileStore.GetOrCreateDirPath(h.project.ID, h.user.ID, filepath.Dir(path)); err != nil {
		return 0, fmt.Errorf("unable to find dir '%s' for project %d: %s", filepath.Dir(path), h.project.ID, err)
	}

	// Create a file that isn't set as current. This way the file doesn't show up until it's
	// data has been written.
	if file, err = h.stores.FileStore.CreateFile(entry.Name, h.project.ID, dir.ID, h.user.ID, mc.GetMimeType(entry.Name)); err != nil {
		log.Errorf("Error creating file %s in project %d, in directory %d for user %d: %s", entry.Name, h.project.ID, dir.ID, h.user.ID, err)
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

		if deleteFile {
			// A file matching this files checksum already exists in the system so delete the file we just
			// uploaded. See the call to h.stores.FileStore.PointAtExistingIfExists towards the end of this method.
			_ = os.Remove(file.ToUnderlyingFilePath(h.mcfsRoot))
		}
	}()

	// Each file in Materials Commons has a checksum associated with it. Create a TeeReader so that as the stream of
	// bytes is read it goes to two separate destinations. One is the file we just opened, and the second is the hasher
	// that is computing the hash.
	hasher := md5.New()
	teeReader := io.TeeReader(entry.Reader, hasher)

	written, err := io.Copy(f, teeReader)
	if err != nil {
		log.Errorf("failure writing to file %d: %s", file.ID, err)
	}

	// Mark the file as current and update all the associated metadata for the file and the project.
	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	if err := h.stores.FileStore.UpdateMetadataForFileAndProject(file, checksum, h.project.ID, written); err != nil {
		log.Errorf("failure updating file (%d) and project (%d) metadata: %s", file.ID, h.project.ID, err)
	}

	// Check if there is a file with matching checksum, and if so have the file point at it and set
	// deleteFile to true so that the defer call above that will close the file we wrote to will
	// also delete it.
	if switched, err := h.stores.FileStore.PointAtExistingIfExists(file); err == nil && switched {
		// There was no error returned and switched is set to true. This means there was an existing
		// file that we pointed at so the file we wrote to can be deleted.
		deleteFile = true
	}

	// Check if file type is one we do a conversion on to make viewable on the web, and if it is
	// then schedule a conversion to run.
	if file.IsConvertible() {
		// Queue up a conversion job
		if _, err := h.stores.ConversionStore.AddFileToConvert(file); err != nil {
			log.Errorf("failed adding file %d to be converted: %s", file.ID, err)
		}
	}

	return written, nil
}

// loadProjectAndUserIntoHandler will look up the user and project if they aren't already set
// in the mcfsHandler. Any errors loading these are considered fatal and set the handler flag
// fatalErrorLoadingProjectOrUser. This flag is checked when this method is called and if set
// then the method returns an error and doesn't attempt to do any retrievals. If there is no
// error then it checks if these values have been previously returned, and if so returns the
// value from the handler rather than looking them up again.
//
// **NOTE**: This method must be called as the first thing at the top of all the implemented
// callbacks for CopyFromClientHandler and CopyToClientHandler as the callbacks rely on
// the user and project context.
func (h *mcfsHandler) loadProjectAndUserIntoHandler(s ssh.Session, path string) error {
	if h.fatalErrorLoadingProjectOrUser {
		return fmt.Errorf("fatal error user or project invalid")
	}

	if err := h.loadUserFromContextIntoHandler(s); err != nil {
		// Fatal error set fatalErrorLoadingProjectOrUser so that this method can short-circuit lookups.
		h.fatalErrorLoadingProjectOrUser = true
		return err
	}

	if h.project != nil {
		// We've already retrieved it
		return nil
	}

	if err := h.loadProjectFromPathIntoHandler(path, h.user.ID); err != nil {
		// Fatal error set fatalErrorLoadingProjectOrUser so that this method can short-circuit lookups.
		h.fatalErrorLoadingProjectOrUser = true
		return err
	}

	return nil
}

// loadUserFromContextIntoHandler loads the user context that was set in the password handler.
//
// **This method should never be called outside loadProjectAndUserIntoHandler.**
func (h *mcfsHandler) loadUserFromContextIntoHandler(s ssh.Session) error {
	if h.user != nil {
		// user already loaded, no need to retrieve it.
		return nil
	}
	// Cache the user from the ssh.Session context into our handler. Only load this once.
	var ok bool
	// See passwordHandler in cmd/mc-sshd/cmd/root for setting the "mcuser" key.
	h.user, ok = s.Context().Value("mcuser").(*mcmodel.User)

	// Make sure that we can retrieve the user and if not then set as a fatal error.
	if !ok {
		return fmt.Errorf("internal error user not set")
	}

	return nil
}

// loadProjectFromPathIntoHandler loads the project from the path. The project is set at the beginning
// of the path and will be the same project across all scp callbacks. This method extracts the project
// and sets it in the handler. Even though the userID should be set in h.user.ID it is passed into
// this method explicitly to make the order dependency clear that loadUserFromContextIntoHandler should
// be called before this method is called.
//
// **This method should never be called outside loadProjectAndUserIntoHandler.**
func (h *mcfsHandler) loadProjectFromPathIntoHandler(path string, userID int) error {
	// Look up the project by the slug in the path. Each path needs to have the project slug encoded in it
	// so that we know which project the user is accessing.
	projectSlug := mc.GetProjectSlugFromPath(path)

	if h.fatalErrorLoadingProjectOrUser {
		// Already tried looking up the project slug and either it doesn't exist or the user
		// didn't have access. No need to try again, just return an error.
		return fmt.Errorf("no such project %s", projectSlug)
	}

	project, err := h.stores.ProjectStore.GetProjectBySlug(projectSlug)
	if err != nil {
		log.Errorf("No such project slug %s", projectSlug)
		return err
	}

	// Once we have the project we need to check that the user has access to the project.
	if !h.stores.ProjectStore.UserCanAccessProject(userID, project.ID) {
		log.Errorf("User %d doesn't have access to project %d (%s)", userID, project.ID, project.Slug)
		return fmt.Errorf("no such project %s", projectSlug)
	}

	// If we are here then the project exists and the user has access so set it in the handler.
	h.project = project

	return nil
}
