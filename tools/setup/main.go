package main

// The Windows installer: a small Viva app that embeds the built apitool.exe,
// copies it into the directory the user picks, lays down shortcuts and the
// per-user uninstall entry (Apps & Features), and offers to launch the app.
// The same binary with -uninstall reverses it, which is what the registry's
// UninstallString points at.
//
// The embedded placeholder is an empty file in git; build with the real exe:
//
//	cp apitool.exe tools/setup/apitool.exe
//	go build -trimpath -ldflags "-H=windowsgui" -o apitool-setup.exe ./tools/setup

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/tools/winpkg"
	"github.com/vivaui/viva/ui"
)

//go:embed apitool.exe
var appExe []byte

//go:embed icon.png
var iconPNG []byte

const (
	appName      = "ApiTool"
	appVersion   = "0.1.1"
	uninstallKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\apitool`
)

// model is the installer's state: the target directory (empty = the default
// under %LOCALAPPDATA%\Programs), the shortcut choices, and how far the
// install got.
type model struct {
	dir        string
	desktop    bool
	startMenu  bool
	runAfter   bool
	theme      ui.Theme
	inv        app.Invalidator
	installed  string // the directory once installed, empty until then
	statusText string
	statusOK   bool
	busy       bool
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-uninstall" {
		runUninstall()
		return
	}
	m := &model{desktop: true, startMenu: true, runAfter: true, theme: ui.LightTheme()}
	page := ui.NewPage(ui.PageConfig{View: m.page, Theme: &m.theme})
	cfg := page.Config(app.Config{
		Invalidator: &m.inv,
		Title:       appName + " " + appVersion + " 安装",
		Width:       560,
		Height:      380,
		MinWidth:    460,
		MinHeight:   320,
		WindowMode:  app.WindowCustomTitlebar,
	})
	var canvas gfx.Canvas
	if err := app.RunCanvas(cfg, &canvas); err != nil {
		os.Exit(1)
	}
}

// defaultDir is where the app installs when the user leaves the path blank:
// a per-user location, so no administrator rights are needed.
func defaultDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "apitool")
}

func (m *model) page(c ui.C) {
	theme := c.Theme()
	ui.Column(c).Fill().Pad(24).Gap(14).Children(func() {
		ui.Row(c).Gap(10).Items(ui.Center).Children(func() {
			ui.Text(c, appName).FontSize(22).Bold()
			ui.Text(c, "v"+appVersion).Muted().Mono().FontSize(13)
		})
		ui.Text(c, "选择安装位置，快捷方式与卸载入口会一并创建。").Muted().FontSize(13)

		ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
			ui.Text(c, "安装到").Muted()
			ui.TextField(c, &m.dir, "安装路径", "留空安装到默认位置").Grow(1)
		})
		ui.Row(c).Gap(16).Children(func() {
			ui.Checkbox(c, &m.desktop, "桌面快捷方式")
			ui.Checkbox(c, &m.startMenu, "开始菜单快捷方式")
			ui.Checkbox(c, &m.runAfter, "安装后运行")
		})

		if m.installed != "" {
			ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
				icon := "✓"
				color := theme.Success
				if !m.statusOK {
					icon, color = "✗", theme.Danger
				}
				ui.Text(c, icon).Color(color).Bold()
				ui.Text(c, m.statusText).FontSize(13).Grow(1).Ellipsis()
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if m.statusOK && ui.Button(c, "运行 "+appName).Clicked() {
					c.After(func() { launchInstalled(m.installed) })
				}
				if ui.Button(c, "完成").Clicked() {
					c.After(func() { os.Exit(0) })
				}
			})
		} else {
			ui.Row(c).Gap(8).Justify(ui.End).Items(ui.Center).Children(func() {
				if m.busy {
					ui.Spinner(c)
					ui.Text(c, m.statusText).Muted().FontSize(13)
				} else {
					if ui.Button(c, "浏览…").Outline().Clicked() {
						c.After(func() { m.browse() })
					}
					if ui.Button(c, "安装 "+appName).Clicked() {
						c.After(func() { m.install() })
					}
				}
			})
		}
		ui.Spacer(c).Grow(1)
		ui.Text(c, "卸载：系统设置的应用列表，或安装目录里的卸载入口。").FontSize(12).Muted()
	})
}

// browse lets the user pick the target directory: it returns outside a paint,
// so the window needs an explicit nudge.
func (m *model) browse() {
	dir, err := app.OpenDirectory(app.OpenDirectoryOptions{Title: "选择安装位置"})
	if err != nil || dir == "" {
		return
	}
	m.dir = dir
	m.inv.Invalidate()
}

// install copies the embedded app and lays down shortcuts and the uninstall
// entry. The steps are quick file operations, so they run inline in the click.
func (m *model) install() {
	if m.busy {
		return
	}
	m.busy = true
	dir := strings.TrimSpace(m.dir)
	if dir == "" {
		dir = defaultDir()
	}
	status := func(text string, ok bool) {
		m.busy = false
		m.installed = dir
		m.statusText, m.statusOK = text, ok
	}
	if err := installTo(dir, m.desktop, m.startMenu); err != nil {
		status("安装失败："+err.Error(), false)
		return
	}
	status("已安装到 "+dir, true)
	if m.runAfter {
		launchInstalled(dir)
	}
}

// installTo does the actual work, separated from the UI state so -uninstall
// and tests can reason about it.
func installTo(dir string, desktop, startMenu bool) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("安装器仅支持 Windows")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "apitool.exe"), appExe, 0o644); err != nil {
		return err
	}
	// A dedicated .ico file is the shortcut's icon source: pointing a fresh
	// .lnk at an exe written moments ago can pick up a stale Windows icon
	// cache entry (a generic sheet), while this path is new to the cache.
	icoPath := filepath.Join(dir, "apitool.ico")
	icoOK := false
	if tmp := filepath.Join(os.TempDir(), "apitool-icon.png"); os.WriteFile(tmp, iconPNG, 0o644) == nil {
		if ico, _, err := winpkg.LoadIcon(tmp); err == nil && len(ico) > 0 {
			if os.WriteFile(icoPath, ico, 0o644) == nil {
				icoOK = true
			}
		}
		_ = os.Remove(tmp)
	}
	self, err := os.Executable()
	if err == nil {
		if b, err := os.ReadFile(self); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "uninstall.exe"), b, 0o644)
		}
	}
	if headless() {
		return nil // tests: no registry or shortcut writes
	}
	if startMenu {
		menu := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
		makeShortcut(filepath.Join(menu, "ApiTool.lnk"), dir, icoOK, icoPath)
	}
	if desktop {
		makeShortcut(filepath.Join(desktopFolder(), "ApiTool.lnk"), dir, icoOK, icoPath)
	}
	// Refresh the shell's icon caches, or a shortcut can show whatever was
	// cached for these paths before.
	_ = exec.Command("ie4uinit.exe", "-show").Run()
	for _, v := range [][2]string{
		{"DisplayName", "ApiTool"},
		{"DisplayVersion", appVersion},
		{"Publisher", "oyzy"},
		{"InstallLocation", dir},
		{"DisplayIcon", iconSource(dir, icoOK, icoPath)},
		{"UninstallString", `"` + filepath.Join(dir, "uninstall.exe") + `" -uninstall`},
	} {
		_ = exec.Command("reg", "add", uninstallKey, "/v", v[0], "/t", "REG_SZ", "/d", v[1], "/f").Run()
	}
	return nil
}

// uninstall removes what installTo laid down: registry, shortcuts, and the
// install directory. The running uninstaller itself sits in that directory,
// so the final rmdir runs detached after this process is gone.
func runUninstall() {
	out, _ := exec.Command("reg", "query", uninstallKey, "/v", "InstallLocation").Output()
	dir := ""
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "InstallLocation") {
			continue
		}
		if parts := strings.Fields(line); len(parts) >= 3 {
			dir = parts[len(parts)-1]
		}
	}
	if dir == "" {
		return
	}
	_ = exec.Command("reg", "delete", uninstallKey, "/f").Run()
	_ = os.Remove(filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "ApiTool.lnk"))
	_ = os.Remove(filepath.Join(desktopFolder(), "ApiTool.lnk"))
	// the classic dance: this exe lives in the directory being removed
	_ = exec.Command("cmd", "/c", "timeout /t 2 /nobreak >nul & rmdir /s /q \""+dir+"\"").Start()
}

// makeShortcut lays a .lnk via the shell COM object PowerShell exposes.
func makeShortcut(lnk, dir string, icoOK bool, icoPath string) {
	home := fmt.Sprintf("%s\\apitool.exe", dir)
	icon := iconSource(dir, icoOK, icoPath)
	ps := fmt.Sprintf(`$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.IconLocation = '%s,0'; $s.Save()`, lnk, home, dir, icon)
	_ = exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
}

// iconSource is what shortcuts and the registry use as the app's icon: the
// dedicated .ico when the install laid one down, else the exe itself.
func iconSource(dir string, icoOK bool, icoPath string) string {
	if icoOK {
		return icoPath
	}
	return filepath.Join(dir, "apitool.exe")
}

// desktopFolder asks Windows where the desktop is (OneDrive may redirect it).
func desktopFolder() string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", "[Environment]::GetFolderPath('Desktop')").Output()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Desktop")
	}
	return strings.TrimSpace(string(out))
}

// launchInstalled starts the installed app detached from the installer.
func launchInstalled(dir string) {
	_ = exec.Command("cmd", "/c", "start", "", filepath.Join(dir, "apitool.exe")).Start()
}

// headless reports the headless test mode, in which the installer skips every
// write outside its target directory.
func headless() bool {
	return os.Getenv("VIVA_HEADLESS") != ""
}
