//go:build vivahead

package main

// auto makes RunCanvas serve the headless line protocol instead of opening a
// window when the process starts with VIVA_HEADLESS set. It is only in tagged
// builds, never in a release.
import _ "github.com/vivaui/viva/headless/auto"
