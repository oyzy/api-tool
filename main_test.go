package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// TestMain points the store at a throwaway file so tests do not read or write
// the user's real data.
func TestMain(m *testing.M) {
	dataPath = filepath.Join(os.TempDir(), "apitool-test-data.json")
	locationPath = filepath.Join(os.TempDir(), "apitool-test-location")
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	_ = os.Remove(locationPath)
	code := m.Run()
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	_ = os.Remove(locationPath)
	os.Exit(code)
}

// open runs the page with no window: the real painter, input and focus rules.
func open(t *testing.T, m *model) *app.Driver {
	t.Helper()
	page := ui.NewPage(ui.PageConfig{View: m.page, Theme: &m.theme})
	cfg := page.Config(config())
	m.inv = cfg.Invalidator
	driver, err := app.NewDriver(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(driver.Close)
	return driver
}

// click presses the middle of the control with the given name, as a user would.
func click(t *testing.T, driver *app.Driver, name string) {
	t.Helper()
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == name && !hit.IsLabel {
			b := hit.Bounds
			if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no control named %q on screen", name)
}

// rightClick opens the context menu of the control with the given name. A tree
// row's name is a label of the row (IsLabel), and clicking it clicks the row.
func rightClick(t *testing.T, driver *app.Driver, name string) {
	t.Helper()
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == name {
			b := hit.Bounds
			if err := driver.ClickButton(b.X+b.Width/2, b.Y+b.Height/2, gfx.PointerButtonSecondary); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no control named %q on screen", name)
}

// drag presses at from and lets go at to, as a user moves the pointer.
func drag(t *testing.T, driver *app.Driver, fromX, fromY, toX, toY float32) {
	t.Helper()
	if err := driver.Move(fromX, fromY); err != nil {
		t.Fatal(err)
	}
	if err := driver.Press(fromX, fromY, gfx.PointerButtonPrimary); err != nil {
		t.Fatal(err)
	}
	if err := driver.Move(toX, toY); err != nil {
		t.Fatal(err)
	}
	if err := driver.Release(toX, toY, gfx.PointerButtonPrimary); err != nil {
		t.Fatal(err)
	}
}

func shows(driver *app.Driver, want string) bool {
	for _, label := range driver.Snapshot().Labels {
		if strings.Contains(label.Value, want) {
			return true
		}
	}
	for _, hit := range driver.Snapshot().Hits {
		if strings.Contains(hit.Label, want) {
			return true
		}
	}
	for _, node := range driver.Accessibility() {
		if strings.Contains(node.Label, want) || strings.Contains(node.Value, want) {
			return true
		}
	}
	return false
}

// waitFor advances the driver until cond holds or the deadline passes. Background
// work (an Effect) delivers its result through the invalidator, applied on the
// next advance.
func waitFor(t *testing.T, driver *app.Driver, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the condition")
		}
		if err := driver.Advance(16 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
}

// clickN presses the middle of the nth control whose name starts with the
// given prefix (0-based): tab chips differ only by the path after the host,
// and the port is the test server's.
func clickN(t *testing.T, driver *app.Driver, name string, n int) {
	t.Helper()
	found := 0
	for _, hit := range driver.Snapshot().Hits {
		if strings.HasPrefix(hit.Label, name) && !hit.IsLabel {
			if found == n {
				b := hit.Bounds
				if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
					t.Fatal(err)
				}
				return
			}
			found++
		}
	}
	t.Fatalf("only %d controls named %q… on screen, wanted number %d", found, name, n)
}

func TestPageRenders(t *testing.T) {
	driver := open(t, newModel())
	for _, want := range []string{"发送", "URL", "收藏", "响应体", "保存到收藏", "复制代码"} {
		if !shows(driver, want) {
			t.Fatalf("the window does not show %q", want)
		}
	}
}

func TestDoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"n":1}`)
	}))
	defer srv.Close()

	res, err := doRequest(context.Background(), &outgoing{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 {
		t.Fatalf("status = %d, want 200", res.Status)
	}
	if !strings.Contains(res.Body, `"ok": true`) {
		t.Fatalf("body = %q, want pretty JSON", res.Body)
	}
	if len(res.Headers) == 0 {
		t.Fatal("expected response headers")
	}
}

// TestBodyVarSubstitution: the request body carries {{var}} like the URL,
// params and headers do; unknown names stay visible instead of going empty.
func TestBodyVarSubstitution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	}))
	defer srv.Close()

	res, err := doRequest(context.Background(), &outgoing{
		Method:   "POST",
		URL:      srv.URL,
		BodyType: "json",
		Body:     `{"name":"{{user}}","keep":"{{none}}"}`,
		Envs:     []kv{{Key: "user", Value: "张三"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Body, "张三") {
		t.Fatalf("body = %q, want the env value substituted", res.Body)
	}
	if !strings.Contains(res.Body, "{{none}}") {
		t.Fatalf("body = %q, an unknown variable must stay as written", res.Body)
	}
}

// TestAuthHelpers drives the auth panel end to end: Basic, Bearer and API Key
// each land as the header they stand for; {{var}} substitutes inside the
// credentials; a save round-trip keeps the config; the runner path
// (toOutgoing) synthesizes the same headers; and a hand-typed header wins over
// the panel.
func TestAuthHelpers(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = got[:0]
		got = append(got, r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := newModel()
	m.envSets = []envSet{{Name: "测试", Vars: []kv{{Key: "tok", Value: "abc123"}}}}
	m.activeEnvName = "测试"
	m.t().url = srv.URL

	// Basic
	m.t().auth = authConfig{Type: "basic", Username: "user", Password: "密 pass"}
	if _, err := doRequest(context.Background(), m.buildOutgoing()); err != nil {
		t.Fatal(err)
	}
	if got[0] != "Basic "+base64.StdEncoding.EncodeToString([]byte("user:密 pass")) {
		t.Fatalf("Authorization = %q, want Basic of user:密 pass", got[0])
	}

	// Bearer with an env variable in the token
	m.t().auth = authConfig{Type: "bearer", Token: "{{tok}}"}
	if _, err := doRequest(context.Background(), m.buildOutgoing()); err != nil {
		t.Fatal(err)
	}
	if got[0] != "Bearer abc123" {
		t.Fatalf("Authorization = %q, want Bearer abc123", got[0])
	}

	// API Key on a custom header
	m.t().auth = authConfig{Type: "apikey", Key: "X-API-Key", Value: "secret-值"}
	if _, err := doRequest(context.Background(), m.buildOutgoing()); err != nil {
		t.Fatal(err)
	}
	if got[1] != "secret-值" {
		t.Fatalf("X-API-Key = %q, want secret-值", got[1])
	}

	// a hand-typed Authorization header overrides the panel
	m.t().auth = authConfig{Type: "bearer", Token: "panel"}
	m.t().headers = []kv{{Key: "Authorization", Value: "hand-typed"}}
	o := m.buildOutgoing()
	if _, err := doRequest(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if got[0] != "hand-typed" {
		t.Fatalf("Authorization = %q, want the hand-typed header to win", got[0])
	}
	m.t().headers = nil

	// a save round-trip keeps the auth config
	m.t().auth = authConfig{Type: "basic", Username: "u", Password: "p"}
	s := m.t().toSaved("x")
	m2 := newModel()
	m2.t().applySaved(s)
	if m2.t().auth.Type != "basic" || m2.t().auth.Username != "u" {
		t.Fatalf("auth after round-trip = %+v", m2.t().auth)
	}

	// the collection runner synthesizes the same headers
	o = toOutgoing(s, true, nil)
	if o.Headers[0].Key != "Authorization" || !strings.HasPrefix(o.Headers[0].Value, "Basic ") {
		t.Fatalf("runner headers = %+v, want the Basic header first", o.Headers)
	}
}

func TestSendRequestThroughTheUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	m := newModel()
	driver := open(t, m)
	m.t().url = srv.URL
	m.t().method = "GET"

	click(t, driver, "发送")
	waitFor(t, driver, func() bool { return m.t().resp != nil && !m.t().sending })

	if m.t().resp.Status != 200 {
		t.Fatalf("status = %d, want 200", m.t().resp.Status)
	}
	if !strings.Contains(m.t().resp.Body, "hello") {
		t.Fatalf("body = %q, want hello", m.t().resp.Body)
	}
	if len(m.history) == 0 {
		t.Fatal("sending did not record a history entry")
	}
}

// onScreen reports whether a control with the exact given name is painted.
func onScreen(t *testing.T, driver *app.Driver, name string) bool {
	t.Helper()
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == name && !hit.IsLabel {
			return true
		}
	}
	return false
}

func TestAddParamAndHeader(t *testing.T) {
	m := newModel()
	driver := open(t, m)

	click(t, driver, "添加参数")
	waitFor(t, driver, func() bool { return len(m.t().params) == 1 })
	// The new row must actually show: a List without Grow collapses to zero
	// height and renders nothing (see kvPanel).
	if !onScreen(t, driver, "键") || !onScreen(t, driver, "值") {
		t.Fatal("the added param row is not visible")
	}

	click(t, driver, "请求头")
	click(t, driver, "添加请求头")
	waitFor(t, driver, func() bool { return len(m.t().headers) == 1 })
	if !onScreen(t, driver, "键") || !onScreen(t, driver, "值") {
		t.Fatal("the added header row is not visible")
	}
}

func TestAddEnvVarShowsRow(t *testing.T) {
	m := newModel()
	driver := open(t, m)

	click(t, driver, "环境")
	click(t, driver, "添加变量")
	waitFor(t, driver, func() bool { return len(m.activeEnvs()) == 1 })
	if !onScreen(t, driver, "名称") || !onScreen(t, driver, "值") {
		t.Fatal("the added env var row is not visible")
	}
}

func TestSaveToCollection(t *testing.T) {
	m := newModel()
	driver := open(t, m)
	m.t().url = "https://example.com"

	click(t, driver, "保存到收藏")
	waitFor(t, driver, func() bool { return m.saveOpen })
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == "接口名" && !hit.IsLabel {
			b := hit.Bounds
			if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
				t.Fatal(err)
			}
			if err := driver.Compose("我的接口"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	click(t, driver, "保存")
	waitFor(t, driver, func() bool { return len(m.collections) == 1 })
	if m.collections[0].Name != "我的接口" {
		t.Fatalf("name = %q, want 我的接口", m.collections[0].Name)
	}
	if m.collections[0].Group != "" {
		t.Fatalf("group = %q, want 未分组 (empty)", m.collections[0].Group)
	}
}

func TestGenerateCode(t *testing.T) {
	o := &outgoing{
		Method:   "POST",
		URL:      "https://api.example.com/x",
		Params:   []kv{{Key: "a", Value: "1"}},
		Headers:  []kv{{Key: "X-Token", Value: "abc"}},
		BodyType: "json",
		Body:     `{"k":1}`,
	}
	curl := generateCode("curl", o, true)
	if !strings.Contains(curl, "curl -X POST") || !strings.Contains(curl, "-H 'X-Token: abc'") || !strings.Contains(curl, "-L") {
		t.Fatalf("curl = %q", curl)
	}
	goCode := generateCode("go", o, true)
	if !strings.Contains(goCode, `http.NewRequest("POST"`) || !strings.Contains(goCode, "req.Header.Set(\"X-Token\", \"abc\")") {
		t.Fatalf("go = %q", goCode)
	}
	js := generateCode("javascript", o, true)
	if !strings.Contains(js, "fetch(") || !strings.Contains(js, "method: \"POST\"") {
		t.Fatalf("js = %q", js)
	}
	py := generateCode("python", o, true)
	if !strings.Contains(py, "requests.request") || !strings.Contains(py, "X-Token") {
		t.Fatalf("py = %q", py)
	}
}

func TestFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/from" {
			http.Redirect(w, r, "/to", http.StatusFound)
			return
		}
		fmt.Fprint(w, "done")
	}))
	defer srv.Close()

	res, _ := doRequest(context.Background(), &outgoing{Method: "GET", URL: srv.URL + "/from", FollowRedirects: true})
	if res.Status != 200 {
		t.Fatalf("following: status = %d, want 200", res.Status)
	}
	res2, _ := doRequest(context.Background(), &outgoing{Method: "GET", URL: srv.URL + "/from", FollowRedirects: false})
	if res2.Status != 302 {
		t.Fatalf("not following: status = %d, want 302", res2.Status)
	}
}

func TestFormData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		v := r.FormValue("name")
		f, _, _ := r.FormFile("file")
		data, _ := io.ReadAll(f)
		fmt.Fprintf(w, "name=%s file=%s", v, string(data))
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "up.txt")
	if err := os.WriteFile(file, []byte("hello-file"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := doRequest(context.Background(), &outgoing{
		Method:   "POST",
		URL:      srv.URL,
		BodyType: "form-data",
		FormFields: []formField{
			{Key: "name", Value: "alice"},
			{Key: "file", IsFile: true, FilePath: file},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Body, "name=alice") || !strings.Contains(res.Body, "file=hello-file") {
		t.Fatalf("body = %q", res.Body)
	}
}

func TestRunCollection(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := newModel()
	driver := open(t, m)
	m.collections = []savedRequest{
		{Name: "one", Method: "GET", URL: srv.URL + "/1"},
		{Name: "two", Method: "GET", URL: srv.URL + "/2"},
	}
	m.startRun(allIndices(2))
	waitFor(t, driver, func() bool { return !m.running && len(m.runResults) == 2 })
	if len(m.runResults) != 2 {
		t.Fatalf("results = %d, want 2 (hits=%d)", len(m.runResults), hits.Load())
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestDeleteCollection(t *testing.T) {
	m := newModel()
	driver := open(t, m)
	m.collections = []savedRequest{
		{Name: "one", Method: "GET", URL: "https://a.example"},
		{Name: "two", Method: "POST", URL: "https://b.example"},
	}
	m.rebuildCollRows()
	m.invalidate()

	// the collection list is a tree now: a row's delete lives in its context menu
	waitFor(t, driver, func() bool {
		for _, hit := range driver.Snapshot().Hits {
			if hit.Label == "one" {
				return true
			}
		}
		return false
	})
	rightClick(t, driver, "one")
	click(t, driver, "删除")
	// the confirm dialog stands between the menu and the deletion
	waitFor(t, driver, func() bool { return shows(driver, "确定要删除收藏") })
	click(t, driver, "删除") // the dialog's destructive button
	waitFor(t, driver, func() bool { return len(m.collections) == 1 })
	if m.collections[0].Name != "two" {
		t.Fatalf("after delete: collections = %v, want [two]", m.collections)
	}
}

func TestCollectionGroups(t *testing.T) {
	m := newModel()
	m.collections = []savedRequest{
		{Name: "a", Method: "GET"},
		{Name: "b", Method: "POST", Group: "服务"},
		{Name: "c", Method: "GET", Group: "服务"},
	}
	m.rebuildCollRows()

	// a group referenced by its members shows even though no folder was created;
	// requests without a group sit at the root, beside the folders
	if len(m.collRows) != 4 {
		t.Fatalf("rows = %+v, want 4 (服务, b, c, a)", m.collRows)
	}
	if !m.collRows[0].group || m.collRows[0].name != "服务" || m.collRows[0].count != 2 {
		t.Fatalf("first row = %+v, want the 服务 group of 2", m.collRows[0])
	}
	if m.collRows[1].coll != 1 || m.collRows[2].coll != 2 || m.collRows[3].coll != 0 {
		t.Fatalf("rows = %+v", m.collRows)
	}

	// folding a group hides only its members
	m.toggleGroup("服务")
	if len(m.collRows) != 2 {
		t.Fatalf("folded rows = %+v, want 2 (服务, a)", m.collRows)
	}

	// moving the last root request into the folder unfolds it
	m.moveCollTo(0, "服务")
	if len(m.collRows) != 4 {
		t.Fatalf("after move: rows = %+v, want 4 (服务, a, b, c)", m.collRows)
	}
	if m.collRows[1].coll != 0 {
		t.Fatalf("first member = %+v, want a", m.collRows[1])
	}

	// renaming carries the members with the group, even a derived one
	m.groupRename = "服务"
	m.groupName = "用户服务"
	m.confirmGroupDialog()
	if m.groups[0] != "用户服务" || m.collections[1].Group != "用户服务" {
		t.Fatalf("after rename: groups = %v, b.Group = %q", m.groups, m.collections[1].Group)
	}

	// deleting a folder drops its requests back to the root
	m.deleteGroup("用户服务")
	if len(m.groups) != 0 || m.collections[1].Group != "" {
		t.Fatalf("after delete: groups = %v, b.Group = %q", m.groups, m.collections[1].Group)
	}
	if len(m.collRows) != 3 || m.collRows[0].group {
		t.Fatalf("rows = %+v, want three root requests", m.collRows)
	}
}

func TestEmptyGroupPersists(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.groups = []string{"空组"}
	m.rebuildCollRows()
	if len(m.collRows) != 1 || !m.collRows[0].group || m.collRows[0].count != 0 {
		t.Fatalf("rows = %+v, want the empty folder alone", m.collRows)
	}
	m.persist()

	// the folder survives a restart: it lives in the store, not in its members
	m2 := newModel()
	var found bool
	for _, g := range m2.groups {
		if g == "空组" {
			found = true
		}
	}
	if !found || len(m2.collRows) != 1 || !m2.collRows[0].group {
		t.Fatalf("after reload: groups = %v rows = %+v, want 空组 kept", m2.groups, m2.collRows)
	}
}

// TestSplitterFollowsDrag guards the panes against a Fill: a pane carrying
// Width/Height from a SplitterState's Pos must not also Fill, or the fill takes
// the room, the state moves and the panes stand still.
func TestSplitterFollowsDrag(t *testing.T) {
	m := newModel()
	driver := open(t, m)

	splitterOf := func(horizontal bool) gfx.Hit {
		for _, hit := range driver.Snapshot().Hits {
			if hit.Label != "Resize panes" {
				continue
			}
			if horizontal && hit.Bounds.Width > hit.Bounds.Height {
				return hit
			}
			if !horizontal && hit.Bounds.Width < hit.Bounds.Height {
				return hit
			}
		}
		return gfx.Hit{}
	}

	vertical := splitterOf(false)
	if vertical.Bounds.X != m.splitSide.Pos {
		t.Fatalf("vertical splitter at %v, want the sidebar's Pos %v", vertical.Bounds, m.splitSide.Pos)
	}

	// dragging right by 60px moves the state and the bar with it
	x := vertical.Bounds.X + vertical.Bounds.Width/2
	y := vertical.Bounds.Y + 300
	drag(t, driver, x, y, x+60, y)
	if err := driver.Advance(16 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if m.splitSide.Pos != 300 {
		t.Fatalf("after drag: side.Pos = %v, want 300", m.splitSide.Pos)
	}
	if at := splitterOf(false).Bounds.X; at != 300 {
		t.Fatalf("after drag the bar sits at %v, want 300", at)
	}

	// and the horizontal one follows too
	horizontal := splitterOf(true)
	if horizontal.Bounds.Y != 30+m.split.Pos {
		t.Fatalf("horizontal splitter at %v, want below the request panel (%v)", horizontal.Bounds, 30+m.split.Pos)
	}
	x = horizontal.Bounds.X + 200
	y = horizontal.Bounds.Y + horizontal.Bounds.Height/2
	drag(t, driver, x, y, x, y-80)
	if err := driver.Advance(16 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if m.split.Pos != 240 {
		t.Fatalf("after drag: split.Pos = %v, want 240", m.split.Pos)
	}
}

func TestShortcuts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := newModel()
	driver := open(t, m)
	m.t().url = srv.URL

	// Ctrl+Enter sends from anywhere
	if err := driver.Key(gfx.KeyEnter, ui.Primary); err != nil {
		t.Fatal(err)
	}
	waitFor(t, driver, func() bool { return m.t().resp != nil && !m.t().sending })
	if m.t().resp.Status != 200 {
		t.Fatalf("status = %d, want 200", m.t().resp.Status)
	}

	// Ctrl+S opens the save dialog, Escape leaves it, Ctrl+N clears the editor
	if err := driver.Chord(ui.Primary, "s"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, driver, func() bool { return m.saveOpen })
	if err := driver.Key(gfx.KeyEscape, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, driver, func() bool { return !m.saveOpen })

	m.t().url = "https://keep.example"
	m.t().method = "POST"
	if err := driver.Chord(ui.Primary, "n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, driver, func() bool { return !m.t().sending && m.t().url == "" })
	if m.t().method != "GET" {
		t.Fatalf("method = %s, want GET after Ctrl+N", m.t().method)
	}
}

func TestEnvRename(t *testing.T) {
	m := newModel()
	m.envSets = []envSet{
		{Name: "默认"},
		{Name: "开发", Vars: []kv{{Key: "host", Value: "dev.example.com"}}},
	}
	m.activeEnvName = "开发"

	m.openEnvDialog("开发")
	m.envName = "测试"
	m.confirmEnvDialog()
	if m.envSets[1].Name != "测试" || m.activeEnvName != "测试" {
		t.Fatalf("after rename: sets = %+v active = %q", m.envSets, m.activeEnvName)
	}
	if len(m.envSets[1].Vars) != 1 {
		t.Fatalf("vars = %v, want them kept with the set", m.envSets[1].Vars)
	}
	// the new name reached the store
	data := loadStore()
	var found bool
	for _, e := range data.EnvSets {
		if e.Name == "测试" {
			found = true
		}
	}
	if !found {
		t.Fatalf("store env sets = %+v, want 测试 persisted", data.EnvSets)
	}

	// a duplicate name is rejected, the dialog stays open
	m.openEnvDialog("测试")
	m.envName = "默认"
	m.confirmEnvDialog()
	if m.envSets[1].Name != "测试" {
		t.Fatalf("duplicate rename applied: sets = %+v", m.envSets)
	}
	if !m.envOpen {
		t.Fatal("a rejected rename should keep the dialog open")
	}
	m.envOpen = false

	// an empty name is rejected too
	m.openEnvDialog("测试")
	m.envName = "   "
	m.confirmEnvDialog()
	if m.envSets[1].Name != "测试" || !m.envOpen {
		t.Fatalf("empty rename applied: sets = %+v open = %v", m.envSets, m.envOpen)
	}
}

func TestEditCollectionEntry(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.groups = []string{"服务"}
	m.rebuildCollRows()

	// 新建接口 in a group: an empty entry lands there and the editor holds it
	m.newInGroup("服务")
	if len(m.collections) != 1 || m.collections[0].Group != "服务" {
		t.Fatalf("collections = %+v, want one 服务 entry", m.collections)
	}
	if _, editing := m.editingEntry(); !editing {
		t.Fatal("新建接口 should put the editor in edit mode")
	}
	if m.t().method != "GET" || m.t().url != "" {
		t.Fatalf("editor = %s %q, want a blank GET", m.t().method, m.t().url)
	}

	// filling it in and saving updates the entry in place: no duplicate
	m.t().url = "https://api.example.com/users"
	m.saveName = "获取用户"
	m.saveGroup = "服务"
	m.confirmSaveDialog()
	if len(m.collections) != 1 {
		t.Fatalf("collections = %+v, want the entry updated, not appended", m.collections)
	}
	e := m.collections[0]
	if e.Name != "获取用户" || e.URL != "https://api.example.com/users" || e.Group != "服务" {
		t.Fatalf("entry = %+v, want the saved changes", e)
	}
	if _, editing := m.editingEntry(); !editing {
		t.Fatal("saving should keep the edit mode")
	}

	// opening an entry enters edit mode, 新建 leaves it
	m.loadCollection(0)
	if _, editing := m.editingEntry(); !editing {
		t.Fatal("loadCollection should enter edit mode")
	}
	m.newRequest()
	if _, editing := m.editingEntry(); editing {
		t.Fatal("新建 should leave edit mode")
	}

	// deleting the entry being edited clears the edit mode
	m.collections = []savedRequest{{Name: "a"}, {Name: "b"}}
	m.t().editing = 1
	m.deleteCollection(0)
	if m.t().editing != 0 {
		t.Fatalf("editing = %d, want 0 after the shift", m.t().editing)
	}
	m.deleteCollection(0)
	if _, editing := m.editingEntry(); editing {
		t.Fatal("deleting the edited entry should clear edit mode")
	}
}

func TestCollectionSearch(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak") // a fresh store: earlier tests may have persisted groups
	m := newModel()
	m.collections = []savedRequest{
		{Name: "获取用户"},
		{Name: "创建订单"},
		{Name: "健康检查"},
	}
	m.rebuildCollRows()
	if len(m.collRows) != 3 {
		t.Fatalf("rows = %+v, want all three", m.collRows)
	}

	// a query flattens the tree to the matching requests
	m.collQuery = "用户"
	m.rebuildCollRows()
	if len(m.collRows) != 1 || m.collRows[0].coll != 0 {
		t.Fatalf("filtered rows = %+v, want 获取用户 only", m.collRows)
	}

	// clearing it brings the tree back
	m.collQuery = ""
	m.rebuildCollRows()
	if len(m.collRows) != 3 {
		t.Fatalf("rows after clear = %+v, want all three", m.collRows)
	}
}

func TestKVDisabled(t *testing.T) {
	var q, h string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, h = r.URL.RawQuery, r.Header.Get("X-Token")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	o := &outgoing{
		Method:  "GET",
		URL:     srv.URL,
		Params:  []kv{{Key: "on", Value: "1"}, {Key: "off", Value: "1", Disabled: true}},
		Headers: []kv{{Key: "X-Token", Value: "abc"}, {Key: "X-Off", Value: "1", Disabled: true}},
	}
	if _, err := doRequest(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "on=1") || strings.Contains(q, "off=1") {
		t.Fatalf("query = %q, want on=1 without off=1", q)
	}
	if h != "abc" {
		t.Fatalf("X-Token = %q, want abc", h)
	}

	// the generators skip the disabled rows too
	curl := generateCode("curl", o, false)
	if strings.Contains(curl, "off=1") || strings.Contains(curl, "X-Off") {
		t.Fatalf("curl = %q, want the disabled rows gone", curl)
	}
}

func TestHistoryGroups(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	yest := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	m := newModel()
	m.history = []savedRequest{
		{Name: "newest", Time: today + " 10:00:00"},
		{Name: "second", Time: today + " 09:00:00"},
		{Name: "old", Time: yest + " 08:00:00"},
		{Name: "ancient", Time: "2020-01-01 07:00:00"},
		{Name: "legacy", Time: "07:30:00"},
	}
	m.rebuildHistRows()
	// 今天, second, newest — wait: newest first. 今天, newest, second, 昨天, old, 2020-01-01, ancient, 更早, legacy
	if len(m.histRows) != 9 {
		t.Fatalf("rows = %+v, want 9", m.histRows)
	}
	if !m.histRows[0].header || m.histRows[0].label != "今天" {
		t.Fatalf("row 0 = %+v, want the 今天 heading", m.histRows[0])
	}
	if m.histRows[1].idx != 0 || m.histRows[2].idx != 1 {
		t.Fatalf("today rows = %+v", m.histRows[1:3])
	}
	if !m.histRows[3].header || m.histRows[3].label != "昨天" || m.histRows[4].idx != 2 {
		t.Fatalf("yesterday = %+v", m.histRows[3:5])
	}
	if !m.histRows[5].header || m.histRows[5].label != "2020-01-01" || m.histRows[6].idx != 3 {
		t.Fatalf("dated = %+v", m.histRows[5:7])
	}
	if !m.histRows[7].header || m.histRows[7].label != "更早" || m.histRows[8].idx != 4 {
		t.Fatalf("legacy = %+v", m.histRows[7:9])
	}
}

func TestDragReorder(t *testing.T) {
	m := newModel()
	m.groups = []string{"G1", "G2"}
	m.collections = []savedRequest{
		{Name: "a", Group: "G1"},
		{Name: "b", Group: "G1"},
		{Name: "c", Group: "G2"},
	}
	m.rebuildCollRows()
	// rows: G1, a, b, G2, c
	if len(m.collRows) != 5 {
		t.Fatalf("rows = %+v, want 5", m.collRows)
	}

	// drag b before a: same folder, order changes, nothing moves folders
	m.dropCollRow(ui.TreeMove{From: 2, To: 1, Where: ui.TreeDropBefore})
	if m.collections[0].Name != "b" || m.collections[1].Name != "a" {
		t.Fatalf("collections = %+v, want b before a", m.collections)
	}

	// drag c after b across folders: it joins G1 right after b
	// rows are now G1, b, a, G2, c
	m.dropCollRow(ui.TreeMove{From: 4, To: 1, Where: ui.TreeDropAfter})
	if m.collections[1].Name != "c" || m.collections[1].Group != "G1" {
		t.Fatalf("collections = %+v, want c after b in G1", m.collections)
	}

	// drag the G2 folder before G1: the folders swap
	// rows are now G1, b, c, a, G2
	m.dropCollRow(ui.TreeMove{From: 4, To: 0, Where: ui.TreeDropBefore})
	if len(m.groups) != 2 || m.groups[0] != "G2" {
		t.Fatalf("groups = %v, want G2 first", m.groups)
	}

	// the editor's entry index follows the entry it edits
	m.collections = []savedRequest{{Name: "x"}, {Name: "y"}, {Name: "z"}}
	m.t().editing = 2 // z
	m.reorderCollTo(0, 2, true)
	if m.collections[1].Name != "x" {
		t.Fatalf("collections = %+v, want x before z", m.collections)
	}
	if m.t().editing != 2 {
		t.Fatalf("editing = %d, want 2 (z still)", m.t().editing)
	}
}

func TestNestedGroups(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.groups = []string{"G1", "G1/子"}
	m.collections = []savedRequest{
		{Name: "x", Group: "G1/子"},
		{Name: "y", Group: "G1"},
		{Name: "z"},
	}
	m.rebuildCollRows()
	// rows: G1(d0), 子(d1), x(d2), y(d1), z(d0)
	if len(m.collRows) != 5 {
		t.Fatalf("rows = %+v, want 5", m.collRows)
	}
	if !m.collRows[0].group || m.collRows[0].name != "G1" ||
		!m.collRows[1].group || m.collRows[1].name != "G1/子" {
		t.Fatalf("folders = %+v", m.collRows[0:2])
	}
	if m.collRows[2].coll != 0 || m.collRows[3].coll != 1 || m.collRows[4].coll != 2 {
		t.Fatalf("members = %+v", m.collRows[2:5])
	}
	for i, want := range []int{0, 1, 2, 1, 0} {
		if d := m.collNodeAt(i).Depth; d != want {
			t.Fatalf("depth of row %d = %d, want %d", i, d, want)
		}
	}

	// a folder nested into another takes its requests and subfolders with it
	m.groups = append(m.groups, "G2")
	m.moveFolderTo("G1/子", "G2")
	if m.groups[1] != "G2" || m.groups[2] != "G2/子" {
		t.Fatalf("groups = %v, want 子 moved under G2", m.groups)
	}
	if m.collections[0].Group != "G2/子" {
		t.Fatalf("x.Group = %q, want G2/子", m.collections[0].Group)
	}

	// renaming a folder rewrites its subfolders and their members
	m.openGroupDialog("G2")
	if m.groupName != "G2" {
		t.Fatalf("dialog name = %q, want the folder's own name", m.groupName)
	}
	m.groupName = "工作"
	m.confirmGroupDialog()
	if m.groups[1] != "工作" || m.groups[2] != "工作/子" || m.collections[0].Group != "工作/子" {
		t.Fatalf("after rename: groups = %v x.Group = %q", m.groups, m.collections[0].Group)
	}

	// a folder created through the dialog nests under its parent
	m.openNewGroupDialog("G1")
	m.groupName = "S"
	m.confirmGroupDialog()
	if m.groupIndex("G1/S") < 0 {
		t.Fatalf("groups = %v, want G1/S created", m.groups)
	}

	// deleting the mid folder sends its requests to the folder's parent
	m.deleteGroup("工作")
	if len(m.groups) != 2 || m.groupIndex("G1/S") < 0 {
		t.Fatalf("groups = %v, want 工作 and 工作/子 gone, G1/S kept", m.groups)
	}
	if m.collections[0].Group != "" {
		t.Fatalf("x.Group = %q, want it back at the root", m.collections[0].Group)
	}
}

func TestURLParamsAutoFill(t *testing.T) {
	m := newModel()
	driver := open(t, m)
	m.t().params = []kv{{Key: "mine", Value: "keep"}}
	m.t().url = "https://api.example.com/list?page=2&size=10"
	m.invalidate()
	waitFor(t, driver, func() bool {
		return len(m.t().params) == 3 && m.t().params[0].Key == "mine" &&
			m.t().params[1].Key == "page" && m.t().params[1].Value == "2" &&
			m.t().params[2].Key == "size" && m.t().params[2].Value == "10"
	})

	// params that leave the URL leave the table; user rows stay untouched
	m.t().url = "https://api.example.com/list?page=3"
	m.invalidate()
	waitFor(t, driver, func() bool {
		return len(m.t().params) == 2 && m.t().params[0].Key == "mine" &&
			m.t().params[1].Key == "page" && m.t().params[1].Value == "3"
	})
}

func TestURLParamsBidirectional(t *testing.T) {
	m := newModel()
	driver := open(t, m)

	// a blank row is a slot: a URL with a query fills it instead of appending
	m.t().params = []kv{{}}
	m.t().url = "https://api.example.com/list?id=1"
	m.invalidate()
	waitFor(t, driver, func() bool {
		return len(m.t().params) == 1 && m.t().params[0].Key == "id" && m.t().params[0].Value == "1"
	})

	// editing the table writes the query back into the URL
	m.t().params = []kv{{Key: "id", Value: "2"}, {Key: "tag", Value: "x"}}
	m.invalidate()
	waitFor(t, driver, func() bool {
		return m.t().url == "https://api.example.com/list?id=2&tag=x"
	})

	// toggling a row off drops it from the URL, on brings it back
	m.t().params[1].Disabled = true
	m.invalidate()
	waitFor(t, driver, func() bool { return m.t().url == "https://api.example.com/list?id=2" })
	m.t().params[1].Disabled = false
	m.invalidate()
	waitFor(t, driver, func() bool { return m.t().url == "https://api.example.com/list?id=2&tag=x" })

	// editing the URL back to the bare path removes the URL-derived rows
	// (the URL is the source whenever it changes); user rows stay
	m.t().params = append(m.t().params, kv{Key: "mine", Value: "keep"})
	m.t().url = "https://api.example.com/list"
	m.invalidate()
	waitFor(t, driver, func() bool {
		return len(m.t().params) == 1 && m.t().params[0].Key == "mine"
	})
}

func TestChineseParams(t *testing.T) {
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("name")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	m := newModel()
	driver := open(t, m)
	m.t().url = srv.URL + "/x"
	m.invalidate()
	waitFor(t, driver, func() bool { return m.t().urlSeen == m.t().url }) // the bare path settles

	// filling the table writes the query, 中文 readable — no %E9 mojibake
	m.t().params = []kv{{Key: "name", Value: "颠三"}}
	m.invalidate()
	waitFor(t, driver, func() bool {
		return strings.Contains(m.t().url, "name=颠三") && !strings.Contains(m.t().url, "%E9")
	})

	// and sending reaches the server with the value decoded
	click(t, driver, "发送")
	waitFor(t, driver, func() bool {
		return m.t().resp != nil && gotName == "颠三"
	})
}

func TestBodyJSONCheckAndFormat(t *testing.T) {
	m := newModel()
	m.t().bodyType = "json"

	// valid body
	m.t().body = `{"a":1,"b":[1,2]}`
	m.t().checkBodyJSON()
	if !m.t().bodyCheck.valid {
		t.Fatalf("check = %+v, want valid", m.t().bodyCheck)
	}

	// a positional syntax error is located
	m.t().body = "{\"a\":1 \"b\":2}"
	m.t().checkBodyJSON()
	if m.t().bodyCheck.valid {
		t.Fatal("invalid body reported valid")
	}
	if m.t().bodyCheck.line != 1 || m.t().bodyCheck.col != 8 {
		t.Fatalf("check = %+v, want the error at line 1 col 8", m.t().bodyCheck)
	}

	// 格式化 pretty-prints and the check turns valid again
	m.t().body = `{"a":1, "b":2}`
	m.formatBodyJSON()
	if m.t().body != "{\n  \"a\": 1,\n  \"b\": 2\n}" {
		t.Fatalf("formatted = %q", m.t().body)
	}
	if !m.t().bodyCheck.valid {
		t.Fatal("formatted body should be valid")
	}

	// an empty body is valid (no body)
	m.t().body = ""
	m.t().checkBodyJSON()
	if !m.t().bodyCheck.valid {
		t.Fatal("empty body should be valid")
	}

	// invalid bodies do not get mangled by 格式化
	m.t().body = `{"broken":`
	m.formatBodyJSON()
	if m.t().body != `{"broken":` {
		t.Fatalf("格式化 mangled an invalid body: %q", m.t().body)
	}
}

func TestCopyAsCurl(t *testing.T) {
	m := newModel()
	driver := open(t, m)
	m.collections = []savedRequest{
		{Name: "获取用户", Method: "GET", URL: "https://api.example.com/users?page=2"},
	}
	m.rebuildCollRows()
	m.invalidate()
	waitFor(t, driver, func() bool {
		for _, hit := range driver.Snapshot().Hits {
			if hit.Label == "获取用户" {
				return true
			}
		}
		return false
	})

	rightClick(t, driver, "获取用户")
	click(t, driver, "复制为")
	click(t, driver, "cURL")
	waitFor(t, driver, func() bool {
		clip := driver.Clipboard()
		return strings.Contains(clip, "curl -X GET") && strings.Contains(clip, "users?page=2")
	})
}

func TestHistoryDeleteAndClear(t *testing.T) {
	m := newModel()
	m.history = []savedRequest{{Name: "one"}, {Name: "two"}}
	m.rebuildHistRows()

	m.deleteHistoryEntry(0)
	if len(m.history) != 1 || m.history[0].Name != "two" {
		t.Fatalf("after delete: history = %+v", m.history)
	}
	// the remaining entry keeps its 更早 heading above it
	if len(m.histRows) != 2 || !m.histRows[0].header || m.histRows[1].idx != 0 {
		t.Fatalf("rows = %+v, want the 更早 heading over the entry", m.histRows)
	}

	m.clearHistory()
	if len(m.history) != 0 || len(m.histRows) != 0 {
		t.Fatalf("after clear: history = %+v rows = %+v, want empty", m.history, m.histRows)
	}
}

func TestSaveResponseToFile(t *testing.T) {
	m := newModel()
	driver := open(t, m)
	path := filepath.Join(t.TempDir(), "resp.json")
	if err := driver.QueueSaveFile(path, nil); err != nil {
		t.Fatal(err)
	}
	m.t().resp = &httpResult{Body: `{"hello":"world"}`, Status: 200, StatusText: "200 OK"}
	m.invalidate()
	waitFor(t, driver, func() bool {
		for _, hit := range driver.Snapshot().Hits {
			if hit.Label == "保存到文件" && !hit.IsLabel {
				return true
			}
		}
		return false
	})

	click(t, driver, "保存到文件")
	waitFor(t, driver, func() bool {
		data, err := os.ReadFile(path)
		return err == nil && string(data) == `{"hello":"world"}`
	})
}

func TestCollectionsExportImportRoundTrip(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.groups = []string{"G1", "G1/子"}
	m.collections = []savedRequest{
		{Name: "x", Group: "G1/子", Method: "POST", URL: "https://x/a"},
		{Name: "y", Group: "G1"},
		{Name: "z"},
	}
	m.rebuildCollRows()

	doc := m.exportCollectionsJSON()

	// a fresh model imports the document: folders merge, requests append
	m2 := newModel()
	ng, nr, err := m2.importCollectionsJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	if ng != 2 || nr != 3 {
		t.Fatalf("newGroups = %d newRequests = %d", ng, nr)
	}
	if len(m2.groups) != 2 || m2.groups[0] != "G1" || m2.groups[1] != "G1/子" {
		t.Fatalf("groups = %v", m2.groups)
	}
	if len(m2.collections) != 3 {
		t.Fatalf("collections = %+v", m2.collections)
	}
	var x savedRequest
	for _, s := range m2.collections {
		if s.Name == "x" {
			x = s
		}
	}
	if x.Group != "G1/子" {
		t.Fatalf("x.Group = %q, want the nested path kept", x.Group)
	}
	m2.rebuildCollRows()
	// the tree: G1, 子, x, y, z
	if len(m2.collRows) != 5 {
		t.Fatalf("rows = %+v, want 5", m2.collRows)
	}
}

func TestJSONTree(t *testing.T) {
	root := parseJSONTree(`{"ok":true,"items":[{"id":1}],"note":"你好"}`)
	if root == nil || !root.branch || !root.open {
		t.Fatal("the root is not an expanded branch")
	}
	if len(root.children) != 3 {
		t.Fatalf("children = %d, want 3", len(root.children))
	}

	m := newModel()
	m.t().jsonRoot = root
	m.t().rebuildJSONRows()
	// the root expanded, its members collapsed: root, items, note, ok
	if len(m.t().jsonRows) != 4 {
		t.Fatalf("rows = %d, want 4", len(m.t().jsonRows))
	}
	if m.jsonLabelAt(0) != "{" {
		t.Fatalf("root label = %q, want {", m.jsonLabelAt(0))
	}

	// opening the items array shows its members
	m.t().toggleJSONRow(1)
	if len(m.t().jsonRows) != 5 {
		t.Fatalf("rows after open = %d, want 5 (root, items, 0, note, ok)", len(m.t().jsonRows))
	}
	if n := m.t().jsonRows[2]; !n.branch || n.depth != 2 {
		t.Fatalf("row 2 = %+v, want the inner object at depth 2", n)
	}

	m.t().toggleJSONRow(2) // open the {"id":1} object
	if len(m.t().jsonRows) != 6 {
		t.Fatalf("rows = %d, want 6", len(m.t().jsonRows))
	}

	if parseJSONTree("hello") != nil || parseJSONTree("") != nil || parseJSONTree("{bad") != nil {
		t.Fatal("non-JSON bodies must not build a tree")
	}
	if empty := parseJSONTree(`{}`); empty == nil || empty.branch || empty.valText != "{}" {
		t.Fatalf("empty object = %+v, want a {} leaf", empty)
	}

	// a response builds its tree when it lands
	m2 := newModel()
	m2.applyResult(0, httpResult{Status: 200, Body: `[1,2]`}, nil)
	if m2.t().jsonRoot == nil || len(m2.t().jsonRows) != 3 {
		t.Fatalf("applyResult: rows = %d, want 3 (root, 0, 1)", len(m2.t().jsonRows))
	}
}

// freshTabs drops any tabs a previous test persisted: tabs now survive
// restarts through the store, so a test that counts or indexes them starts
// from one blank tab.
func freshTabs(m *model) {
	m.tabs = nil
	m.tabSeq = 0
	m.newTab()
	m.tabIdx = 0
}

func TestNewRequest(t *testing.T) {
	m := newModel()
	freshTabs(m)
	driver := open(t, m)
	m.t().method = "POST"
	m.t().url = "https://example.com"
	m.t().params = []kv{{Key: "a", Value: "1"}}
	m.t().bodyType = "json"
	m.t().body = `{"x":1}`
	m.invalidate()

	click(t, driver, "新建")
	waitFor(t, driver, func() bool { return m.t().method == "GET" && m.t().url == "" })
	if len(m.t().params) != 0 || m.t().body != "" || m.t().bodyType != "none" {
		t.Fatalf("new request did not clear: method=%s url=%q params=%d bodyType=%s body=%q",
			m.t().method, m.t().url, len(m.t().params), m.t().bodyType, m.t().body)
	}
	// 新建 now opens a second tab; the first keeps what it held
	if len(m.tabs) != 2 {
		t.Fatalf("tabs = %d, want 2 after 新建", len(m.tabs))
	}
	first := m.tabs[0]
	if first.method != "POST" || first.url != "https://example.com" || len(first.params) != 1 || first.body != `{"x":1}` {
		t.Fatalf("the first tab lost its request: %s %q", first.method, first.url)
	}
}

// TestRequestTabs drives the tab strip: a request per tab, switching by click,
// closing by ×, and a collection entry focused in its own tab instead of
// opened twice.
func TestRequestTabs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tab":true}`)
	}))
	defer srv.Close()

	m := newModel()
	freshTabs(m)
	driver := open(t, m)

	// tab 0 sends and holds a response
	m.t().url = srv.URL
	m.invalidate()
	click(t, driver, "发送")
	waitFor(t, driver, func() bool { return m.t().resp != nil && !m.t().sending })

	// a new tab is blank; tab 0 keeps its response behind it
	click(t, driver, "新建标签")
	waitFor(t, driver, func() bool { return len(m.tabs) == 2 && m.tabIdx == 1 && m.t().url == "" })
	if m.tabs[0].resp == nil {
		t.Fatal("the first tab lost its response")
	}

	// the two tabs hold different requests at the same time
	m.t().url = srv.URL + "/second"
	m.invalidate()
	click(t, driver, "发送")
	waitFor(t, driver, func() bool { return m.t().resp != nil && !m.t().sending })
	if m.tabs[0].url == m.tabs[1].url {
		t.Fatal("both tabs hold the same URL")
	}

	// clicking the first chip switches back: the response is still there
	clickN(t, driver, "GET 127.0.0.1", 0)
	waitFor(t, driver, func() bool { return m.tabIdx == 0 && m.t().url == srv.URL })
	if m.t().resp == nil {
		t.Fatal("the first tab lost its response while in the background")
	}

	// closing the second tab from its × asks first (it has unsaved changes)
	clickN(t, driver, "关闭标签", 1)
	waitFor(t, driver, func() bool { return shows(driver, "有未保存的修改") })
	click(t, driver, "直接关闭")
	waitFor(t, driver, func() bool { return len(m.tabs) == 1 && m.tabIdx == 0 })

	// the last tab closes into a fresh blank one, never nothing (it is dirty
	// too: the request it sent was never saved)
	click(t, driver, "关闭标签")
	waitFor(t, driver, func() bool { return shows(driver, "有未保存的修改") })
	click(t, driver, "直接关闭")
	waitFor(t, driver, func() bool { return len(m.tabs) == 1 && m.t().url == "" && m.t().resp == nil })

	// a collection entry opens in a new tab, then focuses when clicked again
	idx := len(m.collections) // the store may already carry entries from earlier tests
	m.collections = append(m.collections, savedRequest{Name: "用户接口", Method: "GET", URL: srv.URL + "/users"})
	m.rebuildCollRows()
	m.loadCollection(idx)
	waitFor(t, driver, func() bool { return len(m.tabs) == 2 && m.t().url == srv.URL+"/users" })
	m.loadCollection(idx)
	waitFor(t, driver, func() bool { return len(m.tabs) == 2 && m.tabIdx == 1 }) // focused, not duplicated
}

// TestTabDirtyAndDuplicate drives the chip's orange dot and the 复制接口
// action: a blank tab is clean, typing dirties it, saving cleans it, and a
// duplicate inserts a copy after the original while later tabs' entries follow
// the shift.
func TestTabDirtyAndDuplicate(t *testing.T) {
	m := newModel()
	freshTabs(m)
	m.collections = nil // earlier tests persist entries; this one counts them
	if m.tabDirty(m.t()) {
		t.Fatal("a fresh blank tab must be clean")
	}
	m.t().url = "https://api.example.com"
	if !m.tabDirty(m.t()) {
		t.Fatal("a tab with a URL and no entry must be dirty")
	}

	// saving attaches the tab to a collection entry: clean again
	s := m.t().toSaved("接口")
	m.collections = append(m.collections, s)
	m.t().editing = len(m.collections) - 1
	if m.tabDirty(m.t()) {
		t.Fatal("a tab matching its entry must be clean")
	}
	m.t().body = `{"a":1}`
	if !m.tabDirty(m.t()) {
		t.Fatal("an edited tab must be dirty")
	}
	m.t().body = ""

	// a tab editing a LATER entry follows the insert
	m.collections = append(m.collections, savedRequest{Name: "稍后"})
	m.rebuildCollRows()
	m.openTab()
	m.tabs[1].editing = 1

	m.duplicateCollection(0)
	if len(m.collections) != 3 {
		t.Fatalf("collections = %d, want 3 after duplicate", len(m.collections))
	}
	if m.collections[1].Name != "接口 副本" {
		t.Fatalf("copy name = %q, want 接口 副本", m.collections[1].Name)
	}
	if m.collections[1].URL != "https://api.example.com" {
		t.Fatalf("copy URL = %q, want the original's", m.collections[1].URL)
	}
	if m.collections[2].Name != "稍后" {
		t.Fatalf("collections[2] = %q, want 稍后 pushed back", m.collections[2].Name)
	}
	if m.tabs[0].editing != 0 {
		t.Fatalf("tabs[0].editing = %d, want 0 (still the original)", m.tabs[0].editing)
	}
	if m.tabs[1].editing != 2 {
		t.Fatalf("tabs[1].editing = %d, want 2 (稍后 moved 1→2)", m.tabs[1].editing)
	}
	if m.tabDirty(m.tabs[0]) {
		t.Fatal("the original tab must still be clean")
	}
}

// TestTabsPersistAcrossRestart: persist() writes the open tabs, a new model
// reads them back — requests, sub-tabs, the entry each edits, which tab was
// active, and baselines that keep the URL from rewriting itself on the first
// paint.
func TestTabsPersistAcrossRestart(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak") // a fresh store: earlier tests may have persisted tabs
	m := newModel()
	m.collections = nil

	// tab 0: a request that edits a collection entry
	m.t().method = "POST"
	m.t().url = "https://api.example.com/a?x=1"
	m.t().params = []kv{{Key: "x", Value: "1"}}
	m.t().headers = []kv{{Key: "Authorization", Value: "hand"}}
	m.t().bodyType = "json"
	m.t().body = `{"n":1}`
	m.t().auth = authConfig{Type: "bearer", Token: "tok"}
	m.t().reqTab = "请求体"
	s := m.t().toSaved("接口A")
	m.collections = append(m.collections, s)
	m.t().editing = 0
	if m.tabDirty(m.t()) {
		t.Fatal("tab 0 must be clean before the restart")
	}

	// tab 1: a separate request, on screen when the window closes
	m.openTab()
	m.tabs[1].method = "GET"
	m.tabs[1].url = "https://b.example/list"
	m.tabIdx = 1
	m.persist()

	m2 := newModel() // the restart
	if len(m2.tabs) != 2 {
		t.Fatalf("tabs after restart = %d, want 2", len(m2.tabs))
	}
	if m2.tabIdx != 1 {
		t.Fatalf("active tab = %d, want 1", m2.tabIdx)
	}
	a := m2.tabs[0]
	if a.method != "POST" || a.url != "https://api.example.com/a?x=1" ||
		len(a.params) != 1 || len(a.headers) != 1 || a.bodyType != "json" ||
		a.body != `{"n":1}` || a.auth.Token != "tok" || a.reqTab != "请求体" {
		t.Fatalf("tab 0 restored as %s %q %v %v %s %q %+v %s", a.method, a.url, a.params, a.headers, a.bodyType, a.body, a.auth, a.reqTab)
	}
	if a.editing != 0 {
		t.Fatalf("tab 0 editing = %d, want 0", a.editing)
	}
	if a.urlSeen != a.url {
		t.Fatalf("tab 0 urlSeen = %q, want the restored URL (no rewrite on first paint)", a.urlSeen)
	}
	if a.bodyCheck.msg != "" {
		t.Fatalf("tab 0 bodyCheck = %+v, want valid", a.bodyCheck)
	}
	if m2.tabs[1].url != "https://b.example/list" || m2.tabs[1].method != "GET" {
		t.Fatalf("tab 1 restored as %s %q", m2.tabs[1].method, m2.tabs[1].url)
	}
	if m2.tabDirty(m2.tabs[0]) {
		t.Fatal("tab 0 must still be clean after the restart")
	}

	// an entry deleted while away leaves the tab editing nothing
	m2.collections = nil
	m2.persist()
	m3 := newModel()
	if m3.tabs[0].editing != -1 {
		t.Fatalf("editing after the entry vanished = %d, want -1", m3.tabs[0].editing)
	}
}

// TestAssertions drives the declarative checks: status, contains and JSON
// paths (with {{var}} in the needle, array indexing, missing paths, non-JSON
// bodies), the results landing in the tab after a response, and the config
// surviving a save round-trip.
func TestAssertions(t *testing.T) {
	envs := []kv{{Key: "code", Value: "200"}}
	resp := &httpResult{Status: 200, Body: `{"data":{"id":7,"name":"张三"},"tags":["a","b"]}`}

	// every kind passes; {{var}} substitutes in the needle
	rs := runAssertions([]assertion{
		{Kind: "status", Target: "{{code}}"},
		{Kind: "contains", Target: "张三"},
		{Kind: "json", Target: "data.id", Value: "7"},
		{Kind: "json", Target: "data.name", Value: "张三"},
		{Kind: "json", Target: "tags[1]", Value: "b"},
	}, envs, resp)
	if len(rs) != 5 {
		t.Fatalf("results = %d, want 5", len(rs))
	}
	for i, r := range rs {
		if !r.pass {
			t.Fatalf("assertion %d (%s) failed: %s", i, r.desc, r.detail)
		}
	}

	// every kind fails, with a detail that says what actually happened
	rs = runAssertions([]assertion{
		{Kind: "status", Target: "404"},
		{Kind: "contains", Target: "缺失的文本"},
		{Kind: "json", Target: "data.id", Value: "8"},
		{Kind: "json", Target: "data.missing.deep", Value: "x"},
	}, envs, resp)
	for i, r := range rs {
		if r.pass {
			t.Fatalf("assertion %d (%s) should have failed", i, r.desc)
		}
		if r.detail == "" {
			t.Fatalf("failed assertion %d (%s) has no detail", i, r.desc)
		}
	}
	if !strings.Contains(rs[3].detail, "路径不存在") {
		t.Fatalf("missing path detail = %q", rs[3].detail)
	}

	// a non-JSON body fails every path, a blank row is skipped
	rs = runAssertions([]assertion{
		{Kind: "json", Target: "data.id", Value: "7"},
		{Kind: "status", Target: "  "},
	}, envs, &httpResult{Status: 200, Body: "404 page not found"})
	if len(rs) != 1 || rs[0].pass || !strings.Contains(rs[0].detail, "不是 JSON") {
		t.Fatalf("non-JSON results = %+v", rs)
	}

	// the results land in the tab when the response does
	m := newModel()
	freshTabs(m) // earlier tests persist tabs; applyResult below targets tab 0
	m.t().assertions = []assertion{{Kind: "status", Target: "500"}}
	m.applyResult(0, *resp, nil)
	if len(m.tabs[0].assertResults) != 1 || m.tabs[0].assertResults[0].pass {
		t.Fatalf("assertResults after send = %+v, want one failure", m.tabs[0].assertResults)
	}

	// the collection runner counts assertion failures
	rr := runResult{}
	checked := runAssertions([]assertion{
		{Kind: "status", Target: "200"},
		{Kind: "json", Target: "data.id", Value: "8"},
	}, envs, resp)
	for _, cr := range checked {
		if !cr.pass {
			rr.assertFailed++
		}
	}
	rr.assertTotal = len(checked)
	if rr.assertTotal != 2 || rr.assertFailed != 1 {
		t.Fatalf("runner stats = %d/%d, want 2 total 1 failed", rr.assertFailed, rr.assertTotal)
	}
	if !strings.Contains(runStatusText(rr), "断言 1/2") {
		t.Fatalf("runStatusText = %q, want the assertion count", runStatusText(rr))
	}

	// a save round-trip keeps the assertions
	m.t().assertions = []assertion{{Kind: "json", Target: "data.id", Value: "7"}} // still the isolated tab 0
	s := m.t().toSaved("x")
	m2 := newModel()
	freshTabs(m2)
	m2.t().applySaved(s)
	if len(m2.t().assertions) != 1 || m2.t().assertions[0].Target != "data.id" {
		t.Fatalf("assertions after round-trip = %+v", m2.t().assertions)
	}
}

// TestThemePresets: at least five distinct looks, each buildable, and the
// choice persists across a restart.
func TestThemePresets(t *testing.T) {
	if len(themePresets) < 5 {
		t.Fatalf("themes = %d, want at least 5", len(themePresets))
	}
	seen := map[string]bool{}
	backgrounds := map[string]int{}
	for _, p := range themePresets {
		if seen[p.name] {
			t.Fatalf("duplicate theme name %q", p.name)
		}
		seen[p.name] = true
		th := p.build()
		if th.Background.Alpha == 0 {
			t.Fatalf("theme %q has a transparent background", p.name)
		}
		key := fmt.Sprintf("%v", th.Background)
		backgrounds[key]++
	}
	for name, n := range backgrounds {
		if n > 1 {
			t.Fatalf("theme %v's background is shared by %d presets", name, n)
		}
	}

	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.setTheme(3)
	if m.theme.Background != themePresets[3].build().Background {
		t.Fatal("setTheme did not apply the preset")
	}
	if themePresets[m.themeIdx].name != themePresets[3].name {
		t.Fatalf("themeIdx = %d, want 3", m.themeIdx)
	}
	m.persist()
	m2 := newModel()
	if m2.themeIdx != 3 {
		t.Fatalf("themeIdx after restart = %d, want 3", m2.themeIdx)
	}
	if m2.theme.Background != themePresets[3].build().Background {
		t.Fatal("the restored model does not use the persisted theme")
	}
	// an out-of-range stored index clamps to the nearest valid preset
	saveStore(storeData{Theme: 99})
	m3 := newModel()
	if m3.themeIdx != len(themePresets)-1 {
		t.Fatalf("themeIdx for an out-of-range store = %d, want %d", m3.themeIdx, len(themePresets)-1)
	}
}

// TestUpdateCheck covers the version gate: semver comparison, picking the
// platform's asset out of a release, and a full check against a fake
// releases/latest endpoint (newer, equal and broken payloads).
func TestUpdateCheck(t *testing.T) {
	if !semverLess("v0.1.0", "0.2.0") || !semverLess("0.1.9", "v0.10.0") || semverLess("v0.1.1", "v0.1.1") || semverLess("v0.2.0", "v0.1.9") {
		t.Fatal("semverLess is wrong")
	}

	assets := []relAsset{
		{Name: "apitool-windows-amd64.exe", Size: 15093760, BrowserDownloadURL: "https://example.com/win.exe"},
		{Name: "apitool-linux-amd64", Size: 15908128, BrowserDownloadURL: "https://example.com/linux"},
		{Name: "apitool-darwin-arm64", Size: 0, BrowserDownloadURL: "https://example.com/mac"},
	}
	a, ok := pickAsset(assets, "windows", "amd64")
	if !ok || a.Name != "apitool-windows-amd64.exe" {
		t.Fatalf("windows asset = %+v ok=%v", a, ok)
	}
	if a2, ok := pickAsset(assets, "darwin", "arm64"); ok || a2.Name != "" {
		t.Fatal("the zero-size darwin asset must be rejected")
	}
	if _, ok := pickAsset(assets, "linux", "arm64"); ok {
		t.Fatal("no linux-arm64 asset exists")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+repoSlug+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v9.9.9","html_url":"https://github.com/oyzy/api-tool/releases/tag/v9.9.9","body":"修复与改进","assets":[{"name":"apitool-windows-amd64.exe","size":123,"browser_download_url":"%s/win.exe"}]}`, srvAssetBase)
	}))
	defer srv.Close()
	srvAssetBase = srv.URL

	old := updateAPIBase
	updateAPIBase = srv.URL
	defer func() { updateAPIBase = old }()

	info, err := fetchUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !info.available || info.version != "v9.9.9" || info.assetName != "apitool-windows-amd64.exe" || info.notes != "修复与改进" {
		t.Fatalf("update info = %+v", info)
	}
	if info.upToDate {
		t.Fatal("v9.9.9 must not read as up to date for v0.1.0")
	}

	// the same version reads as up to date
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v`+appVersion+`"}`)
	}))
	defer srv2.Close()
	updateAPIBase = srv2.URL
	info, err = fetchUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.available || !info.upToDate {
		t.Fatalf("same version = %+v, want up to date", info)
	}

	// a broken endpoint reports the error, not a phantom update
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	defer srv3.Close()
	updateAPIBase = srv3.URL
	if _, err = fetchUpdate(context.Background()); err == nil {
		t.Fatal("garbage payload must error")
	}
}

// srvAssetBase lets the fake release point its download URL back at the test
// server.
var srvAssetBase string

// TestStoreAtomicAndBackup: a save leaves the previous store as .bak, and a
// store file that fails to parse falls back to that backup instead of
// presenting an empty world.
func TestStoreAtomicAndBackup(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	_ = os.Remove(dataPath + ".bak")

	m := newModel()
	m.collections = nil
	m.collections = append(m.collections, savedRequest{Name: "第一版"})
	m.persist()
	if b, err := os.ReadFile(dataPath); err != nil || !strings.Contains(string(b), "第一版") {
		t.Fatalf("first save missing: %v", err)
	}

	// the next save moves the first version to .bak
	m.collections[0].Name = "第二版"
	m.persist()
	bak, err := os.ReadFile(dataPath + ".bak")
	if err != nil || !strings.Contains(string(bak), "第一版") {
		t.Fatalf(".bak = %v, want the previous version", err)
	}
	live, _ := os.ReadFile(dataPath)
	if !strings.Contains(string(live), "第二版") {
		t.Fatal("live store was not updated")
	}

	// a torn write falls back to the .bak on load: the version before the
	// latest save, never an empty world
	_ = os.WriteFile(dataPath, []byte(`{"collections": [ {"Name"`), 0o644) // truncated
	m2 := newModel()
	if len(m2.collections) != 1 || m2.collections[0].Name != "第一版" {
		t.Fatalf("restored from torn write = %+v, want 第一版 from .bak", m2.collections)
	}
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	_ = os.Remove(dataPath + ".bak")
}

// TestDataDirSwitch: 更改数据目录 checkpoints the old store, points the
// location file at the new directory, and reloads the model from whatever is
// there; a fresh start reads the pointer file back.
func TestDataDirSwitch(t *testing.T) {
	dirA := t.TempDir() + string(os.PathSeparator) + "apitoolA"
	dirB := t.TempDir() + string(os.PathSeparator) + "apitoolB"
	seed := storeData{Collections: []savedRequest{{Name: "甲", Method: "GET", URL: "https://a.example"}}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "data.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	m := newModel()
	freshTabs(m)
	oldDataPath := dataPath
	defer func() { dataPath = oldDataPath }()

	if err := m.switchDataDir(dirA); err != nil {
		t.Fatal(err)
	}
	if dataPath != filepath.Join(dirA, "data.json") {
		t.Fatalf("dataPath = %q, want dirA/data.json", dataPath)
	}
	if len(m.collections) != 1 || m.collections[0].Name != "甲" {
		t.Fatalf("collections after switch = %+v", m.collections)
	}
	if loadLocationOverride() != filepath.Join(dirA, "data.json") {
		t.Fatalf("pointer = %q, want dirA/data.json", loadLocationOverride())
	}

	// switching to a fresh directory reloads an empty workspace; the old
	// store's file is untouched
	if err := m.switchDataDir(dirB); err != nil {
		t.Fatal(err)
	}
	if len(m.collections) != 0 || len(m.tabs) != 1 {
		t.Fatalf("empty store: collections=%d tabs=%d", len(m.collections), len(m.tabs))
	}
	if loadLocationOverride() != filepath.Join(dirB, "data.json") {
		t.Fatalf("pointer = %q, want dirB/data.json", loadLocationOverride())
	}
	if b2, err := os.ReadFile(filepath.Join(dirA, "data.json")); err != nil || !strings.Contains(string(b2), "甲") {
		t.Fatalf("dirA store = %q (%v), want it untouched", b2, err)
	}

	// a fresh start honours the pointer file
	saved := dataPath
	dataPath = ""
	m2 := newModel()
	defer func() { dataPath = saved }()
	if dataPath != filepath.Join(dirB, "data.json") {
		t.Fatalf("dataPath from pointer = %q, want dirB/data.json", dataPath)
	}
	if len(m2.tabs) == 0 {
		t.Fatal("the restarted model has no tabs")
	}

	// switching to a store's own directory is a no-op
	if err := m2.switchDataDir(dataPath); err != nil {
		t.Fatal(err)
	}
	if loadLocationOverride() != filepath.Join(dirB, "data.json") {
		t.Fatalf("pointer changed on a no-op switch: %q", loadLocationOverride())
	}
}

// TestUpdateApply drives the self-swap: the old exe steps aside as .old, the
// downloaded bytes take its place, and a second apply puts the old content
// back (the rollback path).
func TestUpdateApply(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "apitool.exe")
	if err := os.WriteFile(exe, []byte("OLD-BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdateFile(exe, []byte("NEW-BYTES")); err != nil {
		t.Fatal(err)
	}
	live, err := os.ReadFile(exe)
	if err != nil || string(live) != "NEW-BYTES" {
		t.Fatalf("live exe = %q (%v), want NEW-BYTES", live, err)
	}
	old, err := os.ReadFile(exe + ".old")
	if err != nil || string(old) != "OLD-BYTES" {
		t.Fatalf(".old = %q (%v), want OLD-BYTES", old, err)
	}

	// a second apply steps the previous version into .old again
	if err := applyUpdateFile(exe, []byte("NEWEST")); err != nil {
		t.Fatal(err)
	}
	live, _ = os.ReadFile(exe)
	if string(live) != "NEWEST" {
		t.Fatalf("live exe = %q, want NEWEST", live)
	}
	old, _ = os.ReadFile(exe + ".old")
	if string(old) != "NEW-BYTES" {
		t.Fatalf(".old = %q, want NEW-BYTES", old)
	}
	_ = os.Remove(exe)
	_ = os.Remove(exe + ".old")
}

// TestVarNameFor: a leaf becomes the variable's name; an array index joins
// its parent with an underscore.
func TestVarNameFor(t *testing.T) {
	if got := varNameFor("data.token", "token"); got != "token" {
		t.Fatalf("name = %q, want token", got)
	}
	if got := varNameFor("tags[1]", "1"); got != "tags_1" {
		t.Fatalf("name = %q, want tags_1", got)
	}
	if got := varNameFor("data.0.id", "id"); got != "id" {
		t.Fatalf("name = %q, want id", got)
	}
	if got := varNameFor("data", ""); got != "data" {
		t.Fatalf("name = %q, want data", got)
	}
}

// TestSaveEnvVar: writing an unknown name appends, a known name updates in
// place, and an empty name is rejected.
func TestSaveEnvVar(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
	m := newModel()
	m.envSets = []envSet{{Name: "测试", Vars: []kv{{Key: "old", Value: "1"}}}}
	m.activeEnvName = "测试"

	m.saveEnvVar("token", "abc")
	m.saveEnvVar("token", "def") // update in place
	m.saveEnvVar("host", "example.com")
	if m.envSets[0].Vars[0].Key != "old" || m.envSets[0].Vars[0].Value != "1" {
		t.Fatalf("vars[0] = %+v, want old=1 untouched", m.envSets[0].Vars[0])
	}
	if len(m.envSets[0].Vars) != 3 {
		t.Fatalf("vars = %+v, want old + token + host", m.envSets[0].Vars)
	}
	if v := m.envSets[0].Vars[1]; v.Key != "token" || v.Value != "def" {
		t.Fatalf("token var = %+v, want updated to def", v)
	}
	m.saveEnvVar("  ", "x")
	if len(m.envSets[0].Vars) != 3 {
		t.Fatalf("an empty name must be rejected")
	}
	// persisted
	m2 := newModel()
	if len(m2.envSets[0].Vars) != 3 || m2.envSets[0].Vars[1].Value != "def" {
		t.Fatalf("vars after restart = %+v", m2.envSets[0].Vars)
	}
}

// TestUpdateChecksum: the check carries the platform asset's expected hash
// from the release's checksums.txt, and the download verifies it.
func TestUpdateChecksum(t *testing.T) {
	old := updateAPIBase
	defer func() { updateAPIBase = old }()

	payload := []byte("new-exe-bytes")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + repoSlug + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[`+
				`{"name":"apitool-windows-amd64.exe","size":%d,"browser_download_url":"%s/bin"},`+
				`{"name":"checksums.txt","size":99,"browser_download_url":"%s/sums"}]}`, len(payload), srv.URL, srv.URL)
		case "/bin":
			w.Write(payload)
		case "/sums":
			fmt.Fprintf(w, "%s  apitool-windows-amd64.exe\n%s  other-file", hash, strings.Repeat("0", 64))
		}
	}))
	defer srv.Close()
	updateAPIBase = srv.URL

	info, err := fetchUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.sha256 != hash {
		t.Fatalf("sha256 = %q, want %q", info.sha256, hash)
	}

	// the honest download passes; a tampered one is rejected
	got, err := downloadUpdate(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-exe-bytes" {
		t.Fatalf("downloaded = %q", got)
	}
	bad := info
	bad.sha256 = strings.Repeat("0", 64)
	if _, err := downloadUpdate(context.Background(), bad); err == nil {
		t.Fatal("a checksum mismatch must abort the download")
	}
}

// TestApplyResultCaps: the JSON tree is skipped for oversized bodies and the
// text view is bounded, with the full body still available for 复制响应体.
func TestApplyResultCaps(t *testing.T) {
	m := newModel()
	freshTabs(m)

	manyLines := strings.Repeat("line\n", maxTextLines+500)
	m.applyResult(0, httpResult{Status: 200, Body: manyLines}, nil)
	if len(m.t().respLines) != maxTextLines+1 {
		t.Fatalf("respLines = %d, want capped %d + marker", len(m.t().respLines), maxTextLines)
	}
	if !strings.Contains(m.t().respLines[maxTextLines], "仅显示前") {
		t.Fatalf("last line = %q, want the truncation marker", m.t().respLines[maxTextLines])
	}

	big := `{"pad":"` + strings.Repeat("x", maxTreeBytes) + `"}`
	m.applyResult(0, httpResult{Status: 200, Body: big}, nil)
	if m.t().jsonRoot != nil {
		t.Fatal("an oversized body must skip the JSON tree")
	}
}

// TestCloseTabGuard: a dirty tab opens the confirmation and stays until
// 直接关闭 or 保存并关闭; a clean tab closes at once.
func TestCloseTabGuard(t *testing.T) {
	m := newModel()
	m.collections = nil
	freshTabs(m)

	// a clean fresh tab closes without asking — and as the last tab, it is
	// replaced by another fresh one rather than leaving nothing
	m.requestCloseTab(0)
	if len(m.tabs) != 1 || m.t().url != "" {
		t.Fatalf("a clean tab must close into a fresh blank one")
	}
	m.newTab()

	// a tab editing an entry with local edits: the guard holds it
	m.collections = append(m.collections, savedRequest{Name: "接口", Method: "GET", URL: "https://a.example"})
	m.rebuildCollRows()
	m.tabs[0].editing = 0
	m.tabs[0].applySaved(m.collections[0])
	m.tabs[0].url = "https://edited.example" // dirty now
	m.requestCloseTab(0)
	if !m.closeTabOpen || len(m.tabs) != 2 {
		t.Fatalf("dirty tab: closeTabOpen = %v tabs = %d, want the dialog and both tabs kept", m.closeTabOpen, len(m.tabs))
	}

	// 保存并关闭 writes the tab into the entry, then closes it
	id := m.pendingClose
	m.saveAndCloseTab(id)
	if len(m.tabs) != 1 {
		t.Fatalf("save-and-close left both tabs open")
	}
	if m.collections[0].URL != "https://edited.example" {
		t.Fatalf("entry after save = %q, want the tab's URL", m.collections[0].URL)
	}
	if m.closeTabOpen {
		t.Fatal("the dialog must be closed after saving")
	}

	// 直接关闭 keeps the entry untouched: the tab dies with its local edits,
	// and as the last tab it is replaced by a fresh blank one
	m.tabs[0].body = "local edits"
	m.requestCloseTab(0)
	if !m.closeTabOpen {
		t.Fatal("the dirty tab must open the confirmation")
	}
	m.closeTabByID(m.pendingClose)
	if len(m.tabs) != 1 || m.tabs[0].url != "" {
		t.Fatalf("tabs after 直接关闭 = %d, want one fresh blank", len(m.tabs))
	}
	if m.collections[0].URL != "https://edited.example" {
		t.Fatalf("entry after 直接关闭 = %q, want untouched", m.collections[0].URL)
	}
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak")
}

// TestHistorySearch: rebuildHistRows honours the query, keeping entries whose
// URL, name or method matches, with the day headings following the survivors.
func TestHistorySearch(t *testing.T) {
	m := newModel()
	m.history = []savedRequest{
		{Name: "GET http://a.example/first", Method: "GET", URL: "http://a.example/first", Time: time.Now().Format("2006-01-02 15:04:05")},
		{Name: "POST http://b.example/second", Method: "POST", URL: "http://b.example/second", Time: time.Now().Format("2006-01-02 15:04:06")},
	}
	m.rebuildHistRows()
	if len(m.histRows) != 3 {
		t.Fatalf("rows = %d, want header + 2 entries", len(m.histRows))
	}

	m.histQuery = "second"
	m.rebuildHistRows()
	if len(m.histRows) != 2 {
		t.Fatalf("filtered rows = %d, want header + 1 entry", len(m.histRows))
	}
	if m.histRows[1].idx != 1 {
		t.Fatalf("kept idx = %d, want the second entry", m.histRows[1].idx)
	}

	m.histQuery = "get http"
	m.rebuildHistRows() // case-insensitive, matches the method too
	if len(m.histRows) != 2 || m.histRows[1].idx != 0 {
		t.Fatalf("method search: rows = %+v", m.histRows)
	}

	m.histQuery = "nomatch"
	m.rebuildHistRows()
	if len(m.histRows) != 0 {
		t.Fatalf("no-match rows = %d, want 0", len(m.histRows))
	}
}

func TestMultipleEnvs(t *testing.T) {
	_ = os.Remove(dataPath)
	_ = os.Remove(dataPath + ".bak") // a fresh store: earlier tests may have persisted sets
	m := newModel()
	if len(m.envSets) != 1 {
		t.Fatalf("expected 1 default env set, got %d", len(m.envSets))
	}
	m.envSets = append(m.envSets, envSet{
		Name: "开发",
		Vars: []kv{{Key: "host", Value: "dev.example.com"}},
	})
	m.activeEnvName = "开发"

	envs := m.activeEnvs()
	if len(envs) != 1 || envs[0].Key != "host" || envs[0].Value != "dev.example.com" {
		t.Fatalf("activeEnvs = %v, want [host=dev.example.com]", envs)
	}

	m.activeEnvName = "默认"
	if got := m.activeEnvs(); len(got) != 0 {
		t.Fatalf("default envs = %v, want empty", got)
	}

	m.deleteEnvSet(1)
	if len(m.envSets) != 1 {
		t.Fatalf("after delete: envSets = %d, want 1", len(m.envSets))
	}
	if m.activeEnvName != "默认" {
		t.Fatalf("activeEnvName = %q, want 默认", m.activeEnvName)
	}
}
