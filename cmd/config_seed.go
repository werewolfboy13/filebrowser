package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/viper"
)

const (
	// envConfig names the configuration file to use when --config is not given.
	envConfig = "FB_CONFIG"
	// envConfigTemplate names a file that is copied to the envConfig path when
	// no settings are there yet.
	envConfigTemplate = "FB_CONFIG_TEMPLATE"

	seededConfigPermissions = 0644
)

// seedConfigFile copies the template to path when there are no settings at
// path yet: nothing is there, or an empty file. It does nothing when either
// name is empty or when path holds anything, so an existing configuration is
// never overwritten.
//
// The Docker image starts the binary directly, with no shell script before
// it, and this is how it puts its default settings in the /config volume on
// the first start.
//
// An empty file is filled, and not refused. It is what a first start that
// was interrupted left behind when the file was still created first and
// filled second, and what `touch settings.json` makes before the file is
// mounted in the container by itself. It holds nothing that could be lost,
// as JSON it stops every start, and the image has no shell to remove it with.
//
// The template is only copied to a file of its own format, which the
// extension names, and only when that is a format the settings are read in:
// as anything else it could not be read, and it would stop every later start.
func seedConfigFile(path, template string) error {
	if path == "" || template == "" {
		return nil
	}

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("can't check config file %s: %w", path, err)
	case !info.Mode().IsRegular() || info.Size() != 0:
		return nil
	}
	empty := err == nil

	format, readable := configFormat(path)
	if templateFormat, _ := configFormat(template); !readable || format != templateFormat {
		log.Printf("WARNING: %s does not get the settings in %s: going by their extensions,", path, template)
		log.Printf("WARNING: the two are not in the same format, or not in one that File Browser reads.")
		return nil
	}

	content, err := os.ReadFile(template)
	if err != nil {
		return fmt.Errorf("can't read config template: %w", err)
	}

	if empty {
		log.Printf("NOTICE: %s is empty. It gets the settings in %s.", path, template)
		if err := fillEmptyConfigFile(path, content); err != nil {
			return fmt.Errorf("config file %s is empty, and can't be filled from %s: %w", path, template, err)
		}
		return nil
	}

	if err := createConfigFile(path, content); err != nil {
		return fmt.Errorf("can't create config file %s from %s: %w", path, template, err)
	}
	return nil
}

// configFormat returns the format of a configuration file as its name tells
// it, and whether the settings can be read from a file of that name.
//
// It goes by the extension exactly as the reader of the settings does (Viper,
// in initViper): as it is written, so ".JSON" is not ".json" and names no
// format at all. The template was copied to a file named settings.JSON, which
// then stopped that start and every later one as an unsupported type. The two
// spellings of YAML are one format.
func configFormat(path string) (format string, readable bool) {
	format = filepath.Ext(path)
	if format != "" {
		format = format[1:]
	}
	readable = slices.Contains(viper.SupportedExts, format)
	if format == "yml" {
		format = "yaml"
	}
	return format, readable
}

// createConfigFile creates the file at path with the content, in one step:
// the file is there complete or not at all, also when the server is stopped
// or the machine goes down on the way. A file that appeared at path in the
// meantime is kept.
func createConfigFile(path string, content []byte) error {
	// Under a name of its own first, next to path so that it is on the same
	// filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		// It has its final name by now, or it is of no use.
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	if _, err = tmp.Write(content); err != nil {
		return err
	}
	if err = tmp.Chmod(seededConfigPermissions); err != nil {
		return err
	}
	// On the disk before it gets its name, or a machine that goes down could
	// leave the name with nothing behind it.
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	// A hard link is not made when the name is taken, where a rename
	// replaces what is there.
	err = os.Link(tmp.Name(), path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return nil
	}

	// A filesystem without hard links.
	return os.Rename(tmp.Name(), path)
}

// fillEmptyConfigFile writes the content into the empty file at path. The
// file is kept, with its owner and permissions, and not replaced: it can be
// mounted in the container by itself, and such a file cannot be renamed
// over. The content goes there in one write, and a start that is stopped
// before it leaves the file empty, to be filled by the next one.
func fillEmptyConfigFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}

	if _, err = file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
