package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Dir returns the cache directory, creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "ghq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Save writes data as JSON to the given filename in the cache dir.
func Save(filename string, data any) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), b, 0o644)
}

// Load reads JSON from the given filename in the cache dir into dest.
// Returns false if the file doesn't exist (not an error).
func Load(filename string, dest any) (bool, error) {
	dir, err := Dir()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, json.Unmarshal(b, dest)
}
