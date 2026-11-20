package main

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// assertion is one declarative check a request carries, evaluated against its
// response — Postman's Tests tab, without the scripting. It is persisted with
// the request and checked on every send and in collection runs.
//
//	status:   Target is the expected status code ("200")
//	contains: Target is a text the body must contain
//	json:     Target is a dotted path (data.items[0].id), Value what it should equal
type assertion struct {
	Kind   string
	Target string
	Value  string
}

// assertResult is one assertion's outcome after a response.
type assertResult struct {
	desc   string // what was checked, for the list
	pass   bool
	detail string // empty when passed; the mismatch otherwise
}

var assertKinds = []string{"status", "contains", "json"}

func assertKindLabel(k string) string {
	switch k {
	case "status":
		return "状态码"
	case "contains":
		return "包含"
	case "json":
		return "JSON 路径"
	}
	return k
}

// runAssertions checks a request's assertions against a landed response.
// A row whose check target is blank is skipped: the user may leave one
// half-filled while editing without failing every run.
func runAssertions(as []assertion, envs []kv, resp *httpResult) []assertResult {
	var out []assertResult
	var root any
	parsed := false
	parseFailed := false
	for _, a := range as {
		switch a.Kind {
		case "status":
			want := strings.TrimSpace(substituteVars(a.Target, envs))
			if want == "" {
				continue
			}
			out = append(out, assertResult{
				desc:   "状态码 = " + want,
				pass:   strconv.Itoa(resp.Status) == want,
				detail: "实际 " + strconv.Itoa(resp.Status),
			})
		case "contains":
			needle := substituteVars(a.Target, envs)
			if strings.TrimSpace(needle) == "" {
				continue
			}
			out = append(out, assertResult{
				desc:   "响应包含 " + needle,
				pass:   strings.Contains(resp.Body, needle),
				detail: "未找到 " + needle,
			})
		case "json":
			path := strings.TrimSpace(substituteVars(a.Target, envs))
			if path == "" {
				continue
			}
			want := substituteVars(a.Value, envs)
			res := assertResult{desc: path + " = " + want}
			if !parsed {
				dec := json.NewDecoder(strings.NewReader(resp.Body))
				dec.UseNumber()
				var extra any
				// a whole document, not a leading scalar followed by text:
				// "404 page not found" decodes as the number 404
				if err := dec.Decode(&root); err != nil || dec.Decode(&extra) != io.EOF {
					parseFailed = true
				}
				parsed = true
			}
			if parseFailed {
				res.detail = "响应不是 JSON"
			} else if got, ok := jsonPathValue(root, path); !ok {
				res.detail = "路径不存在"
			} else {
				res.pass = got == want
				if !res.pass {
					res.detail = "实际 " + got
				}
			}
			out = append(out, res)
		}
	}
	return out
}

// jsonPathValue extracts one value from parsed JSON by a dotted path whose
// steps may index an array: data.items[0].id. ok=false when the path leaves
// the document.
func jsonPathValue(v any, path string) (string, bool) {
	for _, seg := range strings.Split(path, ".") {
		name := seg
		idx := -1
		if i := strings.IndexByte(seg, '['); i >= 0 && strings.HasSuffix(seg, "]") {
			n, err := strconv.Atoi(strings.TrimSuffix(seg[i+1:], "]"))
			if err != nil {
				return "", false
			}
			name = seg[:i]
			idx = n
		}
		if name != "" {
			m, ok := v.(map[string]any)
			if !ok {
				return "", false
			}
			v, ok = m[name]
			if !ok {
				return "", false
			}
		}
		if idx >= 0 {
			arr, ok := v.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return "", false
			}
			v = arr[idx]
		}
	}
	return jsonScalarText(v), true
}

// jsonScalarText renders a scalar the way the editor compares it: strings
// unquoted, numbers and bools as their text.
func jsonScalarText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	}
	return ""
}
