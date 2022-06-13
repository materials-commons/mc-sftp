package mcsftp

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/mc-ssh/pkg/mc"
	"github.com/pkg/sftp"
)

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
//    continuously look up projects. The mcfsHandler caches projects that were already looked up.
//    These are cached by project slug. It also caches failed projects either because the
//   project-slug didn't exist or the user didn't have access to the project.
type mcfsHandler struct {
	// user is the Materials Commons user for this SFTP session.
	user *mcmodel.User

	stores *mc.Stores

	// mcfsRoot is the directory path where Materials Commons files are being read from/written to.
	mcfsRoot string

	// Tracks all the projects the user has accessed that they also have rights to.
	// The key is the project slug.
	// If this were a map it would look like: map[string]*mcmodel.Project
	projects sync.Map

	// Tracks all the project the user has accessed that they *DO NOT* have rights to.
	// The key is the project slug.
	// If this were a map it would look like: map[string]bool
	projectsWithoutAccess sync.Map
}

// NewMCFSHandler creates a new handler. This is called each time a user connects to the SFTP server.
func NewMCFSHandler(user *mcmodel.User, stores *mc.Stores, mcfsRoot string) sftp.Handlers {
	h := &mcfsHandler{
		user:     user,
		stores:   stores,
		mcfsRoot: mcfsRoot,
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
	flags := r.Pflags()
	if !flags.Read {
		log.Errorf("Attempt to open file %s for read, but flag not set to read", r.Filepath)
		return nil, os.ErrInvalid
	}

	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		log.Errorf("Unable to create MCFile: %s", err)
		return nil, os.ErrNotExist
	}

	if mcFile.file, err = h.stores.FileStore.GetFileByPath(mcFile.project.ID, getPathFromRequest(r)); err != nil {
		log.Errorf("Unable to find file %s in project %d for user %d: %s", getPathFromRequest(r), mcFile.project.ID, h.user.ID, err)
		return nil, os.ErrNotExist
	}

	if mcFile.fileHandle, err = os.Open(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot)); err != nil {
		log.Errorf("Unable to open file %s: %s", mcFile.file.ToUnderlyingFilePath(h.mcfsRoot), err)
		return nil, os.ErrNotExist
	}

	return mcFile, nil
}

// Filewrite sets up a file for writing. It creates a file or new file version in Materials Commons
// as well as the underlying real physical file to write to.
func (h *mcfsHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if !flags.Write {
		// Pathological case, Filewrite should always have the flags.Write set to true.
		log.Errorf("Attempt to write file %s, but write flag not set", r.Filepath)
		return nil, os.ErrInvalid
	}

	// Set up the initial SFTP request file state.
	mcFile, err := h.createMCFileFromRequest(r)
	if err != nil {
		log.Errorf("Error creating file: %s", err)
		return nil, os.ErrNotExist
	}

	// Create the Materials Commons file. This handles version creation.
	fileName := filepath.Base(r.Filepath)
	mcFile.file, err = h.stores.FileStore.CreateFile(fileName, mcFile.project.ID, mcFile.dir.ID, h.user.ID, mc.GetMimeType(fileName))
	if err != nil {
		log.Errorf("Error creating file %s for user %d in directory %d of project %d: %s", fileName, h.user.ID, mcFile.dir.ID, mcFile.project.ID, err)
		return nil, os.ErrNotExist
	}

	// Create the directory path where the file will be written to
	if err := os.MkdirAll(mcFile.file.ToUnderlyingDirPath(h.mcfsRoot), 0777); err != nil {
		log.Errorf("Error creating directory path %s: %s", mcFile.file.ToUnderlyingDirPath(h.mcfsRoot), err)
		return nil, os.ErrNotExist
	}

	if mcFile.fileHandle, err = os.Create(mcFile.file.ToUnderlyingFilePath(h.mcfsRoot)); err != nil {
		log.Errorf("Error creating file %s on filesystem: %s", mcFile.file.ToUnderlyingFilePath(h.mcfsRoot), err)
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
func (h *mcfsHandler) createMCFileFromRequest(r *sftp.Request) (*mcfile, error) {
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	path := getPathFromRequest(r)

	dir, err := h.stores.FileStore.GetDirByPath(project.ID, filepath.Dir(path))
	if err != nil {
		log.Errorf("Error looking up directory %s in project %d: %s", filepath.Dir(path), project.ID, err)
		return nil, os.ErrNotExist
	}

	return &mcfile{
		project: project,
		dir:     dir,
		stores:  h.stores,
	}, nil
}

// Filecmd supports various SFTP commands that manipulate a file and/or filesystem. It only supports
// Mkdir for directory creation. Deletes, renames, setting permissions, etc... are not supported.
func (h *mcfsHandler) Filecmd(r *sftp.Request) error {
	project, err := h.getProject(r)
	if err != nil {
		return err
	}

	path := getPathFromRequest(r)

	switch r.Method {
	case "Mkdir":
		_, err := h.stores.FileStore.GetOrCreateDirPath(project.ID, h.user.ID, path)
		if err != nil {
			log.Errorf("Unable find or create directory path %s in project %d for user %d: %s", path, project.ID, h.user.ID, err)
		}
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
	// The reason this check for the filepath and method isn't done in the case statement below
	// when matching on "List" for the method is that this is a specialized case, where the user
	// is looking at /, and there isn't a project, so we need to build a list of projects and return
	// that list to be presented as directories off of /. The code after this if block assumes that
	// the user is already in a project, and is looking up the project in the path.
	if r.Filepath == "/" && r.Method == "List" {
		// Root path listing, so build a list of project stubs that the user has access to. Treat each
		// of these as a directory in the root.
		projects, err := h.stores.ProjectStore.GetProjectsForUser(h.user.ID)
		if err != nil {
			return nil, fmt.Errorf("unable to get list of projects: %s", err)
		}

		var projectList []os.FileInfo

		// Go through each project creating a fake file (directory) that is the project slug
		for _, project := range projects {
			f := mcmodel.File{
				Name:      project.Slug,
				MimeType:  "directory",
				Size:      uint64(project.Size),
				Path:      filepath.Join("/", project.Slug),
				UpdatedAt: project.UpdatedAt,
			}
			projectList = append(projectList, f.ToFileInfo())
		}

		return listerat(projectList), nil
	}

	if r.Filepath == "/" && r.Method == "Stat" {
		// Stat of root path so create a fake one so there is no error
		f := mcmodel.File{
			Name:      "/",
			MimeType:  "directory",
			Size:      0,
			Path:      "/",
			UpdatedAt: time.Now(),
		}
		return listerat{f.ToFileInfo()}, nil
	}

	// If we are here then we are in a project path context, so do the usual steps to retrieve the project. That
	// is the user isn't looking at "/", but is looking at something like "/my-project". So we can look at the
	// path and check out its project context.
	path := getPathFromRequest(r)
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}

	switch r.Method {
	case "List":
		files, err := h.stores.FileStore.ListDirectoryByPath(project.ID, path)
		if err != nil {
			log.Errorf("Unable to list directory %s in project %d: %s", path, project.ID, err)
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
			log.Errorf("Unable to lookup file %s in project %d: %s", path, project.ID, err)
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
	path := getPathFromRequest(r)
	project, err := h.getProject(r)
	if err != nil {
		return nil, os.ErrNotExist
	}
	file, err := h.stores.FileStore.GetFileByPath(project.ID, path)
	if err != nil {
		log.Errorf("Unable to lookup file %s in project %d: %s", path, project.ID, err)
		return nil, os.ErrNotExist
	}
	fi := file.ToFileInfo()
	return listerat{&fi}, nil
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
	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)

	// Check if we previously found this project.
	if proj, ok := h.projects.Load(projectSlug); ok {
		// Paranoid check - Make sure that the item returned is a *mcmodel.Project
		// and return an error if it isn't.
		p, okCast := proj.(*mcmodel.Project)
		if !okCast {
			// Bug - The item stored in h.projects is not a *mcmodel.Project, so delete
			// it and return an error saying we can't find the project. Also set the
			// projectSlug in h.projectsWithoutAccess so, we don't just continually try
			// to load this.
			h.projects.Delete(projectSlug)
			h.projectsWithoutAccess.Store(projectSlug, true)
			log.Errorf("error casting to project for slug %s", projectSlug)
			return nil, fmt.Errorf("no such project: %s", projectSlug)
		}

		return p, nil
	}

	// Check if we tried to load the project in the past and failed.
	if _, ok := h.projectsWithoutAccess.Load(projectSlug); ok {
		return nil, fmt.Errorf("no such project: %s", projectSlug)
	}

	// If we are here then we've never tried loading the project.

	var (
		project *mcmodel.Project
		err     error
	)

	if project, err = mc.GetAndValidateProjectFromPath(r.Filepath, h.user.ID, h.stores.ProjectStore); err != nil {
		// Error looking up or validating access. Mark this project slug as invalid.
		h.projectsWithoutAccess.Store(projectSlug, true)
		return nil, err
	}

	// Found the project and user has access so put in the projects cache.
	h.projects.Store(projectSlug, project)

	return project, nil
}

// getPathFromRequest will get the path to the file from the request after it removes the
// project slug.
func getPathFromRequest(r *sftp.Request) string {
	projectSlug := mc.GetProjectSlugFromPath(r.Filepath)
	p := mc.RemoveProjectSlugFromPath(r.Filepath, projectSlug)
	return p
}
