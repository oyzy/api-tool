package main

// The uninstaller lives in the app itself: apitool.exe -uninstall reads the
// install directory from the registry, removes the uninstall entry and the
// shortcuts, and finally removes the install directory — detached, after this
// process (which sits inside that directory) is gone. The setup's registry
// entry points here, so no second binary has to ship.
import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// uninstallKey mirrors the installer's registry entry.
const uninstallKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\apitool`

// runUninstall is the -uninstall entry: silent cleanup, then a detached
// sweep of the install directory once this process has exited.
func runUninstall() {
	if runtime.GOOS != "windows" {
		return
	}
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
	if desktop, err := desktopFolderPath(); err == nil {
		_ = os.Remove(filepath.Join(desktop, "ApiTool.lnk"))
	}
	_ = exec.Command("cmd", "/c", "timeout /t 2 /nobreak >nul & rmdir /s /q \""+dir+"\"").Start()
}

// desktopFolderPath asks Windows where the desktop is (OneDrive may have
// redirected it).
func desktopFolderPath() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", "[Environment]::GetFolderPath('Desktop')").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
