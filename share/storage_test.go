package share

import (
	"testing"
	"time"
)

// memoryBackend is a minimal StorageBackend holding links in a map.
type memoryBackend struct {
	links map[string]*Link
	order []string
}

func newMemoryBackend(links ...*Link) *memoryBackend {
	b := &memoryBackend{links: map[string]*Link{}}
	for _, l := range links {
		b.links[l.Hash] = l
		b.order = append(b.order, l.Hash)
	}
	return b
}

func (b *memoryBackend) all(keep func(*Link) bool) []*Link {
	var out []*Link
	for _, hash := range b.order {
		if l, ok := b.links[hash]; ok && keep(l) {
			out = append(out, l)
		}
	}
	return out
}

func (b *memoryBackend) All() ([]*Link, error) {
	return b.all(func(*Link) bool { return true }), nil
}

func (b *memoryBackend) FindByUserID(id uint) ([]*Link, error) {
	return b.all(func(l *Link) bool { return l.UserID == id }), nil
}

func (b *memoryBackend) GetByHash(hash string) (*Link, error) { return b.links[hash], nil }

func (b *memoryBackend) GetPermanent(string, uint) (*Link, error) { return nil, nil }

func (b *memoryBackend) Gets(path string, id uint) ([]*Link, error) {
	return b.all(func(l *Link) bool { return l.Path == path && l.UserID == id }), nil
}

func (b *memoryBackend) Save(l *Link) error { b.links[l.Hash] = l; return nil }

func (b *memoryBackend) Delete(hash string) error { delete(b.links, hash); return nil }

func (b *memoryBackend) DeleteWithPathPrefix(string, uint) error { return nil }

// The listing methods prune expired links. They used to delete from the slice
// they were ranging over, which panicked with two expired links in a row and
// otherwise skipped (and so returned) the link following a removed one.
func TestListingsPruneExpiredLinks(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	future := time.Now().Add(time.Hour).Unix()

	listings := map[string]func(*Storage) ([]*Link, error){
		"All":          func(s *Storage) ([]*Link, error) { return s.All() },
		"FindByUserID": func(s *Storage) ([]*Link, error) { return s.FindByUserID(1) },
		"Gets":         func(s *Storage) ([]*Link, error) { return s.Gets("/f", 1) },
	}

	cases := map[string]struct {
		expires []int64
		want    []string
	}{
		"two expired":                 {[]int64{past, past}, nil},
		"expired, expired, permanent": {[]int64{past, past, 0}, []string{"h2"}},
		"expired between valid ones":  {[]int64{future, past, past, 0, past}, []string{"h0", "h3"}},
		"nothing expired":             {[]int64{0, future}, []string{"h0", "h1"}},
	}

	for listing, list := range listings {
		for name, tc := range cases {
			t.Run(listing+"/"+name, func(t *testing.T) {
				var links []*Link
				for i, expire := range tc.expires {
					links = append(links, &Link{Hash: "h" + string(rune('0'+i)), Path: "/f", UserID: 1, Expire: expire})
				}
				back := newMemoryBackend(links...)

				got, err := list(NewStorage(back))
				if err != nil {
					t.Fatal(err)
				}

				var hashes []string
				for _, l := range got {
					hashes = append(hashes, l.Hash)
				}
				if len(hashes) != len(tc.want) {
					t.Fatalf("returned %v, want %v", hashes, tc.want)
				}
				for i := range hashes {
					if hashes[i] != tc.want[i] {
						t.Fatalf("returned %v, want %v", hashes, tc.want)
					}
				}
				if len(back.links) != len(tc.want) {
					t.Errorf("%d links left in the backend, want %d", len(back.links), len(tc.want))
				}
			})
		}
	}
}
