//go:build vivahead

package main

// The headless build serves the line protocol instead of opening a window
// (VIVA_HEADLESS set) — and it never touches the real store: a plain run gets
// a throwaway per-process directory, and an explicit VIVA_HEADLESS_STORE lets
// a two-phase run (seed, then verify) share one file across restarts. Either
// way a forgotten AppData override cannot overwrite the user's data with test
// runs.
import (
	"os"
	"path/filepath"
	"strconv"

	_ "github.com/vivaui/viva/headless/auto"
)

func init() {
	if dataPath != "" { // tests pin their own store
		return
	}
	if store := os.Getenv("VIVA_HEADLESS_STORE"); store != "" {
		_ = os.MkdirAll(filepath.Dir(store), 0o755)
		dataPath = store
		return
	}
	// plain headless run: never touch the real store
	dir := filepath.Join(os.TempDir(), "apitool-headless-store-"+strconv.Itoa(os.Getpid()))
	_ = os.MkdirAll(dir, 0o755)
	dataPath = filepath.Join(dir, "data.json")
}
