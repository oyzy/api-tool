// genicon compiles an SVG icon into the Go gfx.Icon literal that icons.go
// embeds, using the framework's own compiler:
//
//	go run ./tools/genicon icon.svg > icon.go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/tools/iconcompile"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: genicon icon.svg [more.svg …] — prints a Go file with one var per icon")
		os.Exit(2)
	}
	icons := make(map[string]gfx.Icon, len(os.Args)-1)
	for _, file := range os.Args[1:] {
		source, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		icon, err := iconcompile.Compile(file, source)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		icons[iconName(file)] = icon
	}
	code, err := iconcompile.Generate("main", icons, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Stdout.Write(code)
}

// iconName derives the Go var name from the file's base name: github.svg →
// Github.
func iconName(file string) string {
	base := file
	if i := strings.LastIndexAny(file, "/\\"); i >= 0 {
		base = file[i+1:]
	}
	base = strings.TrimSuffix(base, ".svg")
	if base == "" {
		return "Icon"
	}
	return strings.ToUpper(base[:1]) + base[1:]
}
