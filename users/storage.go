package users

import (
	"errors"
	"slices"
	"sync"
	"time"

	fberrors "github.com/werewolfboy13/filebrowser/v2/errors"
)

// StorageBackend is the interface to implement for a users storage.
type StorageBackend interface {
	GetBy(interface{}) (*User, error)
	GetByScope(scope string) (*User, error)
	Gets() ([]*User, error)
	Save(u *User) error
	Update(u *User, fields ...string) error
	DeleteByID(uint) error
	DeleteByUsername(string) error
	CountAdmins() (int, error)
}

type Store interface {
	Get(baseScope string, followExternalSymlinks bool, id interface{}) (user *User, err error)
	GetByScope(scope string) (*User, error)
	Gets(baseScope string, followExternalSymlinks bool) ([]*User, error)
	Update(user *User, fields ...string) error
	UpdateIfMark(user *User, mark int64, revoke bool, fields ...string) (*User, error)
	Save(user *User) error
	SaveProvisioned(user *User, derivedScope bool) error
	Delete(id interface{}) error
	LastUpdate(id uint) int64
}

// Storage is a users storage.
type Storage struct {
	back    StorageBackend
	updated map[uint]int64
	mux     sync.RWMutex

	// provision serializes the scope-collision check and the save of newly
	// provisioned users, which must not interleave. See SaveProvisioned.
	provision sync.Mutex

	// write is held by everything that changes a stored user, from the
	// moment it looks at the record that is stored to the moment its own is:
	// Update, UpdateIfMark, Save and Delete. What they decide on that record
	// (which revocation mark to keep, whether it is still the one the caller
	// read) holds until they have written, because nothing else can write
	// in between. Where both are taken, provision is taken first.
	//
	// It only orders the writers of this process. There is one Storage per
	// process, and the database is opened by one process at a time.
	write sync.Mutex

	// now is the clock revocation marks are worked out with.
	now func() time.Time
}

// NewStorage creates a users storage from a backend.
func NewStorage(back StorageBackend) *Storage {
	return &Storage{
		back:    back,
		updated: map[uint]int64{},
		now:     time.Now,
	}
}

// Get allows you to get a user by its name or username. The provided
// id must be a string for username lookup or a uint for id lookup. If id
// is neither, a ErrInvalidDataType will be returned.
func (s *Storage) Get(baseScope string, followExternalSymlinks bool, id interface{}) (user *User, err error) {
	user, err = s.back.GetBy(id)
	if err != nil {
		return
	}
	if err := user.Clean(baseScope, followExternalSymlinks); err != nil {
		return nil, err
	}
	return
}

// GetByScope returns the first user whose scope matches the given one, or
// ErrNotExist if none does. The user is returned as stored, without setting up
// its filesystem, as it is meant for existence checks rather than serving.
func (s *Storage) GetByScope(scope string) (*User, error) {
	return s.back.GetByScope(scope)
}

// Gets gets a list of all users.
func (s *Storage) Gets(baseScope string, followExternalSymlinks bool) ([]*User, error) {
	users, err := s.back.Gets()
	if err != nil {
		return nil, err
	}

	for _, user := range users {
		if err := user.Clean(baseScope, followExternalSymlinks); err != nil {
			return nil, err
		}
	}

	return users, err
}

// keepRevocation carries the revocation mark of the stored record over to
// user when user is about to replace it and has an older one, or none. A
// record is rewritten as a whole from many places (the API, the CLI, hook
// authentication, imports), with a user built from input that knows nothing
// of the mark, and none of them may bring revoked sessions back.
//
// It is called with write held.
func (s *Storage) keepRevocation(user *User) {
	if user.ID == 0 {
		return
	}
	if stored, err := s.back.GetBy(user.ID); err == nil && stored.TokensNotBefore > user.TokensNotBefore {
		user.TokensNotBefore = stored.TokensNotBefore
	}
}

// touch notes that the user was updated just now: see LastUpdate.
func (s *Storage) touch(id uint) {
	s.mux.Lock()
	s.updated[id] = time.Now().Unix()
	s.mux.Unlock()
}

// Update updates a user in the database: the fields that are named, or the
// whole record when none is.
//
// It stores what it is given, whatever was stored since the caller read the
// record. That is right for a change of fields nobody else decides on (a
// display preference). A change that ends the sessions of the user, and one
// that writes back a record the caller read, go through UpdateIfMark, which
// refuses when the record was changed in the meantime.
//
// The revocation mark never goes back, and a revocation never leaves it
// where it is. A whole record keeps the mark that is stored when its own is
// older. An update that names TokensNotBefore is a revocation: when the mark
// it brings is not above the stored one, it is stored as one above. Two
// revocations that were worked out from the same record in the same second
// would otherwise store the same mark, and the tokens issued after the first
// would survive the second.
func (s *Storage) Update(user *User, fields ...string) error {
	err := user.Clean("", false, fields...)
	if err != nil {
		return err
	}

	s.write.Lock()
	defer s.write.Unlock()

	if len(fields) == 0 {
		s.keepRevocation(user)
	} else if slices.Contains(fields, "TokensNotBefore") {
		if stored, err := s.back.GetBy(user.ID); err == nil && stored.TokensNotBefore >= user.TokensNotBefore {
			user.TokensNotBefore = stored.TokensNotBefore + 1
		}
	}

	err = s.back.Update(user, fields...)
	if err != nil {
		return err
	}

	s.touch(user.ID)
	return nil
}

// UpdateIfMark is Update for a change that was decided on a record the
// caller read a while ago: it only stores the change when the stored record
// still carries mark, the revocation mark (TokensNotBefore) of the record
// that was read. Otherwise it stores nothing and returns
// fberrors.ErrUserChanged.
//
// The mark moves with every change that ends the sessions of the user: a new
// password, other permissions, another scope. A caller that read the record
// before such a change has decided on an account that is no longer the one
// that is stored. Its password was checked against a hash that was replaced,
// its copy of the record has the permissions that were just taken away. If
// it stored its change all the same, it would undo the other one, and the
// revocation that came with it: a user's own change of password, in flight
// while an administrator resets the password, used to put the user's
// password back and hand the user a valid session. The caller is told
// instead, and can read the record again and decide again, or give up.
//
// With revoke, the change ends the sessions of the user itself: the mark is
// moved on, in the same step, to one that is above the stored one and not
// before the next second (see User.RevokeTokens). Because the stored mark is
// read and written under one lock, no two revocations ever store the same
// mark, whenever they were worked out. Without revoke, the mark stays as it
// is stored.
//
// Either way the mark of user is the server's to set: whatever user carries
// is replaced, and after the call user has the mark that was stored. A
// record written before the mark existed has the mark zero, and is compared
// like any other.
//
// It returns the record as it is stored once the change is in, read back
// before anything else could change it: what a session token for the
// account as this change left it is issued from. Like the result of
// GetByScope, it has no filesystem set up.
func (s *Storage) UpdateIfMark(user *User, mark int64, revoke bool, fields ...string) (*User, error) {
	if err := user.Clean("", false, fields...); err != nil {
		return nil, err
	}

	s.write.Lock()
	defer s.write.Unlock()

	stored, err := s.back.GetBy(user.ID)
	if err != nil {
		return nil, err
	}
	if stored.TokensNotBefore != mark {
		return nil, fberrors.ErrUserChanged
	}

	user.TokensNotBefore = stored.TokensNotBefore
	if revoke {
		user.RevokeTokens(s.now())
		if len(fields) != 0 && !slices.Contains(fields, "TokensNotBefore") {
			fields = append(fields, "TokensNotBefore")
		}
	}

	if err := s.back.Update(user, fields...); err != nil {
		return nil, err
	}
	s.touch(user.ID)

	return s.back.GetBy(user.ID)
}

// Save saves the user in a storage.
func (s *Storage) Save(user *User) error {
	if err := user.Clean("", false); err != nil {
		return err
	}

	s.write.Lock()
	defer s.write.Unlock()

	s.keepRevocation(user)

	return s.back.Save(user)
}

// SaveProvisioned saves a user that is being provisioned (via signup, proxy
// auth or hook auth). When its scope was derived from the username, it first
// rejects the save if another user already owns that scope, so that distinct
// usernames cannot silently share one home directory.
//
// The check and the save are held under a single lock. Performing them as two
// independent operations lets two concurrent provisioning requests both observe
// a free scope and both save, leaving two users sharing one home directory.
func (s *Storage) SaveProvisioned(user *User, derivedScope bool) error {
	if !derivedScope {
		return s.Save(user)
	}

	s.provision.Lock()
	defer s.provision.Unlock()

	switch _, err := s.back.GetByScope(user.Scope); {
	case err == nil:
		return fberrors.ErrExist
	case !errors.Is(err, fberrors.ErrNotExist):
		return err
	}

	return s.Save(user)
}

// Delete allows you to delete a user by its name or username. The provided
// id must be a string for username lookup or a uint for id lookup. If id
// is neither, a ErrInvalidDataType will be returned.
func (s *Storage) Delete(id interface{}) error {
	// Under the lock of the writers: an update that has looked at the
	// record must not store it after it was deleted.
	s.write.Lock()
	defer s.write.Unlock()

	switch id := id.(type) {
	case string:
		user, err := s.back.GetBy(id)
		if err != nil {
			return err
		}
		if s.IsUniqueAdmin(user) {
			return fberrors.ErrRootUserDeletion
		}

		return s.back.DeleteByUsername(id)
	case uint:
		user, err := s.back.GetBy(id)
		if err != nil {
			return err
		}
		if s.IsUniqueAdmin(user) {
			return fberrors.ErrRootUserDeletion
		}

		return s.back.DeleteByID(id)
	default:
		return fberrors.ErrInvalidDataType
	}
}

// LastUpdate gets the timestamp for the last update of an user.
func (s *Storage) LastUpdate(id uint) int64 {
	s.mux.RLock()
	defer s.mux.RUnlock()
	if val, ok := s.updated[id]; ok {
		return val
	}
	return 0
}

func (s *Storage) IsUniqueAdmin(user *User) bool {
	if !user.Perm.Admin {
		return false
	}

	count, err := s.back.CountAdmins()
	if err != nil {
		return true
	}
	return count <= 1
}
