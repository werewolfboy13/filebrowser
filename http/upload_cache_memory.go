package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

const uploadCacheTTL = 3 * time.Minute

var errNoActiveUpload = errors.New("no active upload found for the given path")

// uploadInfo is what is kept about an upload in progress.
type uploadInfo struct {
	// Length is the size the upload was announced with.
	Length int64

	// Override tells whether the upload was created asking to replace what
	// is at its destination. The requests that carry the data do not say, and
	// the destination is only written when the last of them arrives.
	Override bool

	// Owner is the ID of the user who created the upload, who may cancel
	// it. It is 0 when that is not known.
	Owner uint

	// Direct tells that the upload writes to its destination itself. Every
	// other upload writes to a temporary file, and only ever to that: see
	// tusWritePath. An upload of which it is not known (one registered by a
	// version that did not say) is taken to write to a temporary file, so
	// that it is never the destination that is written to by mistake.
	Direct bool
}

// UploadCache is an interface for tracking active uploads.
// Allows for different backends (e.g. in-memory or redis)
// to support both single instance and multi replica deployments.
type UploadCache interface {
	// Register stores an upload. remove is called if the upload expires
	// before completion, to delete the partial file; it must route through
	// the uploading user's scoped filesystem so that eviction cannot follow a
	// symlink out of the user's scope. It is nil when the upload has nothing
	// of its own to delete.
	Register(filePath string, upload uploadInfo, remove func() error)

	// Complete removes an upload from the cache
	Complete(filePath string)

	// Get returns what was registered for an active upload. Looking does
	// not keep the upload alive: that is Touch, for the requests that carry
	// it on. Anybody may ask about an upload, and only its owner's requests
	// are a sign of life.
	Get(filePath string) (uploadInfo, error)

	// GetLength returns the expected file size for an active upload
	GetLength(filePath string) (int64, error)

	// Has reports whether an upload is registered. When the cache cannot
	// tell, it says that there is one.
	Has(filePath string) bool

	// Touch refreshes the TTL for an active upload
	Touch(filePath string)

	// Close cleans up any resources
	Close()
}

// memoryUploadEntry is the value stored for each active upload.
type memoryUploadEntry struct {
	upload uploadInfo
	remove func() error
}

// memoryUploadCache is an upload cache for single replica deployments
type memoryUploadCache struct {
	cache *ttlcache.Cache[string, memoryUploadEntry]
}

func newMemoryUploadCache() *memoryUploadCache {
	cache := ttlcache.New[string, memoryUploadEntry]()
	cache.OnEviction(func(_ context.Context, reason ttlcache.EvictionReason, item *ttlcache.Item[string, memoryUploadEntry]) {
		if reason == ttlcache.EvictionReasonExpired {
			fmt.Printf("deleting incomplete upload file: \"%s\"\n", item.Key())
			// Delete through the scoped removal callback rather than a raw
			// os.Remove on the cached path, so an ancestor directory swapped for
			// a symlink during the TTL window cannot redirect the delete outside
			// the user's scope.
			if remove := item.Value().remove; remove != nil {
				if err := remove(); err != nil {
					fmt.Printf("failed to delete incomplete upload file %q: %v\n", item.Key(), err)
				}
			}
		}
	})
	go cache.Start()

	return &memoryUploadCache{cache: cache}
}

func (c *memoryUploadCache) Register(filePath string, upload uploadInfo, remove func() error) {
	c.cache.Set(filePath, memoryUploadEntry{upload: upload, remove: remove}, uploadCacheTTL)
}

func (c *memoryUploadCache) Complete(filePath string) {
	c.cache.Delete(filePath)
}

func (c *memoryUploadCache) Get(filePath string) (uploadInfo, error) {
	item := c.cache.Get(filePath, ttlcache.WithDisableTouchOnHit[string, memoryUploadEntry]())
	if item == nil {
		return uploadInfo{}, errNoActiveUpload
	}
	return item.Value().upload, nil
}

func (c *memoryUploadCache) GetLength(filePath string) (int64, error) {
	upload, err := c.Get(filePath)
	return upload.Length, err
}

func (c *memoryUploadCache) Has(filePath string) bool {
	return c.cache.Has(filePath)
}

func (c *memoryUploadCache) Touch(filePath string) {
	c.cache.Touch(filePath)
}

func (c *memoryUploadCache) Close() {
	c.cache.Stop()
}

// NewUploadCache creates a new upload cache.
// If redisURL is empty, an in-memory cache will be used (suitable for single instance deployments).
// Otherwise, Redis will be used for the cache (suitable for multi-instance deployments).
// The redisURL can include credentials, e.g. redis://user:pass@host:port
func NewUploadCache(redisURL string) (UploadCache, error) {
	if redisURL != "" {
		return newRedisUploadCache(redisURL)
	}
	return newMemoryUploadCache(), nil
}
