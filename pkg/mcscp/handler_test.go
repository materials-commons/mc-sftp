package mcscp

import (
	"testing"

	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/materials-commons/mc-ssh/pkg/mc"
	"github.com/stretchr/testify/require"
)

type testSshSession struct {
}

func TestMcfsHandler_Glob(t *testing.T) {

}

func TestMcfsHandler_Mkdir(t *testing.T) {

}

func TestMcfsHandler_NewDirEntry(t *testing.T) {
	stores := makeStoresWithFakes()
	handler := NewMCFSHandler(stores, "/tmp")
	session := newFakeSshSession()

	dirEntry, err := handler.NewDirEntry(session, "/proj/dir1")

	require.Nil(t, err, "NewDirEntry unexpected returned err: %s for directory /proj/dir1", err)
	require.NotNil(t, dirEntry, "NewDirEntry succeeded but returned nil for the DirEntry")
}

func TestMcfsHandler_NewFileEntry(t *testing.T) {

}

func TestMcfsHandler_WalkDir(t *testing.T) {

}

func TestMcfsHandler_Write(t *testing.T) {

}

func TestMcfsHandler_loadProjectAndUserIntoHandler(t *testing.T) {

}

func makeStoresWithFakes() *mc.Stores {
	projects := []mcmodel.Project{
		{ID: 1, Slug: "proj", OwnerID: 1},
	}

	files := []mcmodel.File{
		{ID: 1, Name: "/", Path: "/", ProjectID: 1, OwnerID: 1, MimeType: "directory"},
		{ID: 2, Name: "dir1", Path: "/dir1", ProjectID: 1, OwnerID: 1, MimeType: "directory"},
	}

	return &mc.Stores{
		FileStore:       store.NewFakeFileStore(files),
		ProjectStore:    store.NewFakeProjectStore(projects),
		ConversionStore: store.NewFakeConversionStore(),
	}
}
