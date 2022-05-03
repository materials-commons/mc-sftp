package mc

import (
	"mime"
	"path/filepath"
	"strings"
)

// RemoveProjectSlugFromPath removes the slug project name from the path. For example the slug
// project name might be my-project-acf4. All paths will be prefixed with /my-project-acf4. So if
// the path is /my-project-acf4/file.txt then this method will return /file.txt
func RemoveProjectSlugFromPath(path, projectSlug string) string {
	cleanedPath := filepath.Clean(path)
	sluggedNamePath := filepath.Join("/", projectSlug)

	if strings.HasPrefix(cleanedPath, sluggedNamePath) {
		return strings.TrimPrefix(cleanedPath, sluggedNamePath)
	}

	return cleanedPath
}

func GetProjectSlugFromPath(path string) string {
	parts := strings.Split(path, "/")
	// parts will be an array with the first element being an
	// empty string. For example if the path is /my-project/this/that,
	// then the array will be:
	// ["", "my-project", "this", "that"]
	// So the project slug is parts[1]
	projectSlug := parts[1]

	return projectSlug
}

// GetMimeType will determine the type of a file from its extension. It strips out the extra information
// such as the charset and just returns the underlying type. It returns "unknown" for the mime type if
// the mime package is unable to determine the type.
func GetMimeType(name string) string {
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
