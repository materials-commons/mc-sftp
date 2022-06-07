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
	tests := []struct {
		tname      string
		path       string
		shouldFail bool
	}{
		{"Test Project/Dir exist", "/proj/dir1", false},
		{"Test project does not exist", "/proj-not-exist/dir1", true},
		{"Test project exists, dir does not exist", "/proj/dir-not-exist", true},
	}

	for _, test := range tests {
		t.Run(test.tname, func(t *testing.T) {
			dirEntry, err := handler.NewDirEntry(session, test.path)
			if test.shouldFail {
				require.NotNil(t, err, "NewDirEntry unexpectedly passed, should have errored for path %s", test.path)
				require.Nil(t, dirEntry, "NewDirEntry correctly failed but dirEntry should be nil for path %s", test.path)
			} else {
				require.Nil(t, err, "NewDirEntry should have succeeded, but got err %s for path %s", err, test.path)
				require.NotNil(t, dirEntry, "NewDirentry succeeded but returned nil for dirEntry for path %s", test.path)
			}
		})
	}
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
