package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// iconPNG is the application's picture; viva build also embeds it into the
// exe's resources, and the config hands it to the running window's taskbar.
//
//go:embed icon.png
var iconPNG []byte

// model is everything the window shows. The page only reads it; a click handled
// where its control is declared is the only place that changes it.
type model struct {
	// sidebar
	sidebar     string // "收藏" | "历史" | "环境"
	collections []savedRequest
	history     []savedRequest
	histList    ui.ListState
	collQuery   string
	// collQuerySeen is the query the visible rows were built for: a new one
	// rebuilds them before the tree declares.
	collQuerySeen string
	histRows      []histRow // the history list's rows, a heading before each day

	// collection groups: named folders in order, their members reference them
	// by name; collRows is the flattened rows the tree shows
	groups    []string
	collapsed map[string]bool
	collRows  []collRow
	collTree  ui.TreeState

	// request tabs: every open request with its own editor, response and
	// baselines; tabIdx is the one on screen
	tabs   []*tabState
	tabIdx int
	tabSeq int // the id the next tab gets

	// env
	envSets       []envSet
	activeEnvName string
	envList       ui.ListState

	// layout
	splitSide ui.SplitterState
	split     ui.SplitterState

	// chrome
	toaster  ui.Toaster
	theme    ui.Theme
	themeIdx int // the entry of themePresets on screen

	themeMenuOpen bool

	// save dialog
	saveOpen    bool
	saveName    string
	saveOptions []string // 分组 choices while the dialog is open
	saveGroup   string   // the chosen 分组 option

	// group dialog: groupRename is the path a rename starts from (empty when
	// the dialog creates a folder), groupParent the folder the new one nests in
	groupOpen   bool
	groupName   string
	groupRename string
	groupParent string

	// clear-history confirmation
	confirmOpen bool

	// import menu (cURL 命令 / 集合 JSON 文件) and the cURL import dialog
	importMenuOpen bool
	importOpen     bool
	importText     string

	// env dialog: envOld is the environment set being renamed
	envOpen     bool
	envName     string
	envOld      string
	envMenuOpen bool

	// code menu
	// (codeOpen moved into tabState: each tab has its own menu)

	// settings menu and the data-directory dialog
	settingsMenuOpen bool
	dataDirOpen      bool
	dataDirNew       string

	// update check
	updateEff  *ui.EffectState[updateInfo, int]
	updateSeq  int
	updateInfo updateInfo
	updateOpen bool
	dlEff      *ui.EffectState[dlResult, string]
	dlInfo     updateInfo
	dlKey      string
	dlSeq      int

	// collection runner
	runEff     *ui.EffectState[httpResult, *outgoing]
	running    bool
	runQueue   []int
	runIndex   int
	runKey     *outgoing
	runResults []runResult

	// repaint trigger
	inv *app.Invalidator

	// c is the page context of the current paint, for the trees' row-content
	// functions, which ui.Tree calls back without one. It is set at the top of
	// every paint and used only while that paint declares.
	c ui.C // viva:ignore kept-context

	// the trees' callbacks, made once: method values made per paint allocate
	collNodeFn    func(int) ui.TreeNode
	collLabelFn   func(int) string
	collContentFn func(int)
	jsonNodeFn    func(int) ui.TreeNode
	jsonLabelFn   func(int) string
	jsonContentFn func(int)
}

// runResult is one finished request of a collection run.
type runResult struct {
	name         string
	status       int
	elapsed      time.Duration
	ok           bool
	err          string
	assertTotal  int
	assertFailed int
	assertDetail string // the failed assertions' details, one per line
}

// tabState is one open request tab: the editor's fields, the two-way sync's
// baselines, the response — everything a request owns. The model holds a slice
// of them and the page reads the active one through m.t(); switching tabs is
// switching which slice element the page declares.
type tabState struct {
	id int // stable identity for the strip's rows

	// the editor
	method          string
	url             string
	reqTab          string // "参数" | "认证" | "请求头" | "请求体"
	params          []kv
	headers         []kv
	bodyType        string
	body            string
	formFields      []formField
	followRedirects bool
	auth            authConfig  // the auth panel; its headers are synthesized at send time
	assertions      []assertion // declarative checks, evaluated against the response
	assertList      ui.ListState
	assertResults   []assertResult

	paramList  ui.ListState
	headerList ui.ListState
	formList   ui.ListState

	// the two-way sync's baselines and the body's live JSON check
	urlSeen    string
	paramsSeen string
	bodySeen   string
	bodyCheck  bodyCheck

	// editing is the index of the collection entry this tab holds, -1 when it
	// is a fresh request (a save then appends)
	editing int

	// the response
	resp      *httpResult
	respTab   string // "响应体" | "响应头"
	respLines []string
	respMeta  string
	sending   bool
	sendKey   *outgoing
	eff       *ui.EffectState[httpResult, *outgoing] // this tab's own send, so tabs send concurrently

	// the response body as a JSON tree, alongside the plain-text lines
	jsonRoot *jsonNode
	jsonRows []*jsonNode
	jsonTree ui.TreeState
	respView string // "树" | "文本"

	codeOpen bool
}

// t is the tab on screen. The page and the editor's helpers read the request
// through it; a click that switches tabs makes the next paint read another.
func (m *model) t() *tabState {
	return m.tabs[m.tabIdx]
}

// collRow is one visible row of the collection tree: a group header or a
// saved request in one.
type collRow struct {
	group  bool
	name   string // the group's, or the request's
	method string // a request row's
	coll   int    // the request's index in m.collections
	count  int    // a group row's member count
}

var bodyTypes = []string{"none", "json", "text", "form", "form-data"}

func bodyTypeLabel(t string) string {
	switch t {
	case "none":
		return "无"
	case "json":
		return "JSON"
	case "text":
		return "文本"
	case "form":
		return "表单"
	case "form-data":
		return "multipart"
	}
	return t
}

// methodColor is the dot color Postman gives each HTTP method.
func methodColor(method string) gfx.Color {
	switch method {
	case "GET":
		return gfx.RGB24(0x2ea44f)
	case "POST":
		return gfx.RGB24(0xe8862e)
	case "PUT":
		return gfx.RGB24(0x3b82d6)
	case "PATCH":
		return gfx.RGB24(0x18a8a0)
	case "DELETE":
		return gfx.RGB24(0xe5484d)
	}
	return gfx.RGB24(0x8b8f98)
}

// dotIcon is a filled circle in a 24-unit view box: the marker beside each
// HTTP method in the select's trigger and options.
func dotIcon(color gfx.Color) gfx.Icon {
	const k = 0.55228 // a circle is four cubic Béziers
	const cx, r = float32(12), float32(6)
	return gfx.Icon{
		ViewBox: 24,
		Layers: []gfx.IconLayer{{
			Fill: color,
			Commands: []gfx.PathCommand{
				gfx.MoveTo(cx, cx-r),
				gfx.CubicTo(cx+k*r, cx-r, cx+r, cx-k*r, cx+r, cx),
				gfx.CubicTo(cx+r, cx+k*r, cx+k*r, cx+r, cx, cx+r),
				gfx.CubicTo(cx-k*r, cx+r, cx-r, cx+k*r, cx-r, cx),
				gfx.CubicTo(cx-r, cx-k*r, cx-k*r, cx-r, cx, cx-r),
				gfx.ClosePath(),
			},
		}},
	}
}

// methodIcons is one dot per httpMethods entry, built once: icons are
// immutable values, so a paint allocates nothing.
var methodIcons = func() []gfx.Icon {
	icons := make([]gfx.Icon, len(httpMethods))
	for i, method := range httpMethods {
		icons[i] = dotIcon(methodColor(method))
	}
	return icons
}()

func newModel() *model {
	data := loadStore()
	m := &model{
		sidebar:   "收藏",
		splitSide: ui.SplitterState{Pos: 240},
		split:     ui.SplitterState{Pos: 320},
	}
	if dataPath == "" { // tests may have pinned a throwaway store already
		dataPath = loadLocationOverride() // the user's chosen directory survives restarts
	}
	m.runEff = ui.NewEffectKeyed[httpResult, *outgoing](func(ctx context.Context, o *outgoing) (httpResult, error) {
		return doRequest(ctx, o)
	}).Sequential()
	m.updateEff = ui.NewEffectKeyed[updateInfo, int](func(ctx context.Context, _ int) (updateInfo, error) {
		return fetchUpdate(ctx)
	})
	m.dlEff = ui.NewEffectKeyed[dlResult, string](func(ctx context.Context, _ string) (dlResult, error) {
		return downloadAsset(ctx, m.dlInfo)
	})
	m.collTree.Node = m.collNodeAt
	m.collNodeFn, m.collLabelFn, m.collContentFn = m.collNodeAt, m.collLabelAt, m.collRowContent
	m.jsonNodeFn, m.jsonLabelFn, m.jsonContentFn = m.jsonNodeAt, m.jsonLabelAt, m.jsonRowContent
	m.collTree.CanDrop = m.canDropColl
	m.histList.Header = m.histIsHeader
	m.applyStore(data)
	return m
}

// applyStore takes a loaded store into the model: collections, history,
// environments, groups, the theme choice and the restored tabs. newModel calls
// it once; 更改数据目录 calls it again after switching files.
func (m *model) applyStore(data storeData) {
	m.themeIdx = min(max(data.Theme, 0), len(themePresets)-1)
	m.theme = themePresets[m.themeIdx].build()
	m.collections = data.Collections
	m.history = data.History
	m.envSets = data.EnvSets
	if len(m.envSets) == 0 {
		m.envSets = []envSet{{Name: "默认", Vars: []kv{}}}
	}
	if m.envIndex(m.activeEnvName) < 0 {
		m.activeEnvName = m.envSets[0].Name
	}
	m.groups = data.Groups // empty folders live only here: they have no members to derive them from
	m.collapsed = map[string]bool{}
	m.collQuery, m.collQuerySeen = "", ""
	m.rebuildCollRows()
	m.rebuildHistRows()
	// The open tabs come back: the workspace is restored as it was left, or a
	// single blank tab when there is nothing to restore. The old tabs go
	// first — on a store switch their in-flight requests are cancelled with
	// them, which is what the reader asked for by moving the data away.
	m.tabs = nil
	m.tabSeq = 0
	if len(data.Tabs) > 0 {
		for _, st := range data.Tabs {
			if st.Editing < 0 || st.Editing >= len(m.collections) {
				st.Editing = -1 // the entry it pointed at is gone
			}
			m.newTab().restore(st)
		}
		m.tabIdx = min(max(data.ActiveTab, 0), len(m.tabs)-1)
	} else {
		m.newTab() // the window opens with one blank request tab
	}
}

// newTab appends a fresh request tab and returns it. Its send effect is its
// own: two tabs can be in flight at once, and switching tabs never cancels a
// request — the page declares every tab's effect that has one pending.
func (m *model) newTab() *tabState {
	m.tabSeq++
	// A pointer, not a value in the slice: a tabState carries ui widget states
	// (the JSON tree) whose declare callbacks pin the struct's address — a
	// slice append would copy them and strand the callbacks on the old copy.
	t := &tabState{
		id:              m.tabSeq,
		method:          "GET",
		reqTab:          "参数",
		bodyType:        "none",
		respTab:         "响应体",
		respView:        "树",
		followRedirects: true,
		editing:         -1,
		bodyCheck:       bodyCheck{valid: true}, // an empty body is valid; the zero state would render as an error
	}
	t.eff = ui.NewEffectKeyed[httpResult, *outgoing](doRequest)
	t.jsonTree.Node = m.jsonNodeAt
	m.tabs = append(m.tabs, t)
	return t
}

// openTab appends a fresh tab and makes it the one on screen.
func (m *model) openTab() *tabState {
	t := m.newTab()
	m.tabIdx = len(m.tabs) - 1
	return t
}

// closeTab removes tab i; the last tab is replaced by a fresh one, so the
// window never shows an empty editor area.
func (m *model) closeTab(i int) {
	if i < 0 || i >= len(m.tabs) {
		return
	}
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
	if len(m.tabs) == 0 {
		m.newTab()
		m.tabIdx = 0
		return
	}
	if m.tabIdx >= len(m.tabs) {
		m.tabIdx = len(m.tabs) - 1
	}
}

// closeTabByID closes the tab with the given id, whichever index it sits at
// now: the strip's rows are keyed by id, not index.
func (m *model) closeTabByID(id int) {
	for i := range m.tabs {
		if m.tabs[i].id == id {
			m.closeTab(i)
			return
		}
	}
}

// focusTabID brings the tab with the given id to the front.
func (m *model) focusTabID(id int) {
	for i := range m.tabs {
		if m.tabs[i].id == id {
			m.tabIdx = i
			return
		}
	}
}

// tabTitle names a tab: the collection entry it edits, else the request's
// host and path, else 未命名. The path stays so two tabs on one host (one at
// /users, one at /orders) are told apart; Ellipsis keeps the chip short.
func (m *model) tabTitle(t *tabState) string {
	if t.editing >= 0 && t.editing < len(m.collections) {
		return m.collections[t.editing].Name
	}
	s := strings.TrimSpace(t.url)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "/")
	if s != "" {
		return s
	}
	return "未命名"
}

// tabDirty says whether the tab's request differs from the collection entry it
// edits — the orange dot on the chip. A tab with no entry is dirty once it
// holds anything worth saving; a fresh blank one is clean.
func (m *model) tabDirty(t *tabState) bool {
	if t.editing < 0 || t.editing >= len(m.collections) {
		return t.url != "" || t.body != "" || t.bodyType != "none" ||
			len(t.formFields) > 0 || kvRowsHaveContent(t.params) || kvRowsHaveContent(t.headers)
	}
	s := m.collections[t.editing]
	return t.method != s.Method || t.url != s.URL || t.bodyType != s.BodyType || t.body != s.Body ||
		!kvRowsEqual(t.params, s.Params) || !kvRowsEqual(t.headers, s.Headers) ||
		!formRowsEqual(t.formFields, s.FormFields) || t.auth != s.Auth
}

func kvRowsHaveContent(rows []kv) bool {
	for _, r := range rows {
		if r.Key != "" || r.Value != "" {
			return true
		}
	}
	return false
}

func kvRowsEqual(a, b []kv) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value || a[i].Disabled != b[i].Disabled {
			return false
		}
	}
	return true
}

func formRowsEqual(a, b []formField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Value != b[i].Value || a[i].FilePath != b[i].FilePath ||
			a[i].IsFile != b[i].IsFile || a[i].Disabled != b[i].Disabled {
			return false
		}
	}
	return true
}

func config() app.Config {
	cfg := app.Config{
		Title:          "ApiTool — API 调试工具",
		Width:          1120,
		Height:         740,
		MinWidth:       760,
		MinHeight:      500,
		WindowMode:     app.WindowCustomTitlebar,
		TitlebarHeight: 30,
	}
	if img, err := png.Decode(bytes.NewReader(iconPNG)); err == nil {
		if n, ok := img.(*image.NRGBA); ok && n.Bounds().Dx() == n.Bounds().Dy() {
			cfg.Icon = app.WindowIcon{Size: n.Bounds().Dx(), RGBA: n.Pix}
		}
	}
	return cfg
}

func main() {
	m := newModel()
	page := ui.NewPage(ui.PageConfig{View: m.page, Theme: &m.theme})
	cfg := page.Config(config())
	m.inv = cfg.Invalidator
	var canvas gfx.Canvas
	err := app.RunCanvas(cfg, &canvas)
	// closing the window is the last chance to checkpoint the open tabs — a
	// user who never presses save still finds their workspace next launch
	m.persist()
	if err != nil {
		log.Fatal(err)
	}
}

func (m *model) invalidate() {
	if m.inv != nil {
		m.inv.Invalidate()
	}
}

// page declares the window. It runs again on every repaint, from the model.
func (m *model) page(c ui.C) {
	m.c = c
	theme := c.Theme()
	// Every tab's send effect is declared each paint, whether or not its tab
	// is on screen: stopping would cancel the request in flight. A result is
	// applied to the tab that sent it, so it waits there for its reader.
	for i := range m.tabs {
		t := m.tabs[i]
		if t.sendKey != nil {
			ui.Effect(c, t.eff, t.sendKey)
			if r, err, ok := t.eff.Take(); ok {
				m.applyResult(i, r, err)
			}
		}
	}
	if m.runKey != nil {
		ui.Effect(c, m.runEff, m.runKey)
		if r, err, ok := m.runEff.Take(); ok {
			m.advanceRun(r, err)
		}
	}
	// The update check: seq 0 is the startup check, every 检查更新 click bumps
	// the key. A startup check stays quiet unless there IS an update; a manual
	// one always reports.
	ui.Effect(c, m.updateEff, m.updateSeq)
	if info, err, ok := m.updateEff.Take(); ok {
		if err != nil {
			info.err = err.Error()
		}
		m.updateInfo = info
		if info.available {
			m.toaster.ShowAction("发现新版本 "+info.version, "点击查看详情", ui.ToastInfo, "查看详情")
		} else if m.updateSeq > 0 {
			if info.err != "" {
				m.toaster.Show("检查更新失败", info.err, ui.ToastError)
			} else {
				m.toaster.Show("已是最新版本", "当前 v"+appVersion, ui.ToastSuccess)
			}
		}
	}
	if _, acted := m.toaster.Acted(); acted {
		m.updateOpen = true
	}
	if m.dlKey != "" {
		ui.Effect(c, m.dlEff, m.dlKey)
		if r, err, ok := m.dlEff.Take(); ok {
			m.dlKey = ""
			if err != nil {
				m.toaster.Show("下载失败", err.Error(), ui.ToastError)
			} else {
				m.toaster.Show("已下载", r.path+"，关闭当前程序后替换即可完成更新", ui.ToastSuccess)
			}
		}
	}
	// The app's shortcuts: send, save, new tab, close tab, from anywhere.
	if ui.Shortcut(c, ui.Primary, "enter") {
		m.send()
	}
	if ui.Shortcut(c, ui.Primary, "s") {
		m.openSave()
	}
	if ui.Shortcut(c, ui.Primary, "t") {
		m.newRequest()
	}
	if ui.Shortcut(c, ui.Primary, "w") {
		c.After(func() { m.closeTab(m.tabIdx) })
	}
	if ui.Shortcut(c, ui.Primary, "n") {
		m.newRequest()
	}
	ui.Column(c).Fill().Children(func() {
		leading, trailing := app.TitlebarInsets()
		ui.Row(c).Height(30).PadLeft(leading).PadRight(trailing).Items(ui.Center).MovesWindow().Children(func() {
			ui.Spacer(c).Grow(1)
			ui.Text(c, "ApiTool — API 调试工具").Muted().FontSize(12)
			ui.Row(c).Grow(1).Justify(ui.End).Items(ui.Center).Children(func() {
				// The project's home and the update check sit before the theme
				// menu: titlebar controls take their own clicks despite
				// MovesWindow.
				if ui.Button(c, "GitHub").XSmall().Ghost().Icon(icoGithub).IconOnly().Tooltip(repoURL + " — 打开项目主页").Clicked() {
					c.After(func() { _ = app.OpenURL(repoURL) })
				}
				settingsBtn := ui.Button(c, "设置").XSmall().Ghost()
				if settingsBtn.Clicked() {
					m.settingsMenuOpen = true
				}
				ui.Menu(c, settingsBtn, &m.settingsMenuOpen, func(menu *ui.MenuItems) {
					if menu.Item("更改数据目录…").Clicked() {
						c.After(func() { m.openDataDirDialog() })
					}
					menu.Separator()
					if menu.Item("检查更新").Clicked() {
						c.After(func() { m.updateSeq++ })
					}
				})
				themeBtn := ui.Button(c, themePresets[m.themeIdx].name).XSmall().Ghost().Tooltip("切换主题")
				if themeBtn.Clicked() {
					m.themeMenuOpen = true
				}
				ui.Menu(c, themeBtn, &m.themeMenuOpen, func(menu *ui.MenuItems) {
					for i, p := range themePresets {
						i := i
						if menu.Item(p.name).Checked(i == m.themeIdx).Clicked() {
							m.c.After(func() { m.setTheme(i) })
						}
					}
				})
			})
		})
		ui.Row(c).Fill().Children(func() {
			// Splitter panes take no Fill: Width/Height carry the state's Pos,
			// and a Fill would take the room and ignore them.
			ui.Column(c).Width(m.splitSide.Pos).Background(theme.Card).Border(1, theme.Border).Children(func() {
				m.sidebarArea(c, theme)
			})
			ui.Splitter(c, &m.splitSide).Mins(180, 360)
			ui.Column(c).Grow(1).Children(func() {
				m.requestPanel(c, theme)
				ui.Splitter(c, &m.split).Mins(180, 180)
				m.responsePanel(c, theme)
			})
		})
	})
	ui.Toasts(c, &m.toaster)
	m.saveDialog(c)
	m.groupDialog(c)
	m.envDialog(c)
	m.confirmDialog(c)
	m.importDialog(c)
	m.updateModal(c)
	m.dataDirDialog(c)
}

// openDataDirDialog opens the switch dialog with nothing chosen yet: the
// location comes from the folder picker or the 恢复默认位置 button.
func (m *model) openDataDirDialog() {
	m.dataDirNew = ""
	m.dataDirOpen = true
}

// switchDataDir points the store at dir and reloads everything from it. The
// current state is checkpointed into the old location first, and the pointer
// file makes the choice survive restarts. An empty dir restores the default.
func (m *model) switchDataDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = storePath() // back to the default location
	}
	if !strings.HasSuffix(strings.ToLower(dir), ".json") {
		dir = filepath.Join(dir, "data.json") // the dialog asks for a directory
	}
	if dir == storeFile() {
		return nil
	}
	m.persist() // checkpoint into the old location
	if dir == storePath() {
		saveLocationOverride("")
	} else {
		saveLocationOverride(dir)
	}
	dataPath = dir
	m.applyStore(loadStore())
	return nil
}

// dataDirDialog asks where the store should live and moves the app over. The
// new location comes from the system folder picker — nothing to type.
func (m *model) dataDirDialog(c ui.C) {
	ui.Modal(c, &m.dataDirOpen, "数据目录", func() {
		theme := c.Theme()
		ui.Column(c).Gap(10).Children(func() {
			ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
				ui.Text(c, "当前").Muted()
				ui.Text(c, storeFile()).Mono().FontSize(12).Grow(1).Ellipsis()
			})
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "选择文件夹…").Icon(icoFolder).Clicked() {
					if dir, err := app.OpenDirectory(app.OpenDirectoryOptions{Title: "选择数据目录"}); err == nil && dir != "" {
						picked := filepath.Join(dir, "data.json")
						c.After(func() {
							m.dataDirNew = picked
							if m.inv != nil {
								m.inv.Invalidate()
							}
						})
					}
				}
				if ui.Button(c, "恢复默认位置").Small().Ghost().Clicked() {
					c.After(func() {
						m.dataDirNew = storePath()
						if m.inv != nil {
							m.inv.Invalidate()
						}
					})
				}
			})
			// what the picker chose, or a hint that nothing is chosen yet
			if m.dataDirNew != "" {
				ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
					ui.Text(c, "✓").Color(theme.Success).Bold()
					ui.Text(c, m.dataDirNew).Mono().FontSize(12).Grow(1).Ellipsis()
				})
			} else {
				ui.Text(c, "先从电脑里选一个文件夹，data.json 会放在里面。").Muted().FontSize(12)
			}
			ui.Text(c, "切换后立即从新位置读取，原目录的文件保留不动。").Muted().FontSize(12)
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.dataDirOpen = false
			}
			if ui.Button(c, "切换并重新加载").Disabled(m.dataDirNew == "").Clicked() {
				m.dataDirOpen = false
				c.After(func() {
					if err := m.switchDataDir(m.dataDirNew); err != nil {
						m.toaster.Show("切换失败", err.Error(), ui.ToastError)
						return
					}
					m.toaster.Show("数据目录已切换", storeFile(), ui.ToastSuccess)
				})
			}
		})
	})
}

// plainNotes strips the Markdown a release body is written in down to what a
// Paragraph can show, and caps its length.
func plainNotes(notes string, limit int) string {
	var lines []string
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.ReplaceAll(line, "**", "")
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				continue
			}
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n")
	if runes := []rune(out); len(runes) > limit {
		out = string(runes[:limit]) + "…"
	}
	return out
}

// updateModal shows a found release: what changed, and two ways to get it —
// the release page in a browser, or the asset downloaded into Downloads.
func (m *model) updateModal(c ui.C) {
	ui.Modal(c, &m.updateOpen, "发现新版本", func() {
		info := m.updateInfo
		ui.Column(c).Gap(10).Children(func() {
			ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
				ui.Text(c, "当前版本").Muted()
				ui.Text(c, "v"+appVersion).Mono()
				ui.Text(c, "→").Muted()
				ui.Text(c, info.version).Mono().Bold().Color(c.Theme().Primary)
			})
			if info.notes != "" {
				notes := plainNotes(info.notes, 600)
				ui.Paragraph(c, notes, 6).Muted()
			}
			if info.assetName != "" {
				ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
					ui.Text(c, "安装包").Muted()
					ui.Text(c, info.assetName).Mono()
					if info.size > 0 {
						ui.Text(c, "("+formatSize(int(info.size))+")").Muted().FontSize(12)
					}
				})
			}
			if m.dlKey != "" {
				ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "下载中…").Muted().FontSize(12)
				})
			}
		})
	}, func() {
		info := m.updateInfo
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if info.url != "" {
				if ui.Button(c, "打开发布页").Outline().Clicked() {
					c.After(func() { _ = app.OpenURL(info.url) })
				}
			}
			if info.assetURL != "" && m.dlKey == "" {
				if ui.Button(c, "下载安装包").Clicked() {
					m.dlSeq++
					info := m.updateInfo
					c.After(func() { m.dlInfo, m.dlKey = info, fmt.Sprintf("%s#%d", info.assetURL, m.dlSeq) })
				}
			}
			if ui.Button(c, "关闭").Ghost().Clicked() {
				m.updateOpen = false
			}
		})
	})
}

// importDialog pastes a cURL command and parses it into the editor.
func (m *model) importDialog(c ui.C) {
	ui.Modal(c, &m.importOpen, "导入 cURL", func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "粘贴 cURL 命令（支持 bash 与 cmd 的引号和续行）：").Muted()
			ui.TextArea(c, &m.importText, "cURL命令", "curl 'https://api.example.com/users' -H 'Content-Type: application/json'").Height(160).Mono()
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.importOpen = false
			}
			if ui.Button(c, "解析并导入").Clicked() {
				req, err := parseCurl(m.importText)
				if err != nil {
					m.toaster.Show("导入失败", err.Error(), ui.ToastError)
					return
				}
				m.importOpen = false
				m.applyCurlImport(req)
			}
		})
	})
}

// applyCurlImport fills the editor from a parsed cURL command; a save then
// stores it like any new request.
func (m *model) applyCurlImport(req curlRequest) {
	m.newRequest()
	m.t().method = req.method
	m.t().url = req.url
	m.t().params = append([]kv(nil), req.params...)
	m.t().headers = append([]kv(nil), req.headers...)
	m.t().body = req.body
	m.t().bodyType = req.bodyType
	if m.t().bodyType == "" {
		m.t().bodyType = "none"
	}
	m.t().formFields = append([]formField(nil), req.formFields...)
	m.t().followRedirects = req.followRedirects
	m.t().checkBodyJSON()
}

// confirmDialog asks before wiping the history.
func (m *model) confirmDialog(c ui.C) {
	ui.Modal(c, &m.confirmOpen, "清空历史", func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "确定要清空全部历史记录吗？此操作无法撤销。").Muted()
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.confirmOpen = false
			}
			if ui.Button(c, "清空").Destructive().Clicked() {
				m.confirmOpen = false
				m.clearHistory()
			}
		})
	})
}

// ---- sidebar ----

func (m *model) sidebarArea(c ui.C, theme *ui.Theme) {
	ui.Column(c).Fill().Children(func() {
		ui.Tabs(c, &m.sidebar, []string{"收藏", "历史", "环境"}, nil)
		switch m.sidebar {
		case "收藏":
			m.collectionList(c, theme)
		case "历史":
			m.historyList(c, theme)
		case "环境":
			m.envPanel(c, theme)
		}
	})
}

// emptyState paints a centered icon badge over a title and a hint, for the
// lists and panels that start out empty.
func emptyState(c ui.C, theme *ui.Theme, glyph gfx.Icon, title, hint string) {
	ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
		ui.Row(c).Size(44, 44).Radius(22).Background(theme.Muted).Center().Children(func() {
			ui.Icon(c, glyph)
		})
		ui.Text(c, title).FontSize(14)
		ui.Text(c, hint).Muted().FontSize(12)
	})
}

// statChip paints one small response stat as a quiet pill.
func statChip(c ui.C, theme *ui.Theme, text string) {
	if text == "" {
		return
	}
	ui.Row(c).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).Background(theme.Muted).Center().Children(func() {
		ui.Text(c, text).Mono().FontSize(12).Muted()
	})
}

// collectionList declares the saved requests as a tree: one collapsible row
// per group, its requests under it, with a search field over them.
func (m *model) collectionList(c ui.C, theme *ui.Theme) {
	if m.collQuery != m.collQuerySeen {
		m.collQuerySeen = m.collQuery
		m.rebuildCollRows()
	}
	if len(m.collections) == 0 && len(m.groups) == 0 {
		emptyState(c, theme, icoFolder, "暂无收藏", "保存一个请求后，它会出现在这里")
		return
	}
	// no Grow: in a Column it would stretch the field down the sidebar
	ui.TextField(c, &m.collQuery, "搜索", "搜索接口名").Icon(icoSearch)
	ui.Row(c).Gap(8).Children(func() {
		// Icon-only: labelled buttons overflow the 240px sidebar; the tooltips
		// carry the names.
		importBtn := ui.Button(c, "导入集合").Small().Ghost().Icon(icoDownload).IconOnly().Tooltip("导入 cURL 命令或集合文件")
		if importBtn.Clicked() {
			m.importMenuOpen = true
		}
		ui.Menu(c, importBtn, &m.importMenuOpen, func(menu *ui.MenuItems) {
			if menu.Item("cURL 命令…").Clicked() {
				m.c.After(func() { m.importOpen = true })
			}
			if menu.Item("集合 JSON 文件…").Clicked() {
				m.c.After(func() { m.importCollectionsDialog() })
			}
		})
		if ui.Button(c, "导出集合").Small().Ghost().Icon(icoUpload).IconOnly().Tooltip("导出集合到 JSON 文件").Clicked() {
			m.c.After(func() { m.exportCollectionsToFile() })
		}
		if ui.Button(c, "新建分组").Small().Ghost().Icon(icoPlus).IconOnly().Tooltip("新建分组").Clicked() {
			m.openNewGroupDialog("")
		}
		ui.Spacer(c)
		if ui.Button(c, "运行全部").Small().Outline().Icon(icoPlay).IconOnly().Tooltip("运行全部分组").Clicked() {
			m.startRun(allIndices(len(m.collections)))
		}
	})
	ui.Tree(c, &m.collTree, len(m.collRows), m.collLabelFn, m.collContentFn).Grow(1)
	if i, ok := m.collTree.Toggled(); ok {
		c.After(func() { m.toggleCollRow(i) })
	}
	if i, ok := m.collTree.Chosen(); ok {
		c.After(func() { m.chooseCollRow(i) })
	}
	if mv, ok := m.collTree.Dropped(); ok {
		c.After(func() { m.dropCollRow(mv) })
	}
}

// ---- collection groups ----

// groupNames lists every folder path in order, plus any a legacy file only
// references through its members. A path carries its nesting: 用户服务/管理端
// is 管理端 inside 用户服务.
func (m *model) groupNames() []string {
	names := append([]string(nil), m.groups...)
	seen := map[string]bool{}
	for _, g := range m.groups {
		seen[g] = true
	}
	for _, s := range m.collections {
		if s.Group != "" && !seen[s.Group] {
			seen[s.Group] = true
			names = append(names, s.Group)
		}
	}
	return names
}

// parentOf is the path of the folder a path sits in, "" for the root level.
func parentOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return ""
}

// joinPath is a folder's path under parent.
func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// rebuildCollRows flattens the folder tree: each folder row is followed by its
// subfolders and its requests (the way Postman nests collections), and the
// requests without a folder sit at the root.
func (m *model) rebuildCollRows() {
	// Searching flattens the tree: every request whose name matches, no folders.
	if q := strings.ToLower(strings.TrimSpace(m.collQuery)); q != "" {
		var rows []collRow
		for i, s := range m.collections {
			if strings.Contains(strings.ToLower(s.Name), q) {
				rows = append(rows, collRow{name: s.Name, method: s.Method, coll: i})
			}
		}
		m.collRows = rows
		return
	}
	// the subfolders of each parent, in the folders' own order
	children := map[string][]string{}
	for _, g := range m.groupNames() {
		children[parentOf(g)] = append(children[parentOf(g)], g)
	}
	rows := make([]collRow, 0, len(m.collections)+len(m.groups))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		// each level: its folders first, each walked with its own subtree,
		// then the requests that sit directly in this folder
		for _, g := range children[parent] {
			var members []int
			for i, s := range m.collections {
				if s.Group == g {
					members = append(members, i)
				}
			}
			rows = append(rows, collRow{group: true, name: g, count: len(members)})
			if !m.collapsed[g] {
				walk(g, depth+1)
			}
		}
		for i, s := range m.collections {
			if s.Group == parent {
				rows = append(rows, collRow{name: s.Name, method: s.Method, coll: i})
			}
		}
	}
	walk("", 0)
	m.collRows = rows
}

// groupDepth is how deep a folder path nests: 用户服务 is 0, 用户服务/管理端 is 1.
func groupDepth(path string) int { return strings.Count(path, "/") }

func (m *model) collNodeAt(i int) ui.TreeNode {
	if i < 0 || i >= len(m.collRows) {
		return ui.TreeNode{}
	}
	r := &m.collRows[i]
	if r.group {
		return ui.TreeNode{Depth: groupDepth(r.name), Branch: true, Expanded: !m.collapsed[r.name]}
	}
	depth := 0
	if r.coll >= 0 && r.coll < len(m.collections) && m.collections[r.coll].Group != "" {
		// a request in a folder sits one level under it; a root request is 0
		depth = groupDepth(m.collections[r.coll].Group) + 1
	}
	return ui.TreeNode{Depth: depth}
}

func (m *model) collLabelAt(i int) string {
	if i < 0 || i >= len(m.collRows) {
		return ""
	}
	return m.collRows[i].name
}

// collRowContent paints a tree row: a folder glyph, a group's name and count,
// or a request's method and name. The name is a label of the row, so a click,
// the hover and a right click reach the row, and the context menu covers all
// of it.
func (m *model) collRowContent(i int) {
	if i < 0 || i >= len(m.collRows) {
		return
	}
	r := m.collRows[i]
	var label ui.Box
	// PadLeft keeps a root-level request's mark off the panel edge; rows under
	// a folder are already indented by the tree.
	ui.Row(m.c).PadLeft(4).Gap(8).Items(ui.Center).Children(func() {
		if r.group {
			ui.Icon(m.c, icoFolder)
			// a nested folder's path is its address; the tree shows the name
			label = ui.Text(m.c, baseName(r.name)).Grow(1).Ellipsis().LabelFor(m.collTree.RowID(i))
			ui.Text(m.c, strconv.Itoa(r.count)).Muted().FontSize(12)
		} else {
			ui.Text(m.c, r.method).Mono().FontSize(11).Color(methodColor(r.method)).Width(52)
			label = ui.Text(m.c, r.name).Grow(1).Ellipsis().LabelFor(m.collTree.RowID(i))
		}
	})
	if !label.Valid() {
		return
	}
	ui.ContextMenu(m.c, label, func(menu *ui.MenuItems) {
		if r.group {
			m.groupMenuItems(menu, r.name)
		} else {
			m.requestMenuItems(menu, r.coll)
		}
	})
}

// toggleCollRow folds or unfolds the group of visible row i.
func (m *model) toggleCollRow(i int) {
	if i < 0 || i >= len(m.collRows) || !m.collRows[i].group {
		return
	}
	m.toggleGroup(m.collRows[i].name)
}

func (m *model) toggleGroup(name string) {
	if m.collapsed[name] {
		delete(m.collapsed, name)
	} else {
		m.collapsed[name] = true
	}
	m.rebuildCollRows()
}

// chooseCollRow opens a request, or folds a group when one is clicked.
func (m *model) chooseCollRow(i int) {
	if i < 0 || i >= len(m.collRows) {
		return
	}
	r := m.collRows[i]
	if r.group {
		m.toggleGroup(r.name)
		return
	}
	m.loadCollection(r.coll)
}

// canDropColl says which drags the tree takes: a request becomes the sibling
// of another request or joins a folder (at any depth); a folder is reordered
// beside a folder of the same parent or nested into another one it is not
// already inside of.
func (m *model) canDropColl(from, to int, where ui.TreeDrop) bool {
	if from < 0 || from >= len(m.collRows) || to < 0 || to >= len(m.collRows) || from == to {
		return false
	}
	src, dst := m.collRows[from], m.collRows[to]
	switch {
	case !src.group && !dst.group:
		return where == ui.TreeDropBefore || where == ui.TreeDropAfter
	case !src.group && dst.group:
		return where == ui.TreeDropInside && m.collections[src.coll].Group != dst.name
	case src.group && dst.group && where == ui.TreeDropInside:
		// nesting a folder into itself or its own subtree would lose it
		return dst.name != src.name && !strings.HasPrefix(dst.name, src.name+"/")
	case src.group && dst.group:
		return parentOf(src.name) == parentOf(dst.name) &&
			(where == ui.TreeDropBefore || where == ui.TreeDropAfter)
	}
	return false // a folder never drops onto a request
}

// dropCollRow applies the drop the tree reports.
func (m *model) dropCollRow(mv ui.TreeMove) {
	if mv.From < 0 || mv.From >= len(m.collRows) || mv.To < 0 || mv.To >= len(m.collRows) {
		return
	}
	src, dst := m.collRows[mv.From], m.collRows[mv.To]
	switch {
	case !src.group && !dst.group:
		m.reorderCollTo(src.coll, dst.coll, mv.Where == ui.TreeDropBefore)
	case !src.group && dst.group && mv.Where == ui.TreeDropInside:
		m.moveCollTo(src.coll, dst.name)
	case src.group && dst.group && mv.Where == ui.TreeDropInside:
		m.moveFolderTo(src.name, dst.name)
	case src.group && dst.group:
		m.reorderGroupTo(src.name, dst.name, mv.Where == ui.TreeDropBefore)
	}
}

// moveFolderTo nests a folder (and everything inside it) under another one,
// rewriting the paths of the folder, its subfolders and its requests, and
// keeping the moved block together right after its new parent.
func (m *model) moveFolderTo(folder, parent string) {
	if parent == folder || strings.HasPrefix(parent, folder+"/") {
		return // nesting a folder into itself would lose it
	}
	newPath := joinPath(parent, baseName(folder))
	// collect the folder's block — itself and its subfolders, renamed — and
	// keep every other folder in its order
	var block, kept []string
	for _, g := range m.groups {
		if g == folder || strings.HasPrefix(g, folder+"/") {
			block = append(block, newPath+strings.TrimPrefix(g, folder))
			continue
		}
		kept = append(kept, g)
	}
	for i, s := range m.collections {
		if s.Group == folder || strings.HasPrefix(s.Group, folder+"/") {
			m.collections[i].Group = newPath + strings.TrimPrefix(s.Group, folder)
		}
	}
	// the block lands right after the new parent's own subtree
	at := len(kept)
	if p := m.groupIndex(parent); p >= 0 {
		at = p + 1
		for at < len(kept) && (kept[at] == parent || strings.HasPrefix(kept[at], parent+"/")) {
			at++
		}
		if at > len(kept) {
			at = len(kept)
		}
	}
	kept = append(kept[:at], append(block, kept[at:]...)...)
	m.groups = kept
	delete(m.collapsed, folder)
	m.rebuildCollRows()
	m.persist()
}

// reorderCollTo moves a request beside another one, taking its folder; the
// editor's entry index follows the move.
func (m *model) reorderCollTo(from, to int, before bool) {
	if from < 0 || from >= len(m.collections) || to < 0 || to >= len(m.collections) || from == to {
		return
	}
	s := m.collections[from]
	s.Group = m.collections[to].Group // the moved request joins the target's folder
	m.collections = append(m.collections[:from], m.collections[from+1:]...)
	t := to
	if to > from {
		t--
	}
	if !before {
		t++
	}
	m.collections = append(m.collections[:t], append([]savedRequest{s}, m.collections[t:]...)...)
	for k := range m.tabs { // every tab's entry index follows the shift
		e := m.tabs[k].editing
		if e == from {
			m.tabs[k].editing = t
		} else if from < e && e <= t {
			m.tabs[k].editing--
		} else if t <= e && e < from {
			m.tabs[k].editing++
		}
	}
	m.rebuildCollRows()
	m.persist()
}

// reorderGroupTo moves a folder beside another one of the same parent.
func (m *model) reorderGroupTo(fromName, toName string, before bool) {
	if parentOf(fromName) != parentOf(toName) {
		return
	}
	f, t := m.groupIndex(fromName), m.groupIndex(toName)
	if f < 0 || t < 0 || f == t {
		return
	}
	g := m.groups[f]
	m.groups = append(m.groups[:f], m.groups[f+1:]...)
	if t > f {
		t--
	}
	if !before {
		t++
	}
	m.groups = append(m.groups[:t], append([]string{g}, m.groups[t:]...)...)
	m.rebuildCollRows()
	m.persist()
}

// requestMenuItems is a saved request's context menu.
func (m *model) requestMenuItems(menu *ui.MenuItems, coll int) {
	if menu.Item("打开").Icon(icoFolder).Clicked() {
		m.c.After(func() { m.loadCollection(coll) })
	}
	if menu.Item("运行").Icon(icoPlay).Clicked() {
		m.c.After(func() { m.startRun([]int{coll}) })
	}
	if menu.Item("复制接口").Icon(icoCopy).Clicked() {
		m.c.After(func() { m.duplicateCollection(coll) })
	}
	// 复制为 copies the ROW's request, not the editor's content
	menu.Sub("复制为", func(to *ui.MenuItems) {
		for _, lang := range codeLangs {
			lang := lang
			if to.Item(langLabel(lang)).Clicked() {
				m.c.After(func() {
					code := generateCode(lang, toOutgoing(m.collections[coll], m.t().followRedirects, m.activeEnvs()), m.t().followRedirects)
					if err := app.SetClipboardText(code); err != nil {
						m.toaster.Show("复制失败", err.Error(), ui.ToastError)
						return
					}
					m.toaster.Show("已复制", langLabel(lang)+" 已复制到剪贴板", ui.ToastSuccess)
				})
			}
		}
	})
	if menu.Item("重命名").Icon(icoPencil).Clicked() {
		m.c.After(func() {
			m.loadCollection(coll)
			m.openSave()
		})
	}
	menu.Sub("移动到分组", func(to *ui.MenuItems) {
		current := ""
		if coll >= 0 && coll < len(m.collections) {
			current = m.collections[coll].Group
		}
		if current != "" {
			if to.Item("移出分组").Clicked() {
				m.c.After(func() { m.moveCollTo(coll, "") })
			}
		}
		for _, g := range m.groupNames() {
			gg := g
			if to.Item(gg).Checked(gg == current).Clicked() {
				m.c.After(func() { m.moveCollTo(coll, gg) })
			}
		}
	})
	menu.Separator()
	if menu.Item("删除").Icon(icoTrash).Destructive().Clicked() {
		m.c.After(func() { m.deleteCollection(coll) })
	}
}

// groupMenuItems is a folder row's context menu. Running a folder runs its
// requests and everything inside its subfolders.
func (m *model) groupMenuItems(menu *ui.MenuItems, name string) {
	if menu.Item("新建接口").Icon(icoPlus).Clicked() {
		m.c.After(func() { m.newInGroup(name) })
	}
	if menu.Item("新建子分组").Icon(icoFolder).Clicked() {
		m.c.After(func() { m.openNewGroupDialog(name) })
	}
	if menu.Item("运行分组").Icon(icoPlay).Clicked() {
		var members []int
		for i, s := range m.collections {
			if s.Group == name || strings.HasPrefix(s.Group, name+"/") {
				members = append(members, i)
			}
		}
		m.c.After(func() { m.startRun(members) })
	}
	if name == "未分组" {
		return
	}
	if menu.Item("重命名").Clicked() {
		m.c.After(func() { m.openGroupDialog(name) })
	}
	if menu.Item("删除分组").Icon(icoTrash).Destructive().Clicked() {
		m.c.After(func() { m.deleteGroup(name) })
	}
}

func (m *model) groupIndex(name string) int {
	for i, g := range m.groups {
		if g == name {
			return i
		}
	}
	return -1
}

// openNewGroupDialog opens the 新建分组 dialog for a folder nested under
// parent (an empty parent makes a root-level folder).
func (m *model) openNewGroupDialog(parent string) {
	m.groupRename = ""
	m.groupParent = parent
	m.groupName = ""
	m.groupOpen = true
}

// openGroupDialog opens the 重命名分组 dialog for the folder at path old.
func (m *model) openGroupDialog(old string) {
	m.groupRename = old
	m.groupParent = parentOf(old)
	m.groupName = baseName(old)
	m.groupOpen = true
}

// confirmGroupDialog applies the group dialog: create a folder under the
// dialog's parent, or rename one — subfolders and members follow the new path.
func (m *model) confirmGroupDialog() {
	name := strings.TrimSpace(m.groupName)
	if name == "" {
		m.toaster.Show("缺少名称", "请输入分组名称", ui.ToastError)
		return
	}
	if name == "未分组" {
		m.toaster.Show("保留名称", "未分组 是系统分组，请换个名称", ui.ToastError)
		return
	}
	if strings.Contains(name, "/") {
		m.toaster.Show("名称有误", "分组名称里不能有 /", ui.ToastError)
		return
	}
	path := joinPath(m.groupParent, name)
	if m.groupRename == "" {
		if m.groupIndex(path) >= 0 {
			m.toaster.Show("已有同名分组", name, ui.ToastError)
			return
		}
		m.groups = append(m.groups, path)
	} else if path != m.groupRename {
		if m.groupIndex(path) >= 0 {
			m.toaster.Show("已有同名分组", name, ui.ToastError)
			return
		}
		// A folder a legacy file only references becomes a listed one here,
		// and everything under the old path follows the new one.
		if i := m.groupIndex(m.groupRename); i >= 0 {
			m.groups[i] = path
		} else {
			m.groups = append(m.groups, path)
		}
		for i, g := range m.groups {
			if strings.HasPrefix(g, m.groupRename+"/") {
				m.groups[i] = path + strings.TrimPrefix(g, m.groupRename)
			}
		}
		for i, s := range m.collections {
			if s.Group == m.groupRename {
				m.collections[i].Group = path
			} else if strings.HasPrefix(s.Group, m.groupRename+"/") {
				m.collections[i].Group = path + strings.TrimPrefix(s.Group, m.groupRename)
			}
		}
		if m.collapsed[m.groupRename] {
			delete(m.collapsed, m.groupRename)
			m.collapsed[path] = true
		}
	}
	m.groupRename = ""
	m.groupParent = ""
	m.groupOpen = false
	m.rebuildCollRows()
	m.persist()
	m.toaster.Show("已保存", name, ui.ToastSuccess)
}

// deleteGroup removes a folder and its subfolders; their requests fall back
// to the folder's own parent, or to the root.
func (m *model) deleteGroup(path string) {
	if m.groupIndex(path) < 0 {
		return
	}
	fallback := parentOf(path)
	var kept []string
	for _, g := range m.groups {
		if g != path && !strings.HasPrefix(g, path+"/") {
			kept = append(kept, g)
		}
	}
	m.groups = kept
	for j, s := range m.collections {
		if s.Group == path || strings.HasPrefix(s.Group, path+"/") {
			m.collections[j].Group = fallback
		}
	}
	delete(m.collapsed, path)
	for c := range m.collapsed {
		if strings.HasPrefix(c, path+"/") {
			delete(m.collapsed, c)
		}
	}
	m.rebuildCollRows()
	m.persist()
	m.toaster.Show("已删除分组", baseName(path), ui.ToastSuccess)
}

// moveCollTo puts a saved request in a group; an empty name moves it to the
// tree's root. The target unfolds, so what was just moved is visible.
func (m *model) moveCollTo(coll int, group string) {
	if coll < 0 || coll >= len(m.collections) || m.collections[coll].Group == group {
		return
	}
	m.collections[coll].Group = group
	if group != "" {
		delete(m.collapsed, group)
	}
	m.rebuildCollRows()
	m.persist()
}

// groupDialog names a new folder, or a new name for one.
func (m *model) groupDialog(c ui.C) {
	title := "新建分组"
	if m.groupRename != "" {
		title = "重命名分组"
	}
	ui.Modal(c, &m.groupOpen, title, func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "分组名称：").Muted()
			ui.TextField(c, &m.groupName, "名称", "例如 用户服务").AutoFocus()
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.groupOpen = false
			}
			if ui.Button(c, "确定").Clicked() {
				m.confirmGroupDialog()
			}
		})
	})
}

// ---- environments ----

func (m *model) envIndex(name string) int {
	for i, e := range m.envSets {
		if e.Name == name {
			return i
		}
	}
	return -1
}

// newEnvSet appends a set and makes it the active one.
func (m *model) newEnvSet() {
	m.envSets = append(m.envSets, envSet{Name: "环境 " + strconv.Itoa(len(m.envSets)+1), Vars: []kv{}})
	m.activeEnvName = m.envSets[len(m.envSets)-1].Name
	m.persist()
}

// openEnvDialog opens the 重命名环境 dialog for the set called old.
func (m *model) openEnvDialog(old string) {
	m.envOld = old
	m.envName = old
	m.envOpen = true
}

// confirmEnvDialog applies the rename: the active environment follows the new
// name, duplicates are rejected.
func (m *model) confirmEnvDialog() {
	name := strings.TrimSpace(m.envName)
	if name == "" {
		m.toaster.Show("缺少名称", "请输入环境名称", ui.ToastError)
		return
	}
	i := m.envIndex(m.envOld)
	if i < 0 {
		m.envOpen = false
		return
	}
	if name != m.envOld {
		for j, e := range m.envSets {
			if j != i && e.Name == name {
				m.toaster.Show("已有同名环境", name, ui.ToastError)
				return
			}
		}
		m.envSets[i].Name = name
		if m.activeEnvName == m.envOld {
			m.activeEnvName = name
		}
	}
	m.envOpen = false
	m.persist()
	m.toaster.Show("已重命名", name, ui.ToastSuccess)
}

// envDialog renames the environment set the dialog was opened for.
func (m *model) envDialog(c ui.C) {
	ui.Modal(c, &m.envOpen, "重命名环境", func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "环境名称：").Muted()
			ui.TextField(c, &m.envName, "名称", "例如 开发").AutoFocus()
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.envOpen = false
			}
			if ui.Button(c, "确定").Clicked() {
				m.confirmEnvDialog()
			}
		})
	})
}

// histRow is one visible row of the history list: a date heading or one
// history entry.
type histRow struct {
	header bool
	label  string
	idx    int
}

// histDay sorts a saved timestamp into its heading: 今天, 昨天, or the date.
// Entries saved before the timestamps carried a date show as 更早.
func histDay(ts string) string {
	if len(ts) < 10 || ts[4] != '-' {
		return "更早"
	}
	switch day := ts[:10]; day {
	case time.Now().Format("2006-01-02"):
		return "今天"
	case time.Now().AddDate(0, 0, -1).Format("2006-01-02"):
		return "昨天"
	default:
		return day
	}
}

// histTime is the clock part of a saved timestamp.
func histTime(ts string) string {
	if len(ts) > 10 {
		return ts[11:]
	}
	return ts
}

// rebuildHistRows flattens the history into rows, a heading before each day.
func (m *model) rebuildHistRows() {
	m.histRows = m.histRows[:0]
	label := ""
	for i, s := range m.history {
		if d := histDay(s.Time); d != label {
			label = d
			m.histRows = append(m.histRows, histRow{header: true, label: d})
		}
		m.histRows = append(m.histRows, histRow{idx: i})
	}
}

func (m *model) histIsHeader(i int) bool {
	return i >= 0 && i < len(m.histRows) && m.histRows[i].header
}

func (m *model) historyList(c ui.C, theme *ui.Theme) {
	if len(m.history) == 0 {
		emptyState(c, theme, icoClock, "暂无历史", "发送请求后，在这里回看每一次调用")
		return
	}
	ui.Row(c).Justify(ui.End).Children(func() {
		if ui.Button(c, "清空历史").Small().Ghost().Clicked() {
			m.confirmOpen = true
		}
	})
	ui.List(c, &m.histList, len(m.histRows), func(row ui.ListRow) {
		h := m.histRows[row.Index]
		if h.header {
			row.Element.PadX(10).PadY(6).Background(theme.Muted).Children(func() {
				ui.Text(row.C, h.label).Muted().FontSize(12)
			})
			return
		}
		s := m.history[h.idx]
		var label ui.Box
		row.Element.PadX(8).Gap(8).Items(ui.Center).Children(func() {
			label = ui.Text(row.C, s.Name).Grow(1).Ellipsis().LabelFor(m.histList.RowID(row.Index))
			ui.Text(row.C, s.Method).Mono().FontSize(11).Color(methodColor(s.Method)).Width(52)
			ui.Text(row.C, histTime(s.Time)).Mono().FontSize(11).Muted()
		})
		if !label.Valid() {
			return
		}
		ui.ContextMenu(row.C, label, func(menu *ui.MenuItems) {
			if menu.Item("打开").Icon(icoFolder).Clicked() {
				i := h.idx
				m.c.After(func() { m.loadHistory(i) })
			}
			menu.Separator()
			if menu.Item("删除").Icon(icoTrash).Destructive().Clicked() {
				i := h.idx
				m.c.After(func() { m.deleteHistoryEntry(i) })
			}
		})
	}).Label("历史列表").Grow(1)
	if row, ok := m.histList.Submitted(); ok {
		if row >= 0 && row < len(m.histRows) && !m.histRows[row].header {
			i := m.histRows[row].idx
			c.After(func() { m.loadHistory(i) })
		}
	}
}

// deleteHistoryEntry removes one history entry.
func (m *model) deleteHistoryEntry(i int) {
	if i < 0 || i >= len(m.history) {
		return
	}
	m.history = append(m.history[:i], m.history[i+1:]...)
	m.rebuildHistRows()
	m.persist()
}

// clearHistory wipes every history entry.
func (m *model) clearHistory() {
	m.history = nil
	m.rebuildHistRows()
	m.persist()
}

// collectionExport is the portable JSON document of a collection backup: the
// folders (as paths) and the saved requests.
type collectionExport struct {
	Version     int            `json:"version"`
	App         string         `json:"app"`
	Groups      []string       `json:"groups"`
	Collections []savedRequest `json:"collections"`
}

// exportCollectionsJSON builds the portable document for the folders and the
// saved requests.
func (m *model) exportCollectionsJSON() []byte {
	doc := collectionExport{
		Version:     1,
		App:         "apitool",
		Groups:      m.groupNames(),
		Collections: append([]savedRequest(nil), m.collections...),
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return b
}

// importCollectionsJSON merges an exported document: folders that do not exist
// yet are appended, every request is added, and the counts are returned.
func (m *model) importCollectionsJSON(data []byte) (int, int, error) {
	var doc collectionExport
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, 0, err
	}
	newGroups, newRequests := 0, 0
	existing := map[string]bool{}
	for _, g := range m.groupNames() {
		existing[g] = true
	}
	for _, g := range doc.Groups {
		if g != "" && !existing[g] {
			existing[g] = true
			m.groups = append(m.groups, g)
			newGroups++
		}
	}
	for _, s := range doc.Collections {
		if s.Group != "" && !existing[s.Group] {
			existing[s.Group] = true
			m.groups = append(m.groups, s.Group)
			newGroups++
		}
		m.collections = append(m.collections, s)
		newRequests++
	}
	m.rebuildCollRows()
	m.persist()
	return newGroups, newRequests, nil
}

// importCollectionsDialog offers the file picker for a collection backup.
func (m *model) importCollectionsDialog() {
	files, err := app.OpenFiles(app.OpenFileOptions{
		Title:   "导入集合",
		Filters: []app.FileFilter{{Name: "JSON", Pattern: "*.json"}},
	})
	if err != nil {
		if errors.Is(err, app.ErrCanceled) {
			return
		}
		m.toaster.Show("导入失败", err.Error(), ui.ToastError)
		return
	}
	if len(files) == 0 {
		return
	}
	m.importCollectionsFromFile(files[0])
}

// importCollectionsFromFile reads a collection backup and merges it.
func (m *model) importCollectionsFromFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		m.toaster.Show("导入失败", err.Error(), ui.ToastError)
		return
	}
	newGroups, newRequests, err := m.importCollectionsJSON(data)
	if err != nil {
		m.toaster.Show("导入失败", "文件不是有效的集合备份", ui.ToastError)
		return
	}
	m.toaster.Show("已导入", fmt.Sprintf("%d 个接口、%d 个分组", newRequests, newGroups), ui.ToastSuccess)
}

// exportCollectionsToFile offers a save picker and writes the document there.
func (m *model) exportCollectionsToFile() {
	path, err := app.SaveFile(app.SaveFileOptions{
		Title:            "导出集合",
		SuggestedName:    "apitool-collections.json",
		DefaultExtension: ".json",
	})
	if err != nil {
		if errors.Is(err, app.ErrCanceled) {
			return
		}
		m.toaster.Show("导出失败", err.Error(), ui.ToastError)
		return
	}
	if path == "" {
		return
	}
	if err := os.WriteFile(path, m.exportCollectionsJSON(), 0o644); err != nil {
		m.toaster.Show("导出失败", err.Error(), ui.ToastError)
		return
	}
	m.toaster.Show("已导出", path, ui.ToastSuccess)
}

func (m *model) envPanel(c ui.C, theme *ui.Theme) {
	idx := m.activeEnvIndex()
	ui.Column(c).Grow(1).Gap(8).Children(func() {
		ui.Paragraph(c, "在 URL、参数与请求头中用 {{名称}} 引用变量；切换环境在地址栏右侧。", 2).Muted().FontSize(12)
		// The env is managed here: the ⋯ menu makes, renames or deletes the
		// set this tab is editing. The row itself must not grow — it would
		// split the tab's height with the table below.
		ui.Row(c).Items(ui.Center).Children(func() {
			ui.Text(c, "正在编辑环境 "+m.envSets[idx].Name).Grow(1).FontSize(13).Ellipsis()
			more := ui.Button(c, "环境操作").Small().Ghost().Icon(icoMore).IconOnly().Tooltip("新建、重命名或删除环境")
			if more.Clicked() {
				m.envMenuOpen = true
			}
			ui.Menu(c, more, &m.envMenuOpen, func(menu *ui.MenuItems) {
				if menu.Item("新建环境").Icon(icoPlus).Clicked() {
					m.c.After(func() { m.newEnvSet() })
				}
				if menu.Item("重命名环境").Icon(icoPencil).Clicked() {
					i := m.activeEnvIndex()
					m.c.After(func() { m.openEnvDialog(m.envSets[i].Name) })
				}
				menu.Separator()
				if menu.Item("删除环境").Icon(icoTrash).Destructive().Disabled(len(m.envSets) <= 1).Clicked() {
					i := m.activeEnvIndex()
					m.c.After(func() { m.deleteEnvSet(i) })
				}
			})
		})
		envs := m.envSets[idx].Vars
		ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
			ui.Row(c).PadX(10).PadY(7).Gap(4).Background(theme.Muted).Children(func() {
				ui.Text(c, "名称").Muted().FontSize(12).Grow(1)
				ui.Text(c, "值").Muted().FontSize(12).Grow(1)
				ui.Text(c, "").Width(32)
			})
			ui.List(c, &m.envList, len(envs), func(row ui.ListRow) {
				i := row.Index
				row.Element.PadX(10).PadY(3).Gap(4).Children(func() {
					ui.TextField(row.C, &envs[i].Key, "名称", "").Grow(1)
					ui.TextField(row.C, &envs[i].Value, "值", "").Grow(1)
					if ui.Button(row.C, "删除").Small().Ghost().Icon(icoTrash).IconOnly().Width(32).Tooltip("删除").
						Opacity(0).StateFrom(row.Element).Hover(ui.Look{}.Opacity(1)).Transition().Clicked() {
						c.After(func() { removeKV(&m.envSets[idx].Vars, i) })
					}
				})
			}).Label("环境变量").Grow(1)
			addRow(c, theme, "添加变量", func() { m.envSets[idx].Vars = append(m.envSets[idx].Vars, kv{}) })
		})
	})
}

// envSetNames lists the environment names in order, for the URL-row select.
func (m *model) envSetNames() []string {
	names := make([]string, len(m.envSets))
	for i, e := range m.envSets {
		names[i] = e.Name
	}
	return names
}

// ---- request panel ----

// syncURLParams keeps the URL and the params table in sync, both ways:
//   - a changed URL is the source: its query refreshes rows with the same key,
//     fills the blank rows, appends the rest, and rows it once supplied but no
//     longer has are removed;
//   - a changed table (a value, a row, an enable) is the source: the URL's
//     query is rebuilt from the enabled rows.
//
// Whichever changed is absorbed and remembered, so the two never bounce. It
// reports whether the URL was rewritten (the field needs a frame to show it).
func (m *model) syncURLParams() bool {
	base := m.t().url
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	urlChanged := m.t().url != m.t().urlSeen
	tableFP := m.t().paramsFingerprint()
	tableChanged := tableFP != m.t().paramsSeen

	switch {
	case urlChanged:
		query, prevQuery := "", ""
		if i := strings.Index(m.t().url, "?"); i >= 0 {
			query = m.t().url[i+1:]
		}
		if i := strings.Index(m.t().urlSeen, "?"); i >= 0 {
			prevQuery = m.t().urlSeen[i+1:]
		}
		m.t().urlSeen = m.t().url
		m.absorbURLParams(query, prevQuery)
		m.t().paramsSeen = m.t().paramsFingerprint()
	case tableChanged:
		q := ""
		for _, r := range m.t().params {
			if r.Disabled || r.Key == "" {
				continue // empty-key rows stay out of the URL
			}
			if q != "" {
				q += "&"
			}
			q += queryLite(r.Key) + "=" + queryLite(r.Value)
		}
		if q != "" {
			base += "?" + q
		}
		m.t().url = base
		m.t().urlSeen = m.t().url
		m.t().paramsSeen = tableFP
		return true
	}
	return false
}

// absorbURLParams merges one query string into the params table: rows whose
// key the query has refresh from it, query keys without a row fill a blank
// row or are appended, and rows the previous URL carried but this query has
// dropped are removed.
func (m *model) absorbURLParams(query, prevQuery string) {
	var params []kv
	for _, part := range strings.Split(query, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if k = queryDecode(k); k != "" {
			params = append(params, kv{Key: k, Value: queryDecode(v)})
		}
	}
	inQuery := map[string]bool{}
	for _, p := range params {
		inQuery[p.Key] = true
	}
	// rows whose key the query has refresh from it
	for i := range m.t().params {
		if inQuery[m.t().params[i].Key] {
			for _, p := range params {
				if p.Key == m.t().params[i].Key {
					m.t().params[i].Value = p.Value
				}
			}
		}
	}
	// query keys without a row: fill a blank slot, else append
	haveRow := map[string]bool{}
	for _, r := range m.t().params {
		if r.Key != "" {
			haveRow[r.Key] = true
		}
	}
	for _, p := range params {
		if haveRow[p.Key] {
			continue
		}
		placed := false
		for i := range m.t().params {
			if m.t().params[i].Key == "" && m.t().params[i].Value == "" && !m.t().params[i].Disabled {
				m.t().params[i].Key, m.t().params[i].Value = p.Key, p.Value
				placed = true
				break
			}
		}
		if !placed {
			m.t().params = append(m.t().params, p)
		}
		haveRow[p.Key] = true
	}
	// rows the previous URL carried and this query has dropped are removed
	prevKeys := map[string]bool{}
	for _, part := range strings.Split(prevQuery, "&") {
		if part == "" {
			continue
		}
		if k, _, _ := strings.Cut(part, "="); k != "" {
			if k = queryDecode(k); k != "" {
				prevKeys[k] = true
			}
		}
	}
	if len(prevKeys) > 0 {
		var kept []kv
		for _, r := range m.t().params {
			// blank rows stay (they are slots)
			if r.Key == "" || !prevKeys[r.Key] || inQuery[r.Key] {
				kept = append(kept, r)
			}
		}
		m.t().params = kept
	}
}

// paramsFingerprint is the table's change marker: the enabled rows, in order.
// Disabled rows only matter through their toggle, so the mark carries it.
func (t *tabState) paramsFingerprint() string {
	var b strings.Builder
	for _, r := range t.params {
		mark := "1"
		if r.Disabled {
			mark = "0"
		}
		b.WriteString(mark)
		b.WriteString(queryLite(r.Key))
		b.WriteString("=")
		b.WriteString(queryLite(r.Value))
		b.WriteString("&")
	}
	return b.String()
}

// queryDecode decodes one %xx/+ escape, returning the raw text on errors.
func queryDecode(s string) string {
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

// queryLite encodes only what would break the query's shape — the separators,
// the escape marker, spaces and controls — and leaves 中文 and every other
// readable character as it is, so the URL bar never turns into %E9%A2….
// The real encoding happens at send time (buildURL/url.Parse).
func queryLite(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '&', '=', '#', '+', '%', '?', ' ':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, "%%%02X", c)
			} else {
				b.WriteByte(c) // UTF-8 bytes of readable text pass through
			}
		}
	}
	return b.String()
}

// tabStrip paints the open requests as chips over the editor: one chip per tab
// — its method's dot, its name, a close × — and a + to open another. Rows are
// keyed by the tab's id so a click lands on the tab that drew it, not on
// whichever one sits at that index now.
func (m *model) tabStrip(c ui.C, theme *ui.Theme) {
	// Sideways scroll: past a handful of tabs the chips would clip against the
	// window edge and the ones beyond would be unreachable. The + stays out,
	// pinned at the right.
	ui.Row(c).Gap(6).Items(ui.Center).Children(func() {
		ui.Scroll(c).Sideways().Grow(1).Height(34).Scrollbars().Children(func() {
			ui.Row(c).Gap(4).Items(ui.Center).Children(func() {
				for i := range m.tabs {
					t := m.tabs[i]
					id, active := t.id, i == m.tabIdx
					base := theme.Muted
					if active {
						base = theme.Card
					}
					title := m.tabTitle(t)
					chip := ui.Row(c.KeyInt(id)).Height(30).MaxWidth(190).PadX(10).Gap(6).Items(ui.Center).
						Background(base).Radius(theme.Metrics.RadiusControl).
						Hit(ui.ID(c), t.method+" "+title).Tooltip("切换到此标签（Ctrl+W 关闭）").
						Hover(ui.Look{}.Background(theme.Hover)).Children(func() {
						ui.Icon(c, dotIcon(methodColor(t.method)))
						ui.Text(c, title).FontSize(12).Ellipsis()
						// Postman's orange dot: the request differs from the entry
						// it edits (or has never been saved)
						if m.tabDirty(t) {
							ui.Text(c, "●").FontSize(9).Color(methodColor("POST"))
						}
						if t.sending {
							ui.Spinner(c)
						}
						if ui.Button(c, "关闭标签").XSmall().Ghost().Icon(icoClose).IconOnly().Clicked() {
							c.After(func() { m.closeTabByID(id) })
						}
					})
					if chip.Clicked() {
						c.After(func() { m.focusTabID(id) })
					}
				}
			})
		})
		if ui.Button(c, "新建标签").Small().Ghost().Icon(icoPlus).IconOnly().Tooltip("新建请求标签（Ctrl+T）").Clicked() {
			c.After(func() { m.newRequest() })
		}
	})
}

func (m *model) requestPanel(c ui.C, theme *ui.Theme) {
	ui.Column(c).Height(m.split.Pos).Pad(10).Gap(8).Children(func() {
		m.tabStrip(c, theme)
		// The URL bar as its own card: method with its color dot, the address,
		// send and save — the row Postman centers the tool on.
		ui.Row(c).Pad(6).Gap(8).Background(theme.Card).Border(1, theme.Border).Radius(theme.Metrics.RadiusControl).Items(ui.Center).Children(func() {
			ui.Select(c, &m.t().method, httpMethods, nil).Icons(methodIcons)
			if ui.TextField(c, &m.t().url, "URL", "https://api.example.com/get").Grow(1).Submitted() {
				m.send()
			}
			if ui.Button(c, "发送").Icon(icoSend).Disabled(m.t().sending).Clicked() {
				m.send()
			}
			saveLabel := "保存到收藏"
			if _, editing := m.editingEntry(); editing {
				saveLabel = "保存修改"
			}
			if ui.Button(c, saveLabel).Outline().Icon(icoFolder).Tooltip("Ctrl+S").Clicked() {
				m.openSave()
			}
			// The environment is picked right where the request is made; it is
			// created, renamed and deleted from the 环境 tab. No Tooltip on a
			// Select: its node id is zero by design and Tooltip panics on it.
			ui.Select(c, &m.activeEnvName, m.envSetNames(), nil).MaxWidth(150)
		})
		// The two-way sync with the params table, against the URL as the field
		// just committed it. A rewritten URL needs one more frame to show.
		if m.syncURLParams() {
			c.Canvas().RequestFrame()
		}
		// The tab strip, the request-wide settings on its right.
		ui.Row(c).Gap(16).Items(ui.Center).Children(func() {
			ui.Tabs(c, &m.t().reqTab, []string{"参数", "认证", "请求头", "请求体", "断言"}, nil).Line()
			ui.Spacer(c)
			if e, editing := m.editingEntry(); editing {
				ui.Row(c).MaxWidth(200).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).Background(theme.Muted).Items(ui.Center).Gap(6).Hit(ui.ID(c), "正在编辑").Tooltip("正在编辑收藏中的接口，保存会更新它").Children(func() {
					ui.Icon(c, icoPencil)
					ui.Text(c, e.Name).FontSize(12).Muted().Ellipsis()
				})
			}
			ui.Checkbox(c, &m.t().followRedirects, "跟随重定向")
			codeBtn := ui.Button(c, "复制代码").Small().Ghost().Icon(icoCopy)
			if codeBtn.Clicked() {
				m.t().codeOpen = true
			}
			ui.Menu(c, codeBtn, &m.t().codeOpen, func(menu *ui.MenuItems) {
				if menu.Item("cURL").Clicked() {
					m.copyCode("curl")
				}
				if menu.Item("Go").Clicked() {
					m.copyCode("go")
				}
				if menu.Item("JavaScript").Clicked() {
					m.copyCode("javascript")
				}
				if menu.Item("Python").Clicked() {
					m.copyCode("python")
				}
			})
			if ui.Button(c, "新建").Small().Ghost().Clicked() {
				m.newRequest()
			}
		})
		switch m.t().reqTab {
		case "参数":
			m.kvPanel(c, theme, &m.t().paramList, &m.t().params, "添加参数")
		case "认证":
			m.authPanel(c, theme)
		case "请求头":
			m.kvPanel(c, theme, &m.t().headerList, &m.t().headers, "添加请求头")
		case "请求体":
			m.bodyPanel(c, theme)
		case "断言":
			m.assertPanel(c, theme)
		}
	})
}

// addRow paints the Postman-style + row under a key-value table: a full-width
// clickable row with a plus glyph that appends an entry.
func addRow(c ui.C, theme *ui.Theme, label string, add func()) {
	row := ui.Row(c).PadX(4).PadY(6).Gap(8).Items(ui.Center).
		Hit(ui.ID(c), label).
		Radius(theme.Metrics.RadiusControl).
		Hover(ui.Look{}.Background(theme.Hover)).
		Children(func() {
			ui.Icon(c, icoPlus)
			ui.Text(c, label).FontSize(13)
		})
	if row.Clicked() {
		c.After(add)
	}
}

// kvPanel declares an editable key-value table, Postman-style: a bordered
// container with a 键/值 header, rows whose delete shows on hover, and a + row
// at its foot.
func (m *model) kvPanel(c ui.C, theme *ui.Theme, state *ui.ListState, items *[]kv, addLabel string) {
	ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
		ui.Row(c).PadX(10).PadY(7).Gap(4).Background(theme.Muted).Children(func() {
			ui.Text(c, "").Width(24)
			ui.Text(c, "键").Muted().FontSize(12).Grow(1)
			ui.Text(c, "值").Muted().FontSize(12).Grow(1)
			// the delete column's reservation: a fixed-width blank, not a
			// Spacer — a Spacer always grows and would squeeze the columns
			ui.Text(c, "").Width(32)
		})
		ui.List(c, state, len(*items), func(row ui.ListRow) {
			i := row.Index
			(*items)[i].Enabled = !(*items)[i].Disabled // the checkbox's mirror
			row.Element.PadX(10).PadY(3).Gap(4).Children(func() {
				if ui.Checkbox(row.C, &(*items)[i].Enabled, "").Self(ui.Center).Tooltip("启用或停用这一行").Changed() {
					c.After(func() { (*items)[i].Disabled = !(*items)[i].Enabled })
				}
				ui.TextField(row.C, &(*items)[i].Key, "键", "").Grow(1)
				ui.TextField(row.C, &(*items)[i].Value, "值", "").Grow(1)
				if ui.Button(row.C, "删除").Small().Ghost().Icon(icoTrash).IconOnly().Width(32).Tooltip("删除").
					Opacity(0).StateFrom(row.Element).Hover(ui.Look{}.Opacity(1)).Transition().Clicked() {
					c.After(func() { removeKV(items, i) })
				}
			})
		}).Label("键值列表").Grow(1)
		addRow(c, theme, addLabel, func() { *items = append(*items, kv{}) })
	})
}

// formPanel declares the fields of a multipart/form-data body, in the same
// bordered table the params and headers use.
func (m *model) formPanel(c ui.C, theme *ui.Theme) {
	ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
		ui.List(c, &m.t().formList, len(m.t().formFields), func(row ui.ListRow) {
			i := row.Index
			f := &m.t().formFields[i]
			f.Enabled = !f.Disabled
			row.Element.PadX(10).PadY(3).Gap(4).Items(ui.Center).Children(func() {
				if ui.Checkbox(row.C, &f.Enabled, "").Tooltip("启用或停用这一行").Changed() {
					c.After(func() { f.Disabled = !f.Enabled })
				}
				ui.TextField(row.C, &f.Key, "字段", "").Grow(1)
				if f.IsFile {
					ui.TextField(row.C, &f.FilePath, "文件", "").Grow(1)
				} else {
					ui.TextField(row.C, &f.Value, "值", "").Grow(1)
				}
				if ui.Button(row.C, "文件").Small().Ghost().Clicked() {
					c.After(func() { m.t().formFields[i].IsFile = !m.t().formFields[i].IsFile })
				}
				if f.IsFile {
					if ui.Button(row.C, "选择").Small().Ghost().Clicked() {
						if files, err := app.OpenFiles(app.OpenFileOptions{Title: "选择文件"}); err == nil && len(files) > 0 {
							c.After(func() { m.t().formFields[i].FilePath = files[0] })
						}
					}
				}
				if ui.Button(row.C, "删除").Small().Ghost().Icon(icoTrash).IconOnly().Width(32).Tooltip("删除").
					Opacity(0).StateFrom(row.Element).Hover(ui.Look{}.Opacity(1)).Transition().Clicked() {
					c.After(func() { m.removeFormField(i) })
				}
			})
		}).Label("表单字段").Grow(1)
		addRow(c, theme, "添加字段", func() { m.t().formFields = append(m.t().formFields, formField{}) })
	})
}

// authTypes are the auth panel's kinds; authConfig.Type stores the key, the
// panel shows the label.
var authTypes = []string{"", "basic", "bearer", "apikey"}

func authTypeLabel(t string) string {
	switch t {
	case "":
		return "无"
	case "basic":
		return "Basic"
	case "bearer":
		return "Bearer"
	case "apikey":
		return "API Key"
	}
	return t
}

// authHint says what the panel will send, so the user sees the effect without
// opening a response's headers.
func authHint(a authConfig) string {
	switch a.Type {
	case "basic":
		return "发送请求头 Authorization: Basic（用户名:密码 的 Base64）"
	case "bearer":
		return "发送请求头 Authorization: Bearer <token>"
	case "apikey":
		if a.Key != "" {
			return "发送请求头 " + a.Key + ": <值>"
		}
		return "发送一个自定义请求头"
	}
	return ""
}

// authPanel declares the request's authorization: a kind and the fields it
// needs. Nothing is stored as a header — the header is synthesized at send
// time, so the 请求头 table keeps only what the user typed.
func (m *model) authPanel(c ui.C, theme *ui.Theme) {
	t := m.t()
	ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
		ui.Row(c).PadX(10).PadY(7).Gap(12).Items(ui.Center).Background(theme.Muted).Children(func() {
			ui.Text(c, "类型").Muted().FontSize(12)
			ui.Choice(c, &t.auth.Type, authTypes, authTypeLabel)
			ui.Spacer(c)
			if h := authHint(t.auth); h != "" {
				ui.Text(c, h).Muted().FontSize(12).Ellipsis()
			}
		})
		ui.Column(c).Grow(1).Pad(10).Gap(8).Children(func() {
			switch t.auth.Type {
			case "basic":
				ui.Row(c).Gap(8).Children(func() {
					ui.TextField(c, &t.auth.Username, "用户名", "username").Grow(1)
					ui.TextField(c, &t.auth.Password, "密码", "••••••").Grow(1).Secure()
				})
			case "bearer":
				ui.TextField(c, &t.auth.Token, "Token", "粘贴访问令牌，可用 {{变量}}")
			case "apikey":
				ui.Row(c).Gap(8).Children(func() {
					ui.TextField(c, &t.auth.Key, "头名", "X-API-Key").Width(220)
					ui.TextField(c, &t.auth.Value, "值", "").Grow(1)
				})
			case "":
				emptyState(c, theme, icoLock, "无需认证", "选择 Basic、Bearer 或 API Key，请求头会自动生成")
			}
			if t.auth.Type != "" {
				ui.Text(c, "认证不影响下方请求头表；同名头以请求头表为准。").Muted().FontSize(12)
			}
		})
	})
}

// assertPanel edits a request's declarative checks: one row per assertion —
// its kind, what to check, and for JSON paths what the value should equal.
// The results show under the response and in collection runs.
func (m *model) assertPanel(c ui.C, theme *ui.Theme) {
	ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
		ui.List(c, &m.t().assertList, len(m.t().assertions), func(row ui.ListRow) {
			i := row.Index
			a := &m.t().assertions[i]
			row.Element.PadX(10).PadY(3).Gap(6).Items(ui.Center).Children(func() {
				ui.Choice(row.C, &a.Kind, assertKinds, assertKindLabel)
				switch a.Kind {
				case "json":
					ui.TextField(row.C, &a.Target, "路径", "data.items[0].id").Grow(1)
					ui.TextField(row.C, &a.Value, "期望值", "").Grow(1)
				case "contains":
					ui.TextField(row.C, &a.Target, "包含文本", "成功").Grow(1)
					ui.Text(row.C, "").Grow(1)
				default:
					ui.TextField(row.C, &a.Target, "状态码", "200").Grow(1)
					ui.Text(row.C, "").Grow(1)
				}
				if ui.Button(row.C, "删除").Small().Ghost().Icon(icoTrash).IconOnly().Width(32).Tooltip("删除").
					Opacity(0).StateFrom(row.Element).Hover(ui.Look{}.Opacity(1)).Transition().Clicked() {
					idx := i
					c.After(func() { m.removeAssertion(idx) })
				}
			})
		}).Label("断言列表").Grow(1)
		addRow(c, theme, "添加断言", func() { m.t().assertions = append(m.t().assertions, assertion{Kind: "status"}) })
		ui.Row(c).PadX(10).PadY(6).Children(func() {
			ui.Text(c, "支持 {{环境变量}}；JSON 路径按 点.分层 与 [下标] 取值。结果随响应显示，集合运行时计入统计。").Muted().FontSize(12)
		})
	})
}

func (m *model) removeAssertion(i int) {
	if i < 0 || i >= len(m.t().assertions) {
		return
	}
	m.t().assertions = append(m.t().assertions[:i], m.t().assertions[i+1:]...)
}

// bodyCheck is the live JSON validation of the request body.
type bodyCheck struct {
	valid bool
	line  int
	col   int
	msg   string
}

// checkBodyJSON re-validates the body when it changed: valid, or the first
// syntax error with its line and column.
func (t *tabState) checkBodyJSON() {
	if t.body == t.bodySeen {
		return
	}
	t.bodySeen = t.body
	raw := strings.TrimSpace(t.body)
	if raw == "" {
		t.bodyCheck = bodyCheck{valid: true}
		return
	}
	if json.Valid([]byte(raw)) {
		t.bodyCheck = bodyCheck{valid: true}
		return
	}
	line, col, msg := 0, 0, ""
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		msg = err.Error()
		var offset int64
		if se, ok := err.(*json.SyntaxError); ok {
			offset = se.Offset
		} else if te, ok := err.(*json.UnmarshalTypeError); ok {
			offset = te.Offset
		}
		if offset > 0 {
			// the scanner reports after consuming the offending byte, so the
			// column points at that byte itself
			before := raw[:min(int(offset)-1, len(raw))]
			line = 1 + strings.Count(before, "\n")
			if nl := strings.LastIndexByte(before, '\n'); nl >= 0 {
				col = len([]rune(before[nl+1:])) + 1
			} else {
				col = len([]rune(before)) + 1
			}
		}
	}
	t.bodyCheck = bodyCheck{valid: false, line: line, col: col, msg: msg}
}

// formatBodyJSON pretty-prints the body with two-space indentation.
func (m *model) formatBodyJSON() {
	raw := strings.TrimSpace(m.t().body)
	if raw == "" {
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		m.toaster.Show("格式化失败", "JSON 无效，无法格式化", ui.ToastError)
		return
	}
	m.t().body = buf.String()
	m.t().checkBodyJSON()
}

func (m *model) bodyPanel(c ui.C, theme *ui.Theme) {
	ui.Column(c).Grow(1).Gap(8).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Choice(c, &m.t().bodyType, bodyTypes, bodyTypeLabel)
			if m.t().bodyType != "none" {
				ui.Text(c, "在下方编辑请求体").Muted()
			}
		})
		switch m.t().bodyType {
		case "form-data":
			m.formPanel(c, theme)
		case "none":
		case "json":
			before := m.t().body
			m.t().checkBodyJSON()
			ui.Row(c).Gap(12).Items(ui.Center).Children(func() {
				if ui.Button(c, "格式化").Small().Ghost().Tooltip("Ctrl+B").Clicked() {
					m.formatBodyJSON()
				}
				ui.Spacer(c)
				if m.t().bodyCheck.valid {
					if m.t().body != "" {
						ui.Row(c).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).Background(theme.Muted).Center().Children(func() {
							ui.Text(c, "✓ 有效 JSON").FontSize(12).Color(theme.Success)
						})
					}
				} else {
					ui.Row(c).MaxWidth(420).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).Background(theme.Muted).Items(ui.Center).Gap(6).Children(func() {
						ui.Text(c, fmt.Sprintf("✗ 第 %d 行 第 %d 列", m.t().bodyCheck.line, m.t().bodyCheck.col)).FontSize(12).Color(theme.Danger)
						ui.Text(c, m.t().bodyCheck.msg).FontSize(12).Muted().Grow(1).Ellipsis()
					})
				}
			})
			ui.TextArea(c, &m.t().body, "请求体编辑器", "").Grow(1).Mono()
			if ui.Shortcut(c, ui.Primary, "b") {
				m.formatBodyJSON()
			}
			if m.t().body != before {
				// the editor committed a new value this paint: one more frame
				// lets the check and the chip catch up
				c.Canvas().RequestFrame()
			}
		default:
			ui.TextArea(c, &m.t().body, "请求体编辑器", "").Grow(1).Mono()
		}
	})
}

// ---- response panel ----

func (m *model) responsePanel(c ui.C, theme *ui.Theme) {
	// The response, as its own card: tabs and the stats in the header, the
	// body under a divider.
	ui.Column(c).Grow(1).Pad(10).Children(func() {
		ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
			ui.Row(c).PadX(10).PadY(8).Gap(12).Items(ui.Center).Children(func() {
				ui.Tabs(c, &m.t().respTab, []string{"响应体", "响应头"}, nil).Line()
				ui.Spacer(c)
				if m.t().resp != nil {
					m.statusBadge(c, theme)
					statChip(c, theme, m.t().resp.StatusText)
					statChip(c, theme, m.t().respMeta)
					if ui.Button(c, "复制响应体").Small().Ghost().Icon(icoCopy).Clicked() {
						_ = app.SetClipboardText(m.t().resp.Body)
						m.toaster.Show("已复制", "响应体已复制到剪贴板", ui.ToastSuccess)
					}
					if ui.Button(c, "保存到文件").Small().Ghost().Icon(icoDownload).Tooltip("保存响应体到文件").Clicked() {
						m.c.After(func() { m.saveResponseToFile() })
					}
				} else if m.t().sending {
					ui.Spinner(c)
					ui.Text(c, "发送中…").Muted().FontSize(12)
				}
			})
			ui.Divider(c)
			if m.running || len(m.runResults) > 0 {
				m.runResultsPanel(c, theme)
			}
			switch m.t().respTab {
			case "响应体":
				m.respBodyPanel(c, theme)
			case "响应头":
				m.respHeadersPanel(c, theme)
			}
			if m.t().resp != nil && len(m.t().assertResults) > 0 {
				ui.Divider(c)
				m.assertResultsPanel(c, theme)
			}
		})
	})
}

// assertResultsPanel lists the request's assertions against the response:
// one row each, the mismatches carrying what actually came back.
func (m *model) assertResultsPanel(c ui.C, theme *ui.Theme) {
	passed := 0
	for _, r := range m.t().assertResults {
		if r.pass {
			passed++
		}
	}
	allPass := passed == len(m.t().assertResults)
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
			ui.Row(c).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).
				Background(theme.Success.WithAlpha(0.15)).Center().Children(func() {
				ui.Text(c, "断言 "+strconv.Itoa(passed)+"/"+strconv.Itoa(len(m.t().assertResults))).FontSize(12).Bold().
					Color(theme.Success)
			})
			if !allPass {
				ui.Text(c, "存在未通过的断言").FontSize(12).Color(theme.Danger)
			}
			ui.Text(c, "随请求保存，集合运行时同样检查").Muted().FontSize(12)
		})
		// A Scroll needs a height of its own here: the column has none to
		// share, so Grow would collapse to zero. Each row is ~22 px; past four
		// the list scrolls.
		h := min(24*len(m.t().assertResults), 100)
		ui.Scroll(c).Height(float32(h)).Children(func() {
			for _, r := range m.t().assertResults {
				color := theme.Success
				mark := "✓"
				detail := ""
				if !r.pass {
					color = theme.Danger
					mark = "✗"
					detail = " — " + r.detail
				}
				ui.Row(c).Gap(8).Items(ui.Center).PadY(2).Children(func() {
					ui.Text(c, mark).FontSize(12).Color(color)
					ui.Text(c, r.desc).Grow(1).Ellipsis()
					ui.Text(c, detail).FontSize(12).Color(color).Ellipsis()
				})
			}
		})
	})
}

// saveResponseToFile writes the response body to the file the picker returns.
func (m *model) saveResponseToFile() {
	if m.t().resp == nil {
		return
	}
	path, err := app.SaveFile(app.SaveFileOptions{
		Title:            "保存响应",
		SuggestedName:    "response.json",
		DefaultExtension: ".json",
	})
	if err != nil {
		if errors.Is(err, app.ErrCanceled) {
			return // the user dismissed the picker
		}
		m.toaster.Show("保存失败", err.Error(), ui.ToastError)
		return
	}
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(m.t().resp.Body), 0o644); err != nil {
		m.toaster.Show("保存失败", err.Error(), ui.ToastError)
		return
	}
	m.toaster.Show("已保存", path, ui.ToastSuccess)
}

func (m *model) runResultsPanel(c ui.C, theme *ui.Theme) {
	ui.Column(c).Gap(4).MaxHeight(150).Children(func() {
		ui.Text(c, fmt.Sprintf("集合运行 %d/%d", len(m.runResults), len(m.runQueue))).Muted().FontSize(12)
		ui.Scroll(c).Grow(1).Children(func() {
			for _, r := range m.runResults {
				color := theme.Success
				if !r.ok || r.assertFailed > 0 {
					color = theme.Danger
				}
				ui.Row(c).Gap(8).Items(ui.Center).PadY(3).Children(func() {
					ui.Text(c, r.name).Grow(1).Ellipsis()
					ui.Text(c, runStatusText(r)).Mono().FontSize(12).Color(color)
				})
			}
			if m.running {
				ui.Text(c, "运行中…").Muted()
			}
		})
	})
}

func (m *model) statusBadge(c ui.C, theme *ui.Theme) {
	code := m.t().resp.Status
	color := theme.MutedForeground
	switch {
	case code >= 200 && code < 300:
		color = theme.Success
	case code >= 300 && code < 400:
		color = theme.Primary
	case code >= 400 && code < 500:
		// Accent is a surface colour, nearly invisible as text on light — the
		// same lesson the JSON tree's bools taught: 4xx gets the app's orange.
		color = methodColor("POST")
	case code >= 500:
		color = theme.Danger
	}
	ui.Row(c).PadX(8).Height(22).Radius(theme.Metrics.RadiusControl).Background(color.WithAlpha(0.15)).Center().Children(func() {
		ui.Text(c, strconv.Itoa(code)).Bold().Color(color)
	})
}

var respViews = []string{"树", "文本"}

func (m *model) respBodyPanel(c ui.C, theme *ui.Theme) {
	if m.t().resp == nil {
		if m.t().sending {
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Spinner(c)
				ui.Text(c, "发送中…").Muted().FontSize(12)
			})
			return
		}
		emptyState(c, theme, icoSend, "尚未发送请求", "填好地址后点击发送，或按 Ctrl+Enter")
		return
	}
	if m.t().jsonRoot != nil {
		ui.Row(c).Justify(ui.End).Items(ui.Center).Children(func() {
			ui.Choice(c, &m.t().respView, respViews, nil)
		})
		if m.t().respView != "文本" {
			ui.Tree(c, &m.t().jsonTree, len(m.t().jsonRows), m.jsonLabelFn, m.jsonContentFn).Grow(1)
			if i, ok := m.t().jsonTree.Toggled(); ok {
				c.After(func() { m.t().toggleJSONRow(i) })
			}
			if i, ok := m.t().jsonTree.Chosen(); ok {
				c.After(func() { m.t().toggleJSONRow(i) }) // a click on a branch row opens it too
			}
			return
		}
	}
	ui.Scroll(c).Grow(1).Sideways().Children(func() {
		for _, line := range m.t().respLines {
			ui.Text(c, line).Mono().FontSize(13)
		}
	})
}

func (m *model) respHeadersPanel(c ui.C, theme *ui.Theme) {
	if m.t().resp == nil || len(m.t().resp.Headers) == 0 {
		ui.Text(c, "无响应头").Muted()
		return
	}
	ui.Column(c).Grow(1).Clip().Border(1, theme.Border).Radius(theme.Metrics.RadiusSurface).Background(theme.Card).Children(func() {
		ui.Row(c).PadX(10).PadY(7).Gap(4).Background(theme.Muted).Children(func() {
			ui.Text(c, "名称").Muted().FontSize(12).Width(220)
			ui.Text(c, "值").Muted().FontSize(12).Grow(1)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			for _, h := range m.t().resp.Headers {
				ui.Row(c).PadX(10).PadY(3).Gap(4).Children(func() {
					ui.Text(c, h.Key).Mono().FontSize(13).Width(220).Shrink(0).Ellipsis()
					ui.Paragraph(c, h.Value, 0).Mono().FontSize(13).Grow(1)
				})
			}
		})
	})
}

// ---- save dialog ----

func (m *model) saveDialog(c ui.C) {
	title := "保存到收藏"
	if _, editing := m.editingEntry(); editing {
		title = "保存修改"
	}
	ui.Modal(c, &m.saveOpen, title, func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "接口名：").Muted()
			ui.TextField(c, &m.saveName, "接口名", "我的请求").AutoFocus()
			ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
				ui.Text(c, "分组").Muted()
				ui.Select(c, &m.saveGroup, m.saveOptions, nil)
			})
		})
	}, func() {
		ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				m.saveOpen = false
			}
			if ui.Button(c, "保存").Clicked() {
				m.confirmSaveDialog()
			}
		})
	})
}

// confirmSaveDialog applies the save dialog: the entry being edited is updated
// in place, anything else is appended.
func (m *model) confirmSaveDialog() {
	name := strings.TrimSpace(m.saveName)
	if name == "" {
		name = m.t().method + " " + m.t().url
	}
	group := ""
	if m.saveGroup != "未分组" {
		group = m.saveGroup
	}
	m.saveOpen = false
	s := m.t().toSaved(name)
	s.Group = group
	if _, editing := m.editingEntry(); editing {
		m.collections[m.t().editing] = s
		m.toaster.Show("已保存修改", name, ui.ToastSuccess)
	} else {
		m.collections = append(m.collections, s)
		m.t().editing = len(m.collections) - 1 // the tab now owns the entry: the next save updates it
		m.toaster.Show("已保存", name, ui.ToastSuccess)
	}
	m.rebuildCollRows()
	m.persist()
}

// ---- actions ----

// setTheme applies preset i and remembers the choice.
func (m *model) setTheme(i int) {
	if i < 0 || i >= len(themePresets) || i == m.themeIdx {
		return
	}
	m.themeIdx = i
	m.theme = themePresets[i].build()
	m.persist()
	m.invalidate()
}

// buildOutgoing snapshots the current request for a send or code generation.
func (m *model) buildOutgoing() *outgoing {
	envs := append([]kv(nil), m.activeEnvs()...)
	headers := authHeaders(m.t().auth, envs) // the auth panel's headers, before the user's own
	headers = append(headers, append([]kv(nil), m.t().headers...)...)
	return &outgoing{
		Method:          m.t().method,
		URL:             m.t().url,
		Params:          append([]kv(nil), m.t().params...),
		Headers:         headers,
		BodyType:        m.t().bodyType,
		Body:            m.t().body,
		FormFields:      append([]formField(nil), m.t().formFields...),
		Envs:            envs,
		FollowRedirects: m.t().followRedirects,
	}
}

func (m *model) send() {
	if strings.TrimSpace(m.t().url) == "" {
		m.toaster.Show("缺少 URL", "请输入请求地址", ui.ToastError)
		return
	}
	m.t().sendKey = m.buildOutgoing()
	m.t().sending = true
	m.t().resp = nil
	m.t().respLines = nil
	m.t().respMeta = ""

	entry := m.t().toSaved(m.t().method + " " + m.t().url)
	entry.Time = time.Now().Format("2006-01-02 15:04:05")
	m.history = append([]savedRequest{entry}, m.history...)
	if len(m.history) > 200 {
		m.history = m.history[:200]
	}
	m.rebuildHistRows()
	m.persist()
}

// applyResult stores a finished send in the tab that started it: the request
// may have been sent from a tab the user has since left, and its response
// waits there for when they come back.
func (m *model) applyResult(i int, r httpResult, err error) {
	if i < 0 || i >= len(m.tabs) {
		return
	}
	t := m.tabs[i]
	t.sending = false
	if err != nil && r.Err == "" {
		r.Err = err.Error()
	}
	t.resp = &r
	if r.Err != "" {
		t.respLines = []string{r.Err}
		t.respMeta = ""
	} else if r.Body == "" {
		t.respLines = []string{"(空响应体)"}
		t.respMeta = fmt.Sprintf("%d ms · %s", r.Elapsed.Milliseconds(), formatSize(r.Size))
	} else {
		t.respLines = strings.Split(r.Body, "\n")
		t.respMeta = fmt.Sprintf("%d ms · %s", r.Elapsed.Milliseconds(), formatSize(r.Size))
	}
	t.jsonRoot = parseJSONTree(r.Body)
	t.rebuildJSONRows()
	t.assertResults = runAssertions(t.assertions, m.activeEnvs(), &r)
}

func (m *model) copyCode(lang string) {
	code := generateCode(lang, m.buildOutgoing(), m.t().followRedirects)
	_ = app.SetClipboardText(code)
	m.toaster.Show("已复制", langLabel(lang)+" 代码已复制到剪贴板", ui.ToastSuccess)
}

// ---- collection runner ----

func (m *model) startRun(indices []int) {
	if len(indices) == 0 {
		return
	}
	m.runQueue = indices
	m.runIndex = 0
	m.runResults = nil
	m.running = true
	m.setNextRun()
	m.invalidate()
}

func (m *model) setNextRun() {
	if m.runIndex >= len(m.runQueue) {
		m.running = false
		m.runKey = nil
		return
	}
	idx := m.runQueue[m.runIndex]
	m.runKey = toOutgoing(m.collections[idx], m.t().followRedirects, m.activeEnvs())
}

func (m *model) advanceRun(r httpResult, err error) {
	if m.runIndex >= len(m.runQueue) {
		return
	}
	idx := m.runQueue[m.runIndex]
	name := m.collections[idx].Name
	if err != nil {
		r.Err = err.Error()
	}
	checked := runAssertions(m.collections[idx].Assertions, m.activeEnvs(), &r)
	failed := 0
	var details []string
	for _, cr := range checked {
		if !cr.pass {
			failed++
			details = append(details, cr.desc+"（"+cr.detail+"）")
		}
	}
	m.runResults = append(m.runResults, runResult{
		name:         name,
		status:       r.Status,
		elapsed:      r.Elapsed,
		ok:           r.Err == "",
		err:          r.Err,
		assertTotal:  len(checked),
		assertFailed: failed,
		assertDetail: strings.Join(details, "；"),
	})
	m.runIndex++
	m.setNextRun()
	m.invalidate()
}

func toOutgoing(s savedRequest, follow bool, envs []kv) *outgoing {
	headers := authHeaders(s.Auth, envs) // the auth panel's headers, before the user's own
	headers = append(headers, append([]kv(nil), s.Headers...)...)
	return &outgoing{
		Method:          s.Method,
		URL:             s.URL,
		Params:          append([]kv(nil), s.Params...),
		Headers:         headers,
		BodyType:        s.BodyType,
		Body:            s.Body,
		FormFields:      append([]formField(nil), s.FormFields...),
		Envs:            append([]kv(nil), envs...),
		FollowRedirects: follow,
	}
}

func (m *model) openSave() {
	if strings.TrimSpace(m.t().url) == "" {
		m.toaster.Show("缺少 URL", "请先输入请求地址", ui.ToastError)
		return
	}
	m.saveOptions = append([]string{"未分组"}, m.groups...)
	m.saveGroup = "未分组"
	m.saveName = ""
	if e, ok := m.editingEntry(); ok {
		m.saveName = e.Name
		if e.Group != "" {
			m.saveGroup = e.Group
		}
	}
	m.saveOpen = true
}

// editingEntry is the collection entry the editor holds, if any.
func (m *model) editingEntry() (savedRequest, bool) {
	if m.t().editing >= 0 && m.t().editing < len(m.collections) {
		return m.collections[m.t().editing], true
	}
	return savedRequest{}, false
}

// newInGroup appends an empty request to a folder and opens it in a new tab:
// the user fills it in and saves, which updates the entry in place.
func (m *model) newInGroup(group string) {
	s := savedRequest{Name: "新建接口", Method: "GET", Group: group}
	m.collections = append(m.collections, s)
	t := m.openTab()
	t.editing = len(m.collections) - 1
	t.applySaved(s)
	m.rebuildCollRows()
	m.persist()
	m.toaster.Show("已新建接口", "编辑后点 保存修改 收入 "+group, ui.ToastSuccess)
}

// loadCollection brings a saved request into a tab: the tab already editing
// this entry comes to the front, anything else opens in a new one — clicking
// through the collection never tramples an in-progress request.
func (m *model) loadCollection(i int) {
	if i < 0 || i >= len(m.collections) {
		return
	}
	for k := range m.tabs {
		if m.tabs[k].editing == i {
			m.tabIdx = k
			return
		}
	}
	t := m.openTab()
	t.editing = i // the tab now holds this entry: a save updates it in place
	t.applySaved(m.collections[i])
	m.toaster.Show("已加载", m.collections[i].Name, ui.ToastSuccess)
}

// loadHistory opens a history entry in a new tab: history is not a collection
// entry, so the tab saves as a new one.
func (m *model) loadHistory(i int) {
	if i < 0 || i >= len(m.history) {
		return
	}
	t := m.openTab()
	t.editing = -1
	t.applySaved(m.history[i])
}

// persist writes the whole store, open tabs included: every collection
// mutation, send and save checkpoints the workspace, and RunCanvas's caller
// persists once more when the window closes.
func (m *model) persist() {
	tabs := make([]savedTab, len(m.tabs))
	for i, t := range m.tabs {
		tabs[i] = t.snapshot()
	}
	saveStore(storeData{
		Collections: m.collections,
		History:     m.history,
		Groups:      m.groups,
		EnvSets:     m.envSets,
		ActiveEnv:   m.activeEnvName,
		Tabs:        tabs,
		ActiveTab:   m.tabIdx,
		Theme:       m.themeIdx,
	})
}

// activeEnvIndex returns the index of the env set whose name matches
// activeEnvName, or 0 when nothing matches.
func (m *model) activeEnvIndex() int {
	for i, e := range m.envSets {
		if e.Name == m.activeEnvName {
			return i
		}
	}
	return 0
}

// activeEnvs returns the variables of the currently selected env set.
func (m *model) activeEnvs() []kv {
	if len(m.envSets) == 0 {
		return nil
	}
	return m.envSets[m.activeEnvIndex()].Vars
}

// deleteEnvSet removes an env set; the active one follows only when the
// deleted set was showing.
func (m *model) deleteEnvSet(i int) {
	if i < 0 || i >= len(m.envSets) || len(m.envSets) <= 1 {
		return
	}
	name := m.envSets[i].Name
	m.envSets = append(m.envSets[:i], m.envSets[i+1:]...)
	if m.activeEnvName == name {
		m.activeEnvName = m.envSets[0].Name
	}
	m.persist()
	m.toaster.Show("已删除环境", name, ui.ToastSuccess)
}

// deleteCollection removes a saved request from the collection list, keeping
// every tab's entry index honest when the list shifts.
func (m *model) deleteCollection(i int) {
	if i < 0 || i >= len(m.collections) {
		return
	}
	name := m.collections[i].Name
	for k := range m.tabs {
		if m.tabs[k].editing == i {
			m.tabs[k].editing = -1
		} else if m.tabs[k].editing > i {
			m.tabs[k].editing--
		}
	}
	m.collections = append(m.collections[:i], m.collections[i+1:]...)
	m.rebuildCollRows()
	m.persist()
	m.toaster.Show("已删除", name, ui.ToastSuccess)
}

// duplicateCollection appends a copy of a saved request right after it — the
// user edits the copy without touching the original. Every tab editing an
// entry after the insert point follows the shift.
func (m *model) duplicateCollection(i int) {
	if i < 0 || i >= len(m.collections) {
		return
	}
	dup := m.collections[i]
	dup.Name = m.collections[i].Name + " 副本"
	dup.Time = ""
	m.collections = append(m.collections[:i+1], append([]savedRequest{dup}, m.collections[i+1:]...)...)
	for k := range m.tabs {
		if m.tabs[k].editing > i {
			m.tabs[k].editing++
		}
	}
	m.rebuildCollRows()
	m.persist()
	m.toaster.Show("已复制接口", dup.Name, ui.ToastSuccess)
}

// newRequest opens a fresh tab; the one behind keeps what it held, so an
// in-progress request is never lost by Ctrl+N or the 新建 button.
func (m *model) newRequest() {
	m.openTab()
}

// ---- helpers ----

func allIndices(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

func removeKV(items *[]kv, i int) {
	if i < 0 || i >= len(*items) {
		return
	}
	*items = append((*items)[:i], (*items)[i+1:]...)
}

func (m *model) removeFormField(i int) {
	if i < 0 || i >= len(m.t().formFields) {
		return
	}
	m.t().formFields = append(m.t().formFields[:i], m.t().formFields[i+1:]...)
}

func formatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func runStatusText(r runResult) string {
	if r.err != "" {
		return "错误"
	}
	text := strconv.Itoa(r.status) + " · " + r.elapsed.Round(time.Millisecond).String()
	if r.assertTotal > 0 {
		if r.assertFailed > 0 {
			text += " · 断言 " + strconv.Itoa(r.assertTotal-r.assertFailed) + "/" + strconv.Itoa(r.assertTotal)
		} else {
			text += " · 断言全过"
		}
	}
	return text
}
