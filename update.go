package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Update checking. The app asks GitHub for its latest release once at startup
// and whenever the user clicks 检查更新; a newer tag opens the update dialog,
// which can open the release page or download the build for this platform.

const (
	appName    = "ApiTool"
	appVersion = "0.1.5"
	repoURL    = "https://github.com/oyzy/api-tool"
	repoSlug   = "oyzy/api-tool"
)

// updateAPIBase is overridden by VIVA_UPDATE_API so headless runs can check
// against a local server instead of GitHub.
var updateAPIBase = "https://api.github.com"

func init() {
	if v := os.Getenv("VIVA_UPDATE_API"); v != "" {
		updateAPIBase = v
	}
}

// updateInfo is one check's outcome. err is set when the check itself failed;
// upToDate when the latest release is not newer than this build.
type updateInfo struct {
	available bool
	version   string // the release's tag, e.g. v0.1.1
	url       string // the release page
	notes     string
	assetName string
	assetURL  string
	size      int64
	sha256    string // the asset's expected hash, from the release's checksums.txt
	upToDate  bool
	err       string
}

// dlResult is one finished asset download: the update's exe bytes in memory,
// ready for applyUpdate.
type dlResult = []byte

// release is the slice of GitHub's release JSON the check reads.
type release struct {
	TagName    string     `json:"tag_name"`
	HTMLURL    string     `json:"html_url"`
	Body       string     `json:"body"`
	Prerelease bool       `json:"prerelease"`
	Draft      bool       `json:"draft"`
	Assets     []relAsset `json:"assets"`
}

type relAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// semverLess says whether version a is older than b. Both are dotted numbers,
// with or without a leading v; anything unparseable compares byte-wise and
// counts as older.
func semverLess(a, b string) bool {
	as, aok := semverParts(a)
	bs, bok := semverParts(b)
	if !aok || !bok {
		return strings.TrimPrefix(a, "v") < strings.TrimPrefix(b, "v")
	}
	for i := range as {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return false
}

func semverParts(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, part := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return out, false
		}
		out[i] = n
		if i == 2 {
			break
		}
	}
	return out, true
}

// pickAsset names the release asset built for the given platform: the project
// releases as apitool-<goos>-<arch>[.exe].
func pickAsset(assets []relAsset, goos, arch string) (relAsset, bool) {
	want := "apitool-" + goos + "-" + arch
	for _, a := range assets {
		name := strings.TrimSuffix(a.Name, ".exe")
		if name == want && a.Size > 0 && a.BrowserDownloadURL != "" {
			return a, true
		}
	}
	return relAsset{}, false
}

// updateHTTPClient walks the proxy chain: Go honours the environment's proxy
// variables but not the Windows system proxy, and api.github.com is often
// unreachable directly — so on failure the check retries through the system
// proxy found in the registry.
func updateHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}
}

// fetchUpdate asks GitHub for the latest release and compares it with
// appVersion. It runs off the paint, in an effect.
func fetchUpdate(ctx context.Context) (updateInfo, error) {
	info := updateInfo{}
	client := updateHTTPClient()
	body, err := httpGet(ctx, client, updateAPIBase+"/repos/"+repoSlug+"/releases/latest")
	if err != nil && runtime.GOOS == "windows" {
		if proxy := systemProxy(); proxy != "" {
			if u, perr := url.Parse(proxy); perr == nil {
				viaSystem := &http.Client{
					Timeout:   15 * time.Second,
					Transport: &http.Transport{Proxy: http.ProxyURL(u)},
				}
				body, err = httpGet(ctx, viaSystem, updateAPIBase+"/repos/"+repoSlug+"/releases/latest")
			}
		}
	}
	if err != nil {
		return info, err
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return info, fmt.Errorf("无法解析版本信息: %w", err)
	}
	if rel.Draft || rel.Prerelease || rel.TagName == "" {
		return updateInfo{upToDate: true}, nil
	}
	info.version = rel.TagName
	info.url = rel.HTMLURL
	info.notes = rel.Body
	if asset, ok := pickAsset(rel.Assets, runtime.GOOS, runtime.GOARCH); ok {
		info.assetName, info.assetURL, info.size = asset.Name, asset.BrowserDownloadURL, asset.Size
	}
	// the release ships a checksums.txt: carry the expected hash for this
	// platform's asset so the download can be verified (a missing or
	// unreadable checksum is not fatal — the update still goes through)
	if info.assetName != "" {
		for _, a := range rel.Assets {
			if a.Name != "checksums.txt" {
				continue
			}
			if raw, err := httpGet(ctx, client, a.BrowserDownloadURL); err == nil {
				info.sha256 = checksumFor(string(raw), info.assetName)
			}
			break
		}
	}
	info.available = semverLess(appVersion, rel.TagName)
	info.upToDate = !info.available
	return info, nil
}

// downloadUpdate fetches the release asset for this platform. The bytes stay
// in memory; applyUpdate swaps them onto disk.
func downloadUpdate(ctx context.Context, info updateInfo) ([]byte, error) {
	if info.assetURL == "" {
		return nil, fmt.Errorf("该版本没有当前平台的安装包")
	}
	client := updateHTTPClient()
	body, err := httpGet(ctx, client, info.assetURL)
	if err != nil && runtime.GOOS == "windows" {
		if proxy := systemProxy(); proxy != "" {
			if u, perr := url.Parse(proxy); perr == nil {
				viaSystem := &http.Client{
					Timeout:   5 * time.Minute,
					Transport: &http.Transport{Proxy: http.ProxyURL(u)},
				}
				body, err = httpGet(ctx, viaSystem, info.assetURL)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if info.size > 0 && int64(len(body)) != info.size {
		return nil, fmt.Errorf("下载不完整: %d/%d 字节", len(body), info.size)
	}
	if info.sha256 != "" {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != info.sha256 {
			return nil, fmt.Errorf("校验和不匹配，下载可能损坏，已中止")
		}
	}
	return body, nil
}

// checksumFor picks one file's hash out of a sha256sum-style checksum listing.
func checksumFor(list, name string) string {
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			file := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
			if file == name {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

// applyUpdate swaps the downloaded exe over the running one: a running exe on
// Windows can be RENAMED (the image stays mapped), so the old file steps aside
// as .old and the new one takes its place. The caller restarts the app.
func applyUpdate(b []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return applyUpdateFile(exe, b)
}

// applyUpdateFile swaps b over the exe at exePath (see applyUpdate).
func applyUpdateFile(exe string, b []byte) error {
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		_ = os.Rename(old, exe) // put the old one back
		return err
	}
	return nil
}

// cleanupOldUpdate removes the .old file a previous update left behind.
func cleanupOldUpdate() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// restartApp launches the freshly updated exe and ends this one.
func restartApp() {
	if exe, err := os.Executable(); err == nil {
		_ = exec.Command("cmd", "/c", "start", "", exe).Start()
	}
	os.Exit(0)
}

func httpGet(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "apitool-update/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}

// systemProxyURL is systemProxy as a parsed URL, nil when none is set.
func systemProxyURL() *url.URL {
	if p := systemProxy(); p != "" {
		if u, err := url.Parse(p); err == nil {
			return u
		}
	}
	return nil
}

// retryViaSystemProxy says whether a failed direct connection deserves a
// second attempt through the Windows system proxy: never for loopback
// targets, which a proxy would only break.
func retryViaSystemProxy(err error, u *url.URL) bool {
	if u == nil || systemProxyURL() == nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return false
	}
	return runtime.GOOS == "windows"
}

// systemProxy reads the Windows system proxy from the registry, where clash
// and friends publish it. Empty when none is set — the reg call is the price
// of not adding a registry dependency.
func systemProxy() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	out, err := exec.Command("reg", "query",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		"/v", "ProxyServer").Output()
	if err != nil {
		return ""
	}
	proxy := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "ProxyServer") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			proxy = parts[len(parts)-1]
		}
	}
	if proxy == "" {
		return ""
	}
	// the value can be host:port or http=host:port;https=host:port
	proxy = strings.TrimSuffix(proxy, ";")
	if seg, _, ok := strings.Cut(proxy, ";"); ok {
		proxy = seg
	}
	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	return proxy
}
