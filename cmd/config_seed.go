package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const (
	// envConfig names the configuration file to use when --config is not given.
	envConfig = "FB_CONFIG"
	// envConfigTemplate names a file that is copied to the envConfig path when
	// no file exists there yet.
	envConfigTemplate = "FB_CONFIG_TEMPLATE"

	seededConfigPermissions = 0644
)

// seedConfigFile copies the template to path when path does not exist. It
// does nothing when either name is empty or when path already exists, so an
// existing configuration is never overwritten.
//
// The Docker image starts the binary directly, with no shell script before
// it, and this is how it puts its default settings in the /config volume on
// the first start.
func seedConfigFile(path, template string) (err error) {
	if path == "" || template == "" {
		return nil
	}

	if _, err = os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("can't check config file %s: %w", path, err)
	}

	src, err := os.Open(template)
	if err != nil {
		return fmt.Errorf("can't read config template: %w", err)
	}
	defer src.Close()

	// O_EXCL, so a file that another process created in the meantime is kept.
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, seededConfigPermissions)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("can't create config file from %s: %w", template, err)
	}

	if _, err = io.Copy(dst, src); err != nil {
		dst.Close()
		// Half a configuration file would stop every later start.
		os.Remove(path)
		return fmt.Errorf("can't write config file %s: %w", path, err)
	}

	if err = dst.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("can't write config file %s: %w", path, err)
	}

	return nil
}
