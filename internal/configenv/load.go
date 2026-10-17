// Package configenv loads BMP's optional dotenv configuration.
package configenv

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// Load reads .env from the working directory and executable-adjacent fallback
// locations. Existing process environment variables always take precedence.
// Missing files are expected; malformed or unreadable files are reported.
func Load() error {
	candidates := []string{".env"}
	if executable, err := os.Executable(); err == nil {
		directory := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(directory, ".env"),
			filepath.Join(directory, "..", ".env"),
		)
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if err := godotenv.Load(candidate); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("load environment file %s: %w", candidate, err)
		}
	}
	return nil
}
