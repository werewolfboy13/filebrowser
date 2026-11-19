package storage

import (
	"github.com/werewolfboy13/filebrowser/v3/auth"
	"github.com/werewolfboy13/filebrowser/v3/settings"
	"github.com/werewolfboy13/filebrowser/v3/share"
	"github.com/werewolfboy13/filebrowser/v3/users"
)

// Storage is a storage powered by a Backend which makes the necessary
// verifications when fetching and saving data to ensure consistency.
type Storage struct {
	Users    users.Store
	Share    *share.Storage
	Auth     *auth.Storage
	Settings *settings.Storage
}
