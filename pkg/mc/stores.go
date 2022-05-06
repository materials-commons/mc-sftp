package mc

import (
	"github.com/materials-commons/gomcdb/store"
	"gorm.io/gorm"
)

// Stores is a place to consolidate the various stores that are used by the handlers. It
// allows the stores to be easily created and cleans up the number of parameters that need
// to be passed in to create a mcscp.Handler or mcsftp.Handler.
type Stores struct {
	FileStore       store.FileStore
	ProjectStore    store.ProjectStore
	ConversionStore store.ConversionStore
}

func NewGormStores(db *gorm.DB, mcfsRoot string) *Stores {
	return &Stores{
		FileStore:       store.NewGormFileStore(db, mcfsRoot),
		ProjectStore:    store.NewGormProjectStore(db),
		ConversionStore: store.NewGormConversionStore(db),
	}
}
