package mcsftp

import (
	"io"
	"os"

	"github.com/materials-commons/gomcdb/mcmodel"
	"github.com/materials-commons/gomcdb/store"
	"github.com/pkg/sftp"
)

type MCStores struct {
	fileStore       *store.FileStore
	projectStore    *store.ProjectStore
	conversionStore *store.ConversionStore
}

type MCHandler struct {
	User   *mcmodel.User
	stores *MCStores
}

func NewMCHandler(user *mcmodel.User, stores *MCStores) *MCHandler {
	return &MCHandler{User: user, stores: stores}
}

func (h *MCHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}
	return nil, nil
}

func (h *MCHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	return nil, nil
}

func (h *MCHandler) Filecmd(r *sftp.Request) error {
	return nil
}

func (h *MCHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	return nil, nil
}
