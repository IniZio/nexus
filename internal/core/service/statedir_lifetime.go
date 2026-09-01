package service

import (
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/statedir"
	"github.com/IniZio/nexus3/internal/core/store"
)

// removeSupervisorStateDir deletes <storeRoot>/supervisors/<id>, the durable
// per-sandbox supervisor state directory, as part of Service.Remove.
//
// The store root is resolved through store.DefaultRoot rather than carried on
// the Service because that is already how startSupervisor locates the same
// directory to write the egress decisions log into it; keeping both ends on
// the same resolver means a test that redirects XDG_STATE_HOME redirects the
// create side and the teardown side together.
//
// Idempotent: os.RemoveAll treats a missing directory as success.
func removeSupervisorStateDir(id domain.SandboxID) error {
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	return statedir.Remove(statedir.SupervisorDir(root, id))
}
