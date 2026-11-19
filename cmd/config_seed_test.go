package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newServerTestCmd returns a command with the root command's flags, parsed
// from args. Each test gets its own, so no flag state is shared through the
// package's commands.
func newServerTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().StringP("config", "c", "", "config file path")
	addServerFlags(cmd.Flags())

	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	return cmd
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist, got err = %v", path, err)
	}
}

// assertErrorNames fails the test unless err is an error whose message has
// every one of parts in it.
func assertErrorNames(t *testing.T, err error, parts ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range parts {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error = %q, want it to name %s", err, part)
		}
	}
}

func TestSeedConfigFile(t *testing.T) {
	const template = `{"port": 80}`

	t.Run("creates a missing config file from the template", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, configPath); got != template {
			t.Errorf("config file = %q, want %q", got, template)
		}
	})

	t.Run("keeps an existing config file", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)
		writeTestFile(t, configPath, `{"port": 1234}`)

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, configPath); got != `{"port": 1234}` {
			t.Errorf("config file was overwritten with %q", got)
		}
	})

	t.Run("creates the file readable, and leaves nothing else behind", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(t.TempDir(), "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != seededConfigPermissions {
			t.Errorf("mode = %o, want %o", got, seededConfigPermissions)
		}

		// The file is written under another name first.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("%d files next to the config file, want none", len(entries)-1)
		}
	})

	// The file was created first and filled second, so a first start that
	// was stopped in between left an empty file, which stopped every later
	// start: "unexpected end of JSON input".
	t.Run("fills an empty config file, and keeps its permissions", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)
		writeTestFile(t, configPath, "")

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, configPath); got != template {
			t.Errorf("config file = %q, want %q", got, template)
		}
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("mode = %o, want the 600 the file had", got)
		}
	})

	// As for a file that is mounted in the container by itself: it can be
	// written, and nothing can be created next to it or renamed over it.
	t.Run("fills an empty config file where no file can be created", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can create a file in any directory")
		}

		dir := t.TempDir()
		templatePath := filepath.Join(t.TempDir(), "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)
		writeTestFile(t, configPath, "")
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, configPath); got != template {
			t.Errorf("config file = %q, want %q", got, template)
		}
	})

	t.Run("names an empty config file that it cannot fill", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write to any file")
		}

		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)
		writeTestFile(t, configPath, "")
		if err := os.Chmod(configPath, 0400); err != nil {
			t.Fatal(err)
		}

		err := seedConfigFile(configPath, templatePath)
		if err == nil || !strings.Contains(err.Error(), configPath) {
			t.Errorf("error = %v, want one that names %s", err, configPath)
		}
	})

	// A link is the user's own arrangement, whatever it points to.
	t.Run("keeps a link at the path", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		target := filepath.Join(dir, "target.json")
		writeTestFile(t, templatePath, template)
		writeTestFile(t, target, "")
		if err := os.Symlink(target, configPath); err != nil {
			t.Skip("symbolic links are not available")
		}

		if err := seedConfigFile(configPath, templatePath); err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, target); got != "" {
			t.Errorf("the file the link points to holds %q, want it untouched", got)
		}
	})

	// The JSON template was copied to a file named x.toml, which could not
	// be read as TOML, then or on any later start.
	t.Run("does not copy the template to a file of another format", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		writeTestFile(t, templatePath, template)

		for _, name := range []string{"settings.toml", "settings.yaml", "settings", "settings.json.bak"} {
			configPath := filepath.Join(dir, name)
			if err := seedConfigFile(configPath, templatePath); err != nil {
				t.Fatal(err)
			}
			assertNoFile(t, configPath)
		}

		// An empty file of another format stays empty: as TOML or YAML, it
		// is a file without settings.
		emptyPath := filepath.Join(dir, "empty.toml")
		writeTestFile(t, emptyPath, "")
		if err := seedConfigFile(emptyPath, templatePath); err != nil {
			t.Fatal(err)
		}
		if got := readTestFile(t, emptyPath); got != "" {
			t.Errorf("the empty file holds %q, want it untouched", got)
		}
	})

	t.Run("takes both spellings of YAML for one format", func(t *testing.T) {
		dir := t.TempDir()
		for config, templateName := range map[string]string{
			"settings.yaml": "b.yml",
			"settings.yml":  "c.yaml",
		} {
			templatePath := filepath.Join(dir, templateName)
			configPath := filepath.Join(dir, config)
			writeTestFile(t, templatePath, template)

			if err := seedConfigFile(configPath, templatePath); err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, configPath); got != template {
				t.Errorf("%s = %q, want the content of %s", config, got, templateName)
			}
		}
	})

	// The settings are read by the extension as it is written. The template
	// was copied to settings.JSON, which is no format to the reader: the
	// file stopped that start, and every later one.
	t.Run("does not copy the template to a file whose extension the reader does not know", func(t *testing.T) {
		dir := t.TempDir()
		for config, templateName := range map[string]string{
			"upper.JSON":    "a.json",
			"same.JSON":     "b.JSON",
			"mixed.Json":    "c.json",
			"upper.YAML":    "d.yml",
			"settings.conf": "e.conf",
			"settings":      "f",
		} {
			templatePath := filepath.Join(dir, templateName)
			configPath := filepath.Join(dir, config)
			writeTestFile(t, templatePath, template)

			if err := seedConfigFile(configPath, templatePath); err != nil {
				t.Fatal(err)
			}
			assertNoFile(t, configPath)
		}
	})

	t.Run("does nothing without a template or a path", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, template)

		if err := seedConfigFile(configPath, ""); err != nil {
			t.Fatal(err)
		}
		if err := seedConfigFile("", templatePath); err != nil {
			t.Fatal(err)
		}

		assertNoFile(t, configPath)
	})

	t.Run("fails when the template is missing", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "settings.json")

		if err := seedConfigFile(configPath, filepath.Join(dir, "missing.json")); err == nil {
			t.Error("expected an error")
		}

		assertNoFile(t, configPath)
	})

	t.Run("fails when the config directory is missing", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		writeTestFile(t, templatePath, template)

		if err := seedConfigFile(filepath.Join(dir, "missing", "settings.json"), templatePath); err == nil {
			t.Error("expected an error")
		}
	})
}

// A file that another process put at the path while the content was on its
// way is that process's, and stays.
func TestCreateConfigFileKeepsAFileThatIsThere(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	writeTestFile(t, configPath, `{"port": 1234}`)

	if err := createConfigFile(configPath, []byte(`{"port": 80}`)); err != nil {
		t.Fatal(err)
	}

	if got := readTestFile(t, configPath); got != `{"port": 1234}` {
		t.Errorf("config file was overwritten with %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files next to the config file, want none", len(entries)-1)
	}
}

func TestInitViperConfigFromEnv(t *testing.T) {
	t.Run("FB_CONFIG names the config file", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "settings.json")
		writeTestFile(t, configPath, `{"port": 1234}`)
		t.Setenv(envConfig, configPath)

		v, err := initViper(newServerTestCmd(t))
		if err != nil {
			t.Fatal(err)
		}

		if got := v.ConfigFileUsed(); got != configPath {
			t.Errorf("config file used = %q, want %q", got, configPath)
		}
		if got := v.GetString("port"); got != "1234" {
			t.Errorf("port = %q, want 1234", got)
		}
	})

	t.Run("a missing FB_CONFIG file is created from FB_CONFIG_TEMPLATE", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, `{"port": 4321, "address": ""}`)
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		v, err := initViper(newServerTestCmd(t))
		if err != nil {
			t.Fatal(err)
		}

		if got := readTestFile(t, configPath); got != `{"port": 4321, "address": ""}` {
			t.Errorf("config file = %q, want the template", got)
		}
		if got := v.GetString("port"); got != "4321" {
			t.Errorf("port = %q, want 4321", got)
		}
		// The file's empty address has to win over the flag's default.
		if got := v.GetString("address"); got != "" {
			t.Errorf("address = %q, want it empty", got)
		}
	})

	t.Run("an empty FB_CONFIG file is filled from FB_CONFIG_TEMPLATE", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		writeTestFile(t, configPath, "")
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		v, err := initViper(newServerTestCmd(t))
		if err != nil {
			t.Fatal(err)
		}

		if got := v.GetString("port"); got != "4321" {
			t.Errorf("port = %q, want 4321", got)
		}
	})

	t.Run("an existing FB_CONFIG file is not replaced by the template", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		writeTestFile(t, configPath, `{"port": 1234}`)
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		v, err := initViper(newServerTestCmd(t))
		if err != nil {
			t.Fatal(err)
		}

		if got := v.GetString("port"); got != "1234" {
			t.Errorf("port = %q, want 1234", got)
		}
	})

	t.Run("a failed copy of the template is an error", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		t.Setenv(envConfig, filepath.Join(dir, "missing", "settings.json"))
		t.Setenv(envConfigTemplate, templatePath)

		if _, err := initViper(newServerTestCmd(t)); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("--config wins over FB_CONFIG and nothing is created", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		envPath := filepath.Join(dir, "env.json")
		flagPath := filepath.Join(dir, "flag.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		writeTestFile(t, flagPath, `{"port": 1234}`)
		t.Setenv(envConfig, envPath)
		t.Setenv(envConfigTemplate, templatePath)

		v, err := initViper(newServerTestCmd(t, "--config", flagPath))
		if err != nil {
			t.Fatal(err)
		}

		if got := v.ConfigFileUsed(); got != flagPath {
			t.Errorf("config file used = %q, want %q", got, flagPath)
		}
		if got := v.GetString("port"); got != "1234" {
			t.Errorf("port = %q, want 1234", got)
		}
		assertNoFile(t, envPath)
	})

	// A file that is asked for by name and is not there was passed over in
	// silence. The server went on with its defaults: in the Docker image
	// that ended in "open /filebrowser.db: permission denied", and as root
	// it served the working directory.
	t.Run("a missing --config file is an error, and is not created from the template", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		flagPath := filepath.Join(dir, "flag.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		t.Setenv(envConfigTemplate, templatePath)

		_, err := initViper(newServerTestCmd(t, "--config", flagPath))
		assertErrorNames(t, err, flagPath, "--config")
		assertNoFile(t, flagPath)
	})

	t.Run("a missing FB_CONFIG file is an error when there is no template", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "settings.json")
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, "")

		_, err := initViper(newServerTestCmd(t))
		assertErrorNames(t, err, configPath, envConfig)
		assertNoFile(t, configPath)
	})

	t.Run("a missing FB_CONFIG file of another format than the template is an error", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.toml")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		_, err := initViper(newServerTestCmd(t))
		assertErrorNames(t, err, configPath, envConfig)
		assertNoFile(t, configPath)
	})

	// What the reader makes of a name decides: it takes ".JSON" for no
	// format, so a file of that name is not created either.
	t.Run("a missing FB_CONFIG file whose extension is not in lower case is an error, and is not created", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		configPath := filepath.Join(dir, "settings.JSON")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		t.Setenv(envConfig, configPath)
		t.Setenv(envConfigTemplate, templatePath)

		_, err := initViper(newServerTestCmd(t))
		assertErrorNames(t, err, configPath, envConfig)
		assertNoFile(t, configPath)
	})

	// Every format that is seeded is one that is read.
	t.Run("a file that is created from the template is read", func(t *testing.T) {
		for _, name := range []string{"settings.json", "settings.yaml", "settings.yml"} {
			dir := t.TempDir()
			templatePath := filepath.Join(dir, "template"+filepath.Ext(name))
			configPath := filepath.Join(dir, name)
			writeTestFile(t, templatePath, `{"port": 4321}`)
			t.Setenv(envConfig, configPath)
			t.Setenv(envConfigTemplate, templatePath)

			v, err := initViper(newServerTestCmd(t))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := v.GetString("port"); got != "4321" {
				t.Errorf("%s: port = %q, want 4321", name, got)
			}
		}
	})

	t.Run("a config file of a type that cannot be read is an error", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "settings.conf")
		writeTestFile(t, configPath, `{"port": 1234}`)
		t.Setenv(envConfig, configPath)

		_, err := initViper(newServerTestCmd(t))
		assertErrorNames(t, err, configPath, envConfig)
	})

	t.Run("a config file that cannot be parsed is named in the error", func(t *testing.T) {
		flagPath := filepath.Join(t.TempDir(), "flag.json")
		writeTestFile(t, flagPath, "")

		_, err := initViper(newServerTestCmd(t, "--config", flagPath))
		assertErrorNames(t, err, flagPath, "--config")
	})

	t.Run("a config file that is found and cannot be parsed is named in the error", func(t *testing.T) {
		home := t.TempDir()
		found := filepath.Join(home, ".filebrowser.json")
		writeTestFile(t, found, `{"port": `)
		t.Setenv("HOME", home)
		t.Setenv(envConfig, "")

		_, err := initViper(newServerTestCmd(t))
		assertErrorNames(t, err, found)
	})

	t.Run("without FB_CONFIG the default locations are searched", func(t *testing.T) {
		// Empty, so that no .filebrowser file of the machine running the
		// tests is found in the home directory.
		t.Setenv("HOME", t.TempDir())
		t.Setenv(envConfig, "")

		v, err := initViper(newServerTestCmd(t))
		if err != nil {
			t.Fatal(err)
		}

		if got := v.ConfigFileUsed(); got != "" {
			t.Errorf("config file used = %q, want none", got)
		}
		if got := v.GetString("port"); got != "8080" {
			t.Errorf("port = %q, want the default 8080", got)
		}
	})
}
