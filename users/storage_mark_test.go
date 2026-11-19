package users

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fberrors "github.com/werewolfboy13/filebrowser/v2/errors"
)

// memoryBackend is a StorageBackend that keeps users in a map, and stores
// copies, as a database does. Each call can be held up by a test: see hold.
type memoryBackend struct {
	mu    sync.Mutex
	users map[uint]*User

	// before, when set, is called at the start of every call with its name,
	// outside the lock of the backend.
	before atomic.Pointer[func(call string)]
}

func newMemoryBackend(list ...*User) *memoryBackend {
	b := &memoryBackend{users: map[uint]*User{}}
	for _, u := range list {
		c := *u
		b.users[u.ID] = &c
	}
	return b
}

func (b *memoryBackend) enter(call string) {
	if f := b.before.Load(); f != nil {
		(*f)(call)
	}
}

func (b *memoryBackend) GetBy(id interface{}) (*User, error) {
	b.enter("GetBy")
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, u := range b.users {
		if id == interface{}(u.ID) || id == interface{}(u.Username) {
			c := *u
			return &c, nil
		}
	}
	return nil, fberrors.ErrNotExist
}

func (b *memoryBackend) GetByScope(string) (*User, error) { return nil, fberrors.ErrNotExist }
func (b *memoryBackend) Gets() ([]*User, error)           { return nil, nil }
func (b *memoryBackend) CountAdmins() (int, error)        { return 2, nil }

func (b *memoryBackend) Save(u *User) error {
	b.enter("Save")
	b.mu.Lock()
	defer b.mu.Unlock()
	c := *u
	b.users[u.ID] = &c
	return nil
}

func (b *memoryBackend) Update(u *User, fields ...string) error {
	if len(fields) == 0 {
		return b.Save(u)
	}
	b.enter("Update")
	b.mu.Lock()
	defer b.mu.Unlock()
	stored, ok := b.users[u.ID]
	if !ok {
		return fberrors.ErrNotExist
	}
	for _, field := range fields {
		reflect.ValueOf(stored).Elem().FieldByName(field).Set(reflect.ValueOf(u).Elem().FieldByName(field))
	}
	return nil
}

func (b *memoryBackend) DeleteByID(id uint) error {
	b.enter("Delete")
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.users, id)
	return nil
}

func (b *memoryBackend) DeleteByUsername(string) error { return fberrors.ErrInvalidDataType }

func (b *memoryBackend) stored(t *testing.T, id uint) *User {
	t.Helper()
	u, err := b.GetBy(id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func bobRecord() *User {
	return &User{ID: 2, Username: "bob", Password: "hash-0", Scope: "/users/bob", Locale: "en", Perm: Permissions{Create: true, Delete: true}}
}

// A change is only stored when the record still carries the revocation mark
// of the record the change was decided on.
func TestUpdateIfMarkRefusesARecordThatWasRevokedSince(t *testing.T) {
	back := newMemoryBackend(bobRecord())
	store := NewStorage(back)

	// Two requests read the record.
	first, second := back.stored(t, 2), back.stored(t, 2)

	// The first, an administrator's reset, is stored.
	first.Password = "hash-admin"
	got, err := store.UpdateIfMark(first, first.TokensNotBefore, true, "Password")
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "hash-admin" || got.TokensNotBefore == 0 || got.TokensNotBefore != first.TokensNotBefore {
		t.Fatalf("the record returned is %+v, and the user was left with mark %d", got, first.TokensNotBefore)
	}
	if got.Locale != "en" || !got.Perm.Delete {
		t.Errorf("the record returned is not the whole record: %+v", got)
	}

	// The second, the user's own change, was decided on the record of
	// before. It must not undo the reset, with or without a revocation,
	// as named fields or as a whole record.
	for name, change := range map[string]func() (*User, error){
		"revoking":     func() (*User, error) { return store.UpdateIfMark(second, 0, true, "Password") },
		"not revoking": func() (*User, error) { return store.UpdateIfMark(second, 0, false, "Password") },
		"whole record": func() (*User, error) { return store.UpdateIfMark(second, 0, false) },
	} {
		second.Password = "hash-bob"
		if u, err := change(); !errors.Is(err, fberrors.ErrUserChanged) || u != nil {
			t.Errorf("%s: a change decided on the record of before the reset: %v, %v; want ErrUserChanged", name, u, err)
		}
		if stored := back.stored(t, 2); stored.Password != "hash-admin" || stored.TokensNotBefore != got.TokensNotBefore {
			t.Fatalf("%s: the refused change was stored: %+v", name, stored)
		}
	}

	// Read again, it goes through, and moves the mark on.
	again := back.stored(t, 2)
	again.Password = "hash-bob"
	after, err := store.UpdateIfMark(again, again.TokensNotBefore, true, "Password")
	if err != nil {
		t.Fatal(err)
	}
	if after.TokensNotBefore <= got.TokensNotBefore {
		t.Errorf("the mark went from %d to %d", got.TokensNotBefore, after.TokensNotBefore)
	}
}

// Two revocations never store the same mark, whenever they are worked out:
// a token issued after the first would be good after the second.
func TestRevocationMarksStrictlyIncrease(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	revocations := map[string]func(store *Storage, back *memoryBackend) error{
		"compared with the mark that was read": func(store *Storage, back *memoryBackend) error {
			u, _ := back.GetBy(uint(2))
			_, err := store.UpdateIfMark(u, u.TokensNotBefore, true, "Password")
			return err
		},
		"compared, whole record": func(store *Storage, back *memoryBackend) error {
			u, _ := back.GetBy(uint(2))
			_, err := store.UpdateIfMark(u, u.TokensNotBefore, true)
			return err
		},
		"named field": func(store *Storage, back *memoryBackend) error {
			u, _ := back.GetBy(uint(2))
			u.RevokeTokens(now)
			return store.Update(u, "TokensNotBefore")
		},
		// Worked out from a record that was read before another revocation
		// was stored, in the same second: the mark it brings is the one
		// that is stored by now.
		"named field, from a record read earlier": func(store *Storage, _ *memoryBackend) error {
			u := bobRecord()
			u.RevokeTokens(now)
			return store.Update(u, "TokensNotBefore")
		},
	}

	for first, revokeFirst := range revocations {
		for second, revokeSecond := range revocations {
			back := newMemoryBackend(bobRecord())
			store := NewStorage(back)
			store.now = func() time.Time { return now } // the same second, for both

			if err := revokeFirst(store, back); err != nil {
				t.Fatal(err)
			}
			one := back.stored(t, 2).TokensNotBefore
			if one != now.Unix()+1 {
				t.Errorf("%s: the first revocation stored mark %d, want %d", first, one, now.Unix()+1)
			}
			if err := revokeSecond(store, back); err != nil {
				t.Fatal(err)
			}
			if two := back.stored(t, 2).TokensNotBefore; two <= one {
				t.Errorf("%s, then %s in the same second: marks %d and %d", first, second, one, two)
			}
		}
	}
}

// A record written before the mark existed has none. It is updated like any
// other, and a change that does not revoke leaves it without one.
func TestUpdateIfMarkOnARecordWithoutAMark(t *testing.T) {
	back := newMemoryBackend(bobRecord())
	store := NewStorage(back)

	u := back.stored(t, 2)
	u.Locale = "fr"
	u.TokensNotBefore = 12345 // not the caller's to set
	got, err := store.UpdateIfMark(u, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Locale != "fr" || got.TokensNotBefore != 0 || u.TokensNotBefore != 0 {
		t.Errorf("stored %+v; the user was left with mark %d", got, u.TokensNotBefore)
	}

	// Neither does a plain update bring a mark of its own making down.
	v := back.stored(t, 2)
	v.RevokeTokens(time.Now())
	if err := store.Update(v); err != nil {
		t.Fatal(err)
	}
	stale := bobRecord()
	if err := store.Update(stale); err != nil {
		t.Fatal(err)
	}
	if got := back.stored(t, 2).TokensNotBefore; got != v.TokensNotBefore {
		t.Errorf("a whole record without a mark took the stored mark from %d to %d", v.TokensNotBefore, got)
	}
}

// A user that was deleted is not brought back by a change that was under
// way.
func TestUpdateIfMarkOfADeletedUser(t *testing.T) {
	back := newMemoryBackend(bobRecord())
	store := NewStorage(back)

	u := back.stored(t, 2)
	if err := store.Delete(uint(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateIfMark(u, 0, true); !errors.Is(err, fberrors.ErrNotExist) {
		t.Errorf("error %v, want ErrNotExist", err)
	}
	if _, err := back.GetBy(uint(2)); !errors.Is(err, fberrors.ErrNotExist) {
		t.Error("the deleted user is back")
	}
}

// Everything that writes a user waits for a compare-and-swap that is under
// way: between its look at the stored record and its write, nothing else
// gets to the database.
func TestWritersDoNotInterleaveWithACompareAndSwap(t *testing.T) {
	writers := map[string]func(store *Storage) error{
		"Update of a field": func(store *Storage) error {
			return store.Update(&User{ID: 2, Locale: "de"}, "Locale")
		},
		"Update of the mark": func(store *Storage) error {
			u := bobRecord()
			u.RevokeTokens(time.Now())
			return store.Update(u, "TokensNotBefore")
		},
		"Update of the record": func(store *Storage) error { return store.Update(bobRecord()) },
		"Save":                 func(store *Storage) error { return store.Save(bobRecord()) },
		"SaveProvisioned":      func(store *Storage) error { return store.SaveProvisioned(bobRecord(), false) },
		"Delete":               func(store *Storage) error { return store.Delete(uint(2)) },
		"UpdateIfMark": func(store *Storage) error {
			_, err := store.UpdateIfMark(bobRecord(), 0, true, "Password")
			if errors.Is(err, fberrors.ErrUserChanged) {
				return nil // as it should be: the other one was first
			}
			return err
		},
	}

	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			back := newMemoryBackend(bobRecord())
			store := NewStorage(back)

			// The compare-and-swap is held where it has read the stored
			// record and is about to write.
			inside := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32 // calls of the backend by the other writer
			var held atomic.Bool
			before := func(call string) {
				if call == "Update" && held.CompareAndSwap(false, true) {
					close(inside)
					<-release
					return
				}
				if held.Load() {
					calls.Add(1)
				}
			}
			back.before.Store(&before)

			casDone := make(chan error, 1)
			go func() {
				u := bobRecord()
				u.Password = "hash-cas"
				_, err := store.UpdateIfMark(u, 0, true, "Password")
				casDone <- err
			}()
			<-inside

			writeDone := make(chan error, 1)
			go func() { writeDone <- write(store) }()

			// The other writer must not get anywhere while the first is
			// held. There is nothing to wait for but time.
			select {
			case err := <-writeDone:
				t.Fatalf("%s finished (%v) while a compare-and-swap was between its read and its write", name, err)
			case <-time.After(100 * time.Millisecond):
			}
			if n := calls.Load(); n != 0 {
				t.Fatalf("%s reached the database %d times while a compare-and-swap was between its read and its write", name, n)
			}

			close(release)
			if err := <-casDone; err != nil {
				t.Fatalf("compare-and-swap: %v", err)
			}
			if err := <-writeDone; err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
	}
}

// Many revocations of one account at once, all decided on the same record:
// one is stored, the others are told. Meant to be run with the race
// detector.
func TestConcurrentRevocationsOfOneRecord(t *testing.T) {
	back := newMemoryBackend(bobRecord())
	store := NewStorage(back)

	const n = 32
	var wg sync.WaitGroup
	var stored, refused atomic.Int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := bobRecord()
			<-start
			switch _, err := store.UpdateIfMark(u, 0, true, "Password"); {
			case err == nil:
				stored.Add(1)
			case errors.Is(err, fberrors.ErrUserChanged):
				refused.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if stored.Load() != 1 || refused.Load() != n-1 {
		t.Errorf("%d revocations decided on one record: %d stored, %d refused; want 1 and %d", n, stored.Load(), refused.Load(), n-1)
	}
}
