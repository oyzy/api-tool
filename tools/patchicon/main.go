// Command patchicon gives a Windows exe built by plain `go build` the app's
// icon and version information: Go binaries carry no resources, and only
// `viva build` patches its own. It rewrites the exe in place, reading the
// icon, name and version from viva.toml:
//
//	go build -o apitool.exe . && go run ./tools/patchicon
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vivaui/viva/tools/winpkg"
	"github.com/vivaui/viva/tools/winres"
)

func main() {
	exe := "apitool.exe"
	if len(os.Args) > 1 {
		exe = os.Args[1]
	}
	name, version, icon := manifest()

	var ico []byte
	if icon != "" {
		var err error
		if ico, _, err = winpkg.LoadIcon(icon); err != nil {
			fatal(err)
		}
	}
	program, err := os.ReadFile(exe)
	if err != nil {
		fatal(err)
	}
	patched, err := winres.Patch(program, ico, winres.Info{
		Name: name, Description: name, Version: version, File: exe,
	})
	if errors.Is(err, winres.ErrHasResources) {
		fmt.Println(exe, "already has resources")
		return
	}
	if err != nil {
		fatal(err)
	}
	info, err := os.Stat(exe)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(exe, patched, info.Mode().Perm()); err != nil {
		fatal(err)
	}
	fmt.Println("patched", exe, "with", icon)
}

// manifest reads the flat [app] table of viva.toml: the fields the patch
// needs, one `key = "value"` per line.
func manifest() (name, version, icon string) {
	b, err := os.ReadFile("viva.toml")
	if err != nil {
		fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "version":
			version = value
		case "icon":
			icon = value
		}
	}
	return name, version, icon
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "patchicon:", err)
	os.Exit(1)
}
