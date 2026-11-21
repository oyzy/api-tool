//go:build vivahead

package main

// The headless build serves the line protocol instead of opening a window
// (VIVA_HEADLESS set) — and it never touches the real store: without an
// explicit AppData override the data file is pointed at a throwaway
// directory, so a forgotten override cannot overwrite the user's data with
// test runs.
import (
	"os"
	"path/filepath"

	_ "github.com/vivaui/viva/headless/auto"
)

func init() {
	if dataPath == "" {
		dir := filepath.Join(os.TempDir(), "apitool-headless-store")
		_ = os.MkdirAll(dir, 0o755)
		dataPath = filepath.Join(dir, "data.json")
	}
}
