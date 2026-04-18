package config

import (
	"os"
	"path/filepath"
	"testing"
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
