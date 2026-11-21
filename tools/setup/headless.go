//go:build vivahead

package main

// auto makes RunCanvas serve the headless line protocol instead of opening a
// window when the process starts with VIVA_HEADLESS set. Test-only.
import _ "github.com/vivaui/viva/headless/auto"
