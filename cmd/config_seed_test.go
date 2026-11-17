package cmd

import (
	"os"
	"path/filepath"
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

	t.Run("a missing --config file is not created from the template", func(t *testing.T) {
		dir := t.TempDir()
		templatePath := filepath.Join(dir, "template.json")
		flagPath := filepath.Join(dir, "flag.json")
		writeTestFile(t, templatePath, `{"port": 4321}`)
		t.Setenv(envConfigTemplate, templatePath)

		v, err := initViper(newServerTestCmd(t, "--config", flagPath))
		if err != nil {
			t.Fatal(err)
		}

		assertNoFile(t, flagPath)
		if got := v.GetString("port"); got != "8080" {
			t.Errorf("port = %q, want the default 8080", got)
		}
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
