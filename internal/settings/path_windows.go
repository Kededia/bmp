//go:build windows

package settings

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path stores BMP settings below the user's Windows AppData configuration root.
func Path() (string, error) {
	if override := os.Getenv("BMP_SETTINGS_PATH"); override != "" {
		return filepath.Clean(override), nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate AppData: %w", err)
	}
	return filepath.Join(root, "BMP", "settings.json"), nil
}
