package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// redisUploadCache is an upload cache for multi replica deployments
type redisUploadCache struct {
	client *redis.Client
}

func newRedisUploadCache(redisURL string) (*redisUploadCache, error) {
	if redisURL == "" {
		return nil, fmt.Errorf("redis URL is required")
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	// Test connection
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &redisUploadCache{client: client}, nil
}

func (c *redisUploadCache) filePathKey(filePath string) string {
	return "filebrowser:upload:" + filePath
}

// Register stores the upload. The scoped removal callback is unused by the
// redis backend: nothing runs when a key expires. The temporary file of an
// abandoned upload is removed by tusTemps instead.
func (c *redisUploadCache) Register(filePath string, upload uploadInfo, _ func() error) {
	err := c.client.Set(context.Background(), c.filePathKey(filePath), encodeUploadInfo(upload), uploadCacheTTL).Err()
	if err != nil {
		log.Printf("failed to register upload in redis cache: %v", err)
	}
}

func (c *redisUploadCache) Complete(filePath string) {
	err := c.client.Del(context.Background(), c.filePathKey(filePath)).Err()
	if err != nil {
		log.Printf("failed to complete upload in redis cache: %v", err)
	}
}

func (c *redisUploadCache) Get(filePath string) (uploadInfo, error) {
	result, err := c.client.Get(context.Background(), c.filePathKey(filePath)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return uploadInfo{}, errNoActiveUpload
		}
		return uploadInfo{}, fmt.Errorf("redis error: %w", err)
	}

	upload, err := decodeUploadInfo(result)
	if err != nil {
		return uploadInfo{}, err
	}

	c.Touch(filePath)

	return upload, nil
}

func (c *redisUploadCache) GetLength(filePath string) (int64, error) {
	upload, err := c.Get(filePath)
	return upload.Length, err
}

// encodeUploadInfo writes an upload as the value kept in redis: its length,
// followed by ":" and a field for each thing that is more than the length
// alone ("o" for an override, "u" and the ID of the owner). An upload
// without any is the bare number this cache has always stored. Fields that
// are not known are skipped when reading, so that more can be added.
func encodeUploadInfo(upload uploadInfo) string {
	value := strconv.FormatInt(upload.Length, 10)
	if upload.Override {
		value += ":o"
	}
	if upload.Owner != 0 {
		value += ":u" + strconv.FormatUint(uint64(upload.Owner), 10)
	}
	return value
}

func decodeUploadInfo(value string) (uploadInfo, error) {
	fields := strings.Split(value, ":")

	length, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return uploadInfo{}, fmt.Errorf("invalid upload length in cache: %w", err)
	}

	upload := uploadInfo{Length: length}
	for _, field := range fields[1:] {
		switch {
		case field == "o":
			upload.Override = true
		case strings.HasPrefix(field, "u"):
			if owner, err := strconv.ParseUint(field[1:], 10, 0); err == nil {
				upload.Owner = uint(owner)
			}
		}
	}
	return upload, nil
}

func (c *redisUploadCache) Touch(filePath string) {
	err := c.client.Expire(context.Background(), c.filePathKey(filePath), uploadCacheTTL).Err()
	if err != nil {
		log.Printf("failed to touch upload in redis cache: %v", err)
	}
}

func (c *redisUploadCache) Close() {
	c.client.Close()
}
