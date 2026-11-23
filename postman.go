package main

// postman.go imports Postman's own export formats so users arrive with their
// existing work: collection exports (v2.x, folders nest via item[]) and
// environment exports (a name plus a values list). Both are detected from the
// JSON shape, so one menu item serves either.
import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// pmFile is the union of both export shapes: a collection has info+item, an
// environment has name+values.
type pmFile struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Name     string   `json:"name"` // environment exports
	Auth     *pmAuth  `json:"auth"` // collection-level auth, inherited by requests
	Item     []pmItem `json:"item"`
	Variable []pmKV   `json:"variable"`
	Values   []pmKV   `json:"values"`
}

// pmAuth is Postman's auth block; params are key/value pairs named after the
// auth type (bearer → token, basic → username/password, apikey → key/value/in).
type pmAuth struct {
	Type   string `json:"type"`
	Bearer []pmKV `json:"bearer"`
	Basic  []pmKV `json:"basic"`
	APIKey []pmKV `json:"apikey"`
}

// valueOf picks one param's value by key.
func (a *pmAuth) valueOf(key string) string {
	for _, list := range [][]pmKV{a.Bearer, a.Basic, a.APIKey} {
		for _, kv := range list {
			if kv.Key == key {
				return kv.Value
			}
		}
	}
	return ""
}

type pmItem struct {
	Name    string     `json:"name"`
	Item    []pmItem   `json:"item"`   // a folder's children
	Request *pmRequest `json:"request"` // nil for folders
}

type pmRequest struct {
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"` // a string or a {raw, query} object
	Header []pmKV          `json:"header"`
	Body   *pmBody         `json:"body"`
	Auth   *pmAuth         `json:"auth"` // request-level auth overrides the collection's
}

type pmBody struct {
	Mode     string `json:"mode"`
	Raw      string `json:"raw"`
	Language string `json:"language"`
	Options  struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
	Formdata   []pmKV `json:"formdata"`
	Urlencoded []pmKV `json:"urlencoded"`
}

type pmKV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Disabled bool   `json:"disabled"`
}

// pmResult is one parsed export: everything the store needs to absorb it.
type pmResult struct {
	collections []savedRequest
	groups      []string
	envSets     []envSet
}

// parsePostmanFile detects the export kind and maps it into the store's
// shapes. Folder names cannot carry our "/" path separator, so they are
// flattened to "-".
func parsePostmanFile(data []byte, fallbackName string) (pmResult, error) {
	var f pmFile
	if err := json.Unmarshal(data, &f); err != nil {
		return pmResult{}, fmt.Errorf("不是有效的 JSON: %w", err)
	}
	var res pmResult
	switch {
	case len(f.Item) > 0 || f.Info.Name != "":
		// a collection export
		name := f.Info.Name
		if name == "" {
			name = fallbackName
		}
		walkPMItemsAuth(f.Item, "", f.Auth, &res)
		if len(f.Variable) > 0 {
			res.envSets = append(res.envSets, envSet{Name: name, Vars: pmVarsToKV(f.Variable)})
		}
	case len(f.Values) > 0:
		// an environment export
		envName := f.Name
		if envName == "" {
			envName = fallbackName
		}
		res.envSets = append(res.envSets, envSet{Name: envName, Vars: pmVarsToKV(f.Values)})
	default:
		return pmResult{}, fmt.Errorf("无法识别的 Postman 文件（既不是集合也不是环境）")
	}
	return res, nil
}

// walkPMItems maps Postman items: requests become collection entries, folders
// become nested group paths.
func walkPMItems(items []pmItem, group string, res *pmResult) {
	walkPMItemsAuth(items, group, nil, res)
}

// walkPMItemsAuth carries the collection-level auth down so requests without
// their own auth inherit it (Postman's rule; request-level noauth disables it).
func walkPMItemsAuth(items []pmItem, group string, inherited *pmAuth, res *pmResult) {
	for _, it := range items {
		if it.Request == nil { // a folder
			folder := sanitizePMGroup(it.Name)
			path := folder
			if group != "" {
				path = group + "/" + folder
			}
			if !containsString(res.groups, path) {
				res.groups = append(res.groups, path)
			}
			walkPMItemsAuth(it.Item, path, inherited, res)
			continue
		}
		req := it.Request
		auth := inherited
		if req.Auth != nil {
			auth = req.Auth
		}
		s := savedRequest{
			Name:   it.Name,
			Group:  group,
			Method: req.Method,
			Auth:   pmAuthToConfig(auth),
		}
		if s.Method == "" {
			s.Method = "GET"
		}
		s.URL, s.Params = pmURLParts(req.URL)
		for _, h := range req.Header {
			s.Headers = append(s.Headers, kv{Key: h.Key, Value: h.Value, Disabled: h.Disabled})
		}
		if b := req.Body; b != nil {
			switch b.Mode {
			case "raw":
				s.Body = b.Raw
				s.BodyType = "text"
				lang := strings.ToLower(b.Options.Raw.Language)
				if lang == "" {
					lang = strings.ToLower(b.Language)
				}
				if strings.Contains(lang, "json") {
					s.BodyType = "json"
				}
			case "formdata":
				s.BodyType = "form-data"
				for _, f := range b.Formdata {
					if f.Type == "file" {
						continue // no local path survives an export
					}
					s.FormFields = append(s.FormFields, formField{Key: f.Key, Value: f.Value, Enabled: !f.Disabled})
				}
			case "urlencoded":
				s.BodyType = "form"
				for _, f := range b.Urlencoded {
					if f.Disabled {
						continue
					}
					s.Body += f.Key + ": " + f.Value + "\n"
				}
			}
		}
		res.collections = append(res.collections, s)
	}
}

// pmAuthToConfig maps Postman's auth block into our auth panel's config. Only
// the three kinds we support are mapped; everything else imports as 无.
func pmAuthToConfig(a *pmAuth) authConfig {
	if a == nil {
		return authConfig{}
	}
	switch strings.ToLower(a.Type) {
	case "bearer":
		return authConfig{Type: "bearer", Token: a.valueOf("token")}
	case "basic":
		return authConfig{Type: "basic", Username: a.valueOf("username"), Password: a.valueOf("password")}
	case "apikey":
		if strings.EqualFold(a.valueOf("in"), "query") {
			return authConfig{} // our apikey lives in a header only
		}
		return authConfig{Type: "apikey", Key: a.valueOf("key"), Value: a.valueOf("value")}
	}
	return authConfig{}
}

// pmURLParts splits a Postman URL (a raw string or an object) into our shape:
// a clean URL plus its query as ordered params.
func pmURLParts(raw json.RawMessage) (string, []kv) {
	if len(raw) == 0 {
		return "", nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}
	var obj struct {
		Raw   string `json:"raw"`
		Query []pmKV `json:"query"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Raw == "" {
		return strings.TrimSpace(string(raw)), nil
	}
	clean := obj.Raw
	var params []kv
	if u, err := url.Parse(obj.Raw); err == nil && u.RawQuery != "" {
		clean = u.Scheme + "://" + u.Host + u.Path
		for _, pair := range strings.Split(u.RawQuery, "&") {
			k, v, _ := strings.Cut(pair, "=")
			if k == "" {
				continue
			}
			key, kerr := url.QueryUnescape(k)
			val, verr := url.QueryUnescape(v)
			if kerr != nil || verr != nil {
				key, val = k, v
			}
			params = append(params, kv{Key: key, Value: val})
		}
	}
	return clean, params
}

// pmVarsToKV maps Postman variables/environments into our kv rows, keeping
// the disabled flag.
func pmVarsToKV(vars []pmKV) []kv {
	var out []kv
	for _, v := range vars {
		if v.Key == "" {
			continue
		}
		out = append(out, kv{Key: v.Key, Value: v.Value, Disabled: v.Disabled})
	}
	return out
}

// sanitizePMGroup keeps our group path model intact: folder names from
// Postman may carry "/".
func sanitizePMGroup(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "未命名分组"
	}
	return strings.ReplaceAll(name, "/", "-")
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
