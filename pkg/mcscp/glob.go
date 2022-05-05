package mcscp

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Code modified from  go's path/filepath/match.go

func (h *mcfsHandler) glob(pattern string) (matches []string, err error) {
	// Check pattern is well-formed
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, err
	}

	if !hasMeta(pattern) {
		if _, err = h.stores.FileStore.GetFileByPath(h.project.ID, pattern); err != nil {
			return nil, nil
		}
		return []string{pattern}, nil
	}

	dir, file := filepath.Split(pattern)
	volumeLen := 0

	dir = cleanGlobPath(dir)

	if !hasMeta(dir[volumeLen:]) {
		return h.glob2(dir, file, nil)
	}

	return nil, nil
}

func (h *mcfsHandler) glob2(dir string, file string, t interface{}) ([]string, error) {
	return nil, nil
}

// cleanGlobPath prepares path for glob matching.
func cleanGlobPath(path string) string {
	switch path {
	case "":
		return "."
	case string(filepath.Separator):
		// do nothing to the path
		return path
	default:
		return path[0 : len(path)-1] // chop off trailing separator
	}
}

// hasMeta reports whether path contains any of the magic characters
// recognized by Match.
func hasMeta(path string) bool {
	magicChars := `*?[`
	if runtime.GOOS != "windows" {
		magicChars = `*?[\`
	}
	return strings.ContainsAny(path, magicChars)
}
