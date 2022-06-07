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

// mcfsHandler implements the scp.CopyToClientHandler and scp.CopyFromClientHandler interfaces
// that implement receiving and sending files/directories over SCP. A few things of note:
//
//    1. All the callbacks that were implemented for scp.CopyToClientHandler and scp.CopyFromClientHandler
//       have to load the project and the user. This is done by every method calling
//       h.loadProjectAndUserIntoHandler. Because there is no guaranteed order that the callbacks will
//       be called in, each callback calls this method. The loadProjectAndUserIntoHandler will load the
//       mcfsHandler.user and mcfsHandler.project fields only if they are nil. Otherwise, it just returns
//       because these are already set. Also, loadProjectAndUserIntoHandler checks the flag
//       mcfsHandler.fatalErrorLoadingProjectOrUser allowing it to error fast if a previous call was made
//       and failed to load either the project or user.
//
//    2. The callbacks have to deal with the path. Path handling is special because the mcscp server needs
//       to know the project that the user is writing to/reading from. The way this is handled is that the
//       user has to encode the project in the path. Each Materials Commons project has a "slug" associated
//       with it, which is a short unique identifier derived from the project name. When a user uses scp
//       to upload/download files for Materials Commons, the path encodes the project slug to identify
//       which project is being accessed. For example if the user has a project with a unique project
//       slug of "my-project", then to specify upload/download for the project the user specifies a
//       path that starts with /my-project. As an example the following scp command would recursively upload
//       the directory /tmp/d3 into the project with slug 'my-project' and into it's jpegs directory:
//
//           scp -r /tmp/d3 mc-user@materialscommons.org:/my-project/jpegs
//
//       When this happens the callbacks will remove the project slug from the path, so that any files or
//       directories that are accessed/created/read/written to use the path starting with /jpegs. This
//       path handling is done in each routine by calling mc.RemoveProjectSlugFromPath(path, h.project.Slug)
//       where path is the original path (eg /my-project/jpegs/file.jpg), and h.project.Slug is the project
//       slug to remove from the path (in this case 'my-project').
//
//    3. Each Materials Commons user also has a unique user slug. This is derived from the users email
//       address and is how the user identifies their materials commons account. For the website the
//       user uses their email to login. This doesn't work for scp as scp uses the @ to separate the
//       username from the host. So for scp the user has to specify their user slug.
//
type mcfsHandler struct {
	// The user is set in the context from the passwordHandler method in cmd/mc-sshd/cmd/root. Rather than
	// constantly retrieving it we get it one time and set it in the mcfsHandler. See
	// loadProjectAndUserIntoHandler for details.
	user *mcmodel.User

	// The project that this scp instance is using. It gets loaded from the path the user specified. See
	// loadProjectAndUserIntoHandler and pkg/mc/util mc.*ProjectSlug* methods for how this is handled.
	project *mcmodel.Project

	// The different stores used in the handler.
	stores *mc.Stores

	// Each callback has to attempt to load the project and the user. The project and user gets loaded once in the
	// mcfsHandler by loadProjectAndUserIntoHandler. However, it's possible that the project is invalid or there
	// was an upstream error and the user wasn't set. Either of these are fatal errors. The loadProjectAndUserIntoHandler
	// uses this flag to see if an attempt was made to load these and failed allow it to error out quickly
	// in subsequent calls when this flag is set.
	fatalErrorLoadingProjectOrUser bool

	// This is the root where files get stored in Materials Commons. This path is needed for creating
	// or reading existing files (eg calls like os.Open).
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

// Glob We don't support Glob for now...
func (h *mcfsHandler) Glob(_ ssh.Session, pattern string) ([]string, error) {
	//fmt.Println("scp Glob:", pattern)

	// Just return an array containing a single entry which is the pattern specified.
	return []string{pattern}, nil
}

// WalkDir implements directory walking for SCP. It is heavily based on filepath.WalkDir and modified to
// work with Materials Commons.
func (h *mcfsHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	if err := h.loadProjectAndUserIntoHandler(s, path); err != nil {
		return err
	}

	cleanedPath := mc.RemoveProjectSlugFromPath(path, h.project.Slug)

	// Get the initial directory
	d, err := h.stores.FileStore.GetDirByPath(h.project.ID, cleanedPath)
	if err != nil {
		// If there was an error then pass the error to the callback (for whatever processing it
		// will do.
		err = fn(cleanedPath, nil, err)
	} else {
		// No error, so begin walking the directory we just loaded.
		err = h.walkDir(cleanedPath, d.ToDirEntry(), fn)
	}

	if err == filepath.SkipDir {
		return nil
	}

	return err
}

// walkDir is where the actual recursive calls happen for directory walking.
func (h *mcfsHandler) walkDir(path string, d fs.DirEntry, fn fs.WalkDirFunc) error {
	// Directory that was just loaded, so pass to callback and see what it does.
	if err := fn(path, d, nil); err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			// Skipped directory
			err = nil
		}

		return err
	}

	// If we are here then its time to list the directory contents and start processing them.
	dirs, err := h.stores.FileStore.ListDirectoryByPath(h.project.ID, path)
	if err != nil {
		log.Errorf("Failure find path %q in project %d: %s", path, h.project.ID, err)
		err = fn(path, d, err)
		if err != nil {
			return err
		}
	}

	// Loop through each of the retrieved directories, recursively walking them.
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

// NewDirEntry creates a new directory entry to send back to the client where it will be (if needed) created.
// The directory needs to exist in Materials Commons. NewDirEntry doesn't create directories on the server
// it sends back existing directories to the client.
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

// NewFileEntry sends back the new file entry, and also the handle and a close function for the file. For
// Materials Commons this means locating the real file by it's UUID (file.ToUnderlyingFilePath(mcfsRoot)),
// and using os.Open to read it. NewFileEntry doesn't create a file on the server. It sends back to the
// client an existing file.
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
// called when a recursive upload is specified. So the Write() callback also needs
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
// also has to handle directory creation. Creating a new file has a number of considerations
// including version handling, only storing files once that share the same checksum (and instead pointing
// at these previously uploaded files), potentially creating a web version of the file for viewing on
// the web, updating project statistics, etc... Read the comments in the method to see the details.
func (h *mcfsHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	var (
		err  error
		dir  *mcmodel.File
		file *mcmodel.File
	)

	// After writing the file if we determine that a file matching its checksum already exists then
	// we can delete the file just written (because we updated the file in the database to point at
	// the file with the matching checksum). Assume this is not the case, but if a matching file is
	// found then Write will set deleteFile to true. The defer method to close the opened file will
	// then take care of deleting the file since a version with that checksum already exists.
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
			// A file matching this file's checksum already exists in the system so delete the file we just
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

	checksum := fmt.Sprintf("%x", hasher.Sum(nil))
	// Note deleteFile in the if statement - DoneWritingToFile will switch the file if there was an existing file that had the
	// same checksum. Here is where deleteFile gets set so that it can delete the file that was just written
	// if this switch occurred.
	if deleteFile, err = h.stores.FileStore.DoneWritingToFile(file, checksum, written, h.stores.ConversionStore); err != nil {
		log.Errorf("Failure updating file (%d) and project (%d) metadata: %s", file.ID, h.project.ID, err)
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
// the user and project fields being set.
func (h *mcfsHandler) loadProjectAndUserIntoHandler(s ssh.Session, path string) error {
	if h.fatalErrorLoadingProjectOrUser {
		// A previous attempt at loading either project or user failed. This is a fatal error
		// so that previous attempt set fatalErrorLoadingProjectOrUser to true. We respect
		// this flag and return an error.
		return fmt.Errorf("fatal error user or project invalid")
	}

	// Short circuit - check if project and user have already been loaded.
	if h.user != nil && h.project != nil {
		// Already loaded both so nothing further to do.
		return nil
	}

	// Check if user was already loaded.
	if h.user == nil {
		// h.user wasn't previously loaded so attempt to load it.
		if err := h.loadUserFromContextIntoHandler(s); err != nil {
			// Fatal error set fatalErrorLoadingProjectOrUser so that this method can short-circuit lookups.
			h.fatalErrorLoadingProjectOrUser = true
			return err
		}
	}

	// If we are here then user is loaded, so now we handle project.

	// Check if project was already loaded.
	if h.project == nil {
		// h.project wasn't previously loaded to attempt to load it.
		if err := h.loadProjectFromPathIntoHandler(path, h.user.ID); err != nil {
			// Fatal error - set fatalErrorLoadingProjectOrUser so that this method
			// can short-circuit lookups in the future.
			h.fatalErrorLoadingProjectOrUser = true
			return err
		}
	}

	return nil
}

// loadUserFromContextIntoHandler loads the user context that was set in the passwordHandler method
// in cmd/mc-sshd/cmd/root.go.
//
// **This method should never be called outside loadProjectAndUserIntoHandler.**
func (h *mcfsHandler) loadUserFromContextIntoHandler(s ssh.Session) error {
	if h.user != nil {
		// user already loaded, no need to retrieve it.
		return nil
	}

	// Paranoid checking to make sure there wasn't a previous attempt that set h.fatalErrorLoadingProjectOrUser
	if h.fatalErrorLoadingProjectOrUser {
		return fmt.Errorf("internal error no user")
	}

	var ok bool

	// Cache the user from the ssh.Session context into our handler. Only load this once.
	// See passwordHandler in cmd/mc-sshd/cmd/root for setting the "mcuser" key.
	h.user, ok = s.Context().Value("mcuser").(*mcmodel.User)

	// Make sure that we can retrieve the user and if not then return an error.
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
	var (
		project *mcmodel.Project
		err     error
	)
	if h.fatalErrorLoadingProjectOrUser {
		// Already tried looking up the project slug and either it doesn't exist or the user
		// didn't have access. No need to try again, just return an error.
		return fmt.Errorf("internal error no project")
	}

	if project, err = mc.GetAndValidateProjectFromPath(path, userID, h.stores.ProjectStore); err != nil {
		return err
	}

	// If we are here then the project exists and the user has access so set it in the handler.
	h.project = project

	return nil
}
