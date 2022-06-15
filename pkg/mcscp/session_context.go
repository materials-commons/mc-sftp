package mcscp

import "github.com/materials-commons/gomcdb/mcmodel"

// SessionContext is the context for a scp session. It contains the user that started the session
// as well as the project (determined from the slug in the project path).
type SessionContext struct {
	// The user is set in the context from the passwordHandler method in cmd/mc-sshd/cmd/root. Rather than
	// constantly retrieving it we get it one time and set it in the mcfsHandler. See
	// loadProjectAndUserIntoHandler for details.
	user *mcmodel.User

	// The project that this scp instance is using. It gets loaded from the path the user specified. See
	// loadProjectAndUserIntoHandler and pkg/mc/util mc.*ProjectSlug* methods for how this is handled.
	project *mcmodel.Project

	// Each callback has to attempt to load the project. The project gets loaded once into the context
	// loadProjectIntoSessionContext. However, it's possible that the project is invalid. If this
	// happens then fatalErrorLoadingProject is set to true so that an attempt isn't made to
	// load the project again.
	fatalErrorLoadingProject bool
}

// NewSessionContext creates a new SessionContext. The user is a required parameter and cannot be nil.
func NewSessionContext(user *mcmodel.User) *SessionContext {
	return &SessionContext{
		user:                     user,
		fatalErrorLoadingProject: false,
	}
}
