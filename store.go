package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// envSet is one named set of environment variables.
type envSet struct {
	Name string
	Vars []kv
}

// authConfig is a request's authorization panel: the header it stands for is
// synthesized at send time (authHeaders), so the user's own header rows stay
// clean. Type "" is 无 — legacy files load as none.
type authConfig struct {
	Type     string // "" | "basic" | "bearer" | "apikey"
	Username string
	Password string
	Token    string
	Key      string // api key: the header name
	Value    string // api key: the value
}

// savedRequest is one saved collection item or one history entry. Group names
// the folder it sits in; empty is the implicit 未分组.
type savedRequest struct {
	ID         int64 // unique id for in-place updates (e.g. the status lands after the entry is created)
	Name       string
	Group      string
	Time       string
	Status     int   // the response's HTTP status; 0 while the request is in flight
	Ms         int64 // how long the request took, in milliseconds; 0 while in flight
	Method     string
	URL        string
	Params     []kv
	Headers    []kv
	BodyType   string
	Body       string
	GqlQuery   string // the GraphQL editor's query (bodyType graphql)
	GqlVars    string // the GraphQL editor's variables, as JSON text
	FormFields []formField
	Auth       authConfig
	Assertions []assertion
}

// savedTab is one open request tab, persisted so the workspace survives a
// restart: the editor's request and which entry it edits. Responses are not
// kept, and the sync baselines are rebuilt on load.
type savedTab struct {
	Method     string
	URL        string
	ReqTab     string
	Params     []kv
	Headers    []kv
	BodyType   string
	Body       string
	GqlQuery   string
	GqlVars    string
	FormFields []formField
	Auth       authConfig
	Assertions []assertion
	Editing    int
}

// snapshot copies the tab's request into a persisted form.
func (t *tabState) snapshot() savedTab {
	return savedTab{
		Method:     t.method,
		URL:        t.url,
		ReqTab:     t.reqTab,
		Params:     append([]kv(nil), t.params...),
		Headers:    append([]kv(nil), t.headers...),
		BodyType:   t.bodyType,
		Body:       t.body,
		GqlQuery:   t.gqlQuery,
		GqlVars:    t.gqlVars,
		FormFields: append([]formField(nil), t.formFields...),
		Auth:       t.auth,
		Assertions: append([]assertion(nil), t.assertions...),
		Editing:    t.editing,
	}
}

// restore loads a persisted tab: the request fields, the active sub-tab, and
// baselines that keep the two-way sync from rewriting anything on the first
// paint. Editing must already be clamped by the caller.
func (t *tabState) restore(s savedTab) {
	t.applySaved(savedRequest{
		Method:     s.Method,
		URL:        s.URL,
		Params:     s.Params,
		Headers:    s.Headers,
		BodyType:   s.BodyType,
		Body:       s.Body,
		GqlQuery:   s.GqlQuery,
		GqlVars:    s.GqlVars,
		FormFields: s.FormFields,
		Auth:       s.Auth,
		Assertions: s.Assertions,
	})
	t.reqTab = s.ReqTab
	t.editing = s.Editing
	t.urlSeen = t.url
	t.paramsSeen = t.paramsFingerprint()
	t.bodySeen = ""
	t.checkBodyJSON() // the chip's ✓/✗ must match the body it came back with
}

// storeData is everything persisted to disk.
type storeData struct {
	Collections []savedRequest `json:"collections"`
	History     []savedRequest `json:"history"`
	Groups      []string       `json:"groups"` // folder names, in order; may hold empty folders
	Envs        []kv           `json:"envs"`   // legacy single set
	EnvSets     []envSet       `json:"envSets"`
	ActiveEnv   string         `json:"activeEnv"`
	Tabs        []savedTab     `json:"tabs"`       // the open request tabs
	ActiveTab   int            `json:"activeTab"`  // which one was on screen
	Theme       int            `json:"theme"`      // the theme preset's index
	TimeoutSec  int            `json:"timeoutSec"` // request timeout in seconds; 0 = 30
}

// dataPath overrides where the data file lives; empty uses the location the
// data.location pointer names, else the default storePath(). It lets tests
// run against a throwaway file instead of the user's real store, and carries
// the directory the user picked in 设置.
var dataPath string

// locationPath overrides where the data.location pointer file lives; empty
// uses the default store's directory. Tests point it at a throwaway file.
var locationPath string

// storeFile returns the data file location, honouring dataPath.
func storeFile() string {
	if dataPath != "" {
		return dataPath
	}
	return storePath()
}

// storeLocationFile is the pointer file beside the default store: it holds
// the data directory the user switched to, so the choice survives restarts.
func storeLocationFile() string {
	if locationPath != "" {
		return locationPath
	}
	return filepath.Join(filepath.Dir(storePath()), "data.location")
}

// loadLocationOverride reads the pointer file; empty means the default
// location.
func loadLocationOverride() string {
	b, err := os.ReadFile(storeLocationFile())
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if p == "" || p == storePath() { // the default needs no pointer
		return ""
	}
	return p
}

// saveLocationOverride records the data directory (empty string removes the
// pointer, restoring the default).
func saveLocationOverride(dir string) {
	p := storeLocationFile()
	if dir == "" {
		_ = os.Remove(p)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, []byte(dir), 0o644)
}

// storePath returns the on-disk location of the data file.
func storePath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "apitool", "data.json")
}

// loadStore reads the data file, returning an empty store when it is missing
// or unreadable. A file that fails to parse (a torn write, say) falls back to
// the .bak the previous save left behind, so one bad write cannot lose data.
func loadStore() storeData {
	data := storeData{}
	b, err := os.ReadFile(storeFile())
	if err != nil || json.Unmarshal(b, &data) != nil {
		data = storeData{}
		if b, err = os.ReadFile(storeFile() + ".bak"); err != nil || json.Unmarshal(b, &data) != nil {
			data = storeData{}
		}
	}
	// Migrate a legacy single env list into the multi-env format.
	if len(data.EnvSets) == 0 && len(data.Envs) > 0 {
		data.EnvSets = []envSet{{Name: "默认", Vars: data.Envs}}
		data.ActiveEnv = "默认"
	}
	if len(data.EnvSets) == 0 {
		data.EnvSets = []envSet{{Name: "默认", Vars: []kv{}}}
	}
	if data.ActiveEnv == "" {
		data.ActiveEnv = data.EnvSets[0].Name
	}
	// Sanitize the folders: drop empty and duplicate names, keep the order.
	var groups []string
	seen := map[string]bool{}
	for _, g := range data.Groups {
		if g != "" && !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	data.Groups = groups
	return data
}

// saveStore writes the data file, creating its directory when needed. The
// write is atomic (temp file + rename) and the previous version is kept as
// .bak, so a crash or a torn write never takes the store down with it.
func saveStore(data storeData) {
	path := storeFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Remove(path + ".bak")
	_ = os.Rename(path, path+".bak") // the previous store becomes the backup
	_ = os.Rename(tmp, path)         // rename within a volume is atomic
}

// toSaved builds a savedRequest from the tab's request.
func (t *tabState) toSaved(name string) savedRequest {
	return savedRequest{
		Name:       name,
		Method:     t.method,
		URL:        t.url,
		Params:     append([]kv(nil), t.params...),
		Headers:    append([]kv(nil), t.headers...),
		BodyType:   t.bodyType,
		Body:       t.body,
		GqlQuery:   t.gqlQuery,
		GqlVars:    t.gqlVars,
		FormFields: append([]formField(nil), t.formFields...),
		Auth:       t.auth,
		Assertions: append([]assertion(nil), t.assertions...),
	}
}

// applySaved loads a saved request into the tab's fields.
func (t *tabState) applySaved(s savedRequest) {
	t.method = s.Method
	t.url = s.URL
	t.params = append([]kv(nil), s.Params...)
	t.headers = append([]kv(nil), s.Headers...)
	t.bodyType = s.BodyType
	t.body = s.Body
	t.gqlQuery = s.GqlQuery
	t.gqlVars = s.GqlVars
	t.formFields = append([]formField(nil), s.FormFields...)
	t.auth = s.Auth
	t.assertions = append([]assertion(nil), s.Assertions...)
}
