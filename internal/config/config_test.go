package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromMissingFile(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.HelpMode {
		t.Errorf("HelpMode default = false, want true")
	}
}

func TestLoadFromExplicitDisable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("help_mode: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HelpMode {
		t.Errorf("HelpMode = true, want false")
	}
}

func TestLoadFromExplicitEnable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("help_mode: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.HelpMode {
		t.Errorf("HelpMode = false, want true")
	}
}

func TestLoadFromUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("future_setting: wow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.HelpMode {
		t.Errorf("HelpMode default = false, want true")
	}
}

func TestDirHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/example")
	dir, err := Dir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/tmp/example/gg" {
		t.Errorf("Dir() = %q, want /tmp/example/gg", dir)
	}
}

func TestDirFallsBackToHomeDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")
	dir, err := Dir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/tmp/fakehome", ".config", "gg")
	if dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func TestLoadFromCommitPrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte("commit_prompt: \"always use emoji prefixes\"\n"), 0o644)
	require.NoError(t, err)

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "always use emoji prefixes", cfg.CommitPrompt)
}

func TestLoadFromCommitPromptDefault(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "nope.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "", cfg.CommitPrompt)
}
