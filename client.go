package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxBody caps how much of a response we read, so a huge download cannot
// exhaust memory. 10 MiB is far beyond anything a manual API test needs.
const maxBody = 10 << 20

// kv is one key-value pair, used for query params, headers and env vars.
// Disabled rows stay in the table but do not join the request; the zero value
// is enabled, so entries saved before the field existed all work. Enabled is
// the checkbox's mirror of it (UI-only, never stored).
type kv struct {
	Key      string
	Value    string
	Disabled bool
	Enabled  bool `json:"-"`
}

// formField is one field of a multipart/form-data body. A field is either a
// plain value or a file (IsFile), in which case FilePath names the file.
// Disabled fields stay in the table but do not join the request.
type formField struct {
	Key      string
	Value    string
	IsFile   bool
	FilePath string
	Disabled bool
	Enabled  bool `json:"-"`
}

// outgoing is the snapshot of a request handed to the background worker. It is
// the key of the send effect: a new pointer per send cancels the previous run.
type outgoing struct {
	Method          string
	URL             string
	Params          []kv
	Headers         []kv
	BodyType        string // "none" | "json" | "text" | "form" | "form-data"
	Body            string
	FormFields      []formField
	Envs            []kv
	FollowRedirects bool
}

// httpResult is what the worker returns to the page.
type httpResult struct {
	Status     int
	StatusText string
	Elapsed    time.Duration
	Size       int
	Headers    []kv
	Body       string
	Err        string
}

var httpMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// substituteVars replaces {{name}} in s with the matching env value. Unknown
// names stay as they are, so a typo is visible instead of silently empty.
func substituteVars(s string, envs []kv) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.Index(s[i+2:], "}}")
			if end >= 0 {
				name := s[i+2 : i+2+end]
				if v := envValue(envs, name); v != "" {
					b.WriteString(v)
				} else {
					b.WriteString(s[i : i+2+end+2])
				}
				i += 2 + end + 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// authHeaders synthesizes the headers a request's auth panel stands for, with
// {{var}} substituted from the environment. They go BEFORE the user's own
// header rows, so an Authorization the user typed by hand overrides the panel
// (headers are Set in order, the last one wins).
func authHeaders(a authConfig, envs []kv) []kv {
	switch a.Type {
	case "basic":
		cred := base64.StdEncoding.EncodeToString([]byte(
			substituteVars(a.Username, envs) + ":" + substituteVars(a.Password, envs)))
		return []kv{{Key: "Authorization", Value: "Basic " + cred}}
	case "bearer":
		tok := substituteVars(a.Token, envs)
		if tok == "" {
			return nil
		}
		return []kv{{Key: "Authorization", Value: "Bearer " + tok}}
	case "apikey":
		k := substituteVars(a.Key, envs)
		if k == "" {
			return nil
		}
		return []kv{{Key: k, Value: substituteVars(a.Value, envs)}}
	}
	return nil
}

func envValue(envs []kv, name string) string {
	for _, e := range envs {
		if e.Key == name {
			return e.Value
		}
	}
	return ""
}

// buildURL merges the query params into the URL, preserving any query the URL
// already carries.
func buildURL(raw string, params []kv, envs []kv) (string, error) {
	raw = substituteVars(raw, envs)
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for _, p := range params {
		if p.Disabled {
			continue
		}
		k := substituteVars(p.Key, envs)
		v := substituteVars(p.Value, envs)
		if k != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// doRequest runs one HTTP request off the paint thread. It only reads its
// outgoing snapshot and returns a plain result; it never touches the window.
func doRequest(ctx context.Context, o *outgoing) (httpResult, error) {
	target, err := buildURL(o.URL, o.Params, o.Envs)
	if err != nil {
		return httpResult{Err: "无效的 URL: " + err.Error()}, nil
	}

	var bodyReader io.Reader
	contentType := ""
	switch o.BodyType {
	case "json", "text":
		// the body carries {{var}} too — Postman substitutes everywhere
		bodyReader = strings.NewReader(substituteVars(o.Body, o.Envs))
	case "form":
		form := url.Values{}
		for _, line := range strings.Split(substituteVars(o.Body, o.Envs), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			form.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		bodyReader = strings.NewReader(form.Encode())
	case "form-data":
		buf := &bytes.Buffer{}
		w := multipart.NewWriter(buf)
		for _, f := range o.FormFields {
			if f.Disabled {
				continue
			}
			if f.IsFile {
				data, rerr := os.ReadFile(substituteVars(f.FilePath, o.Envs))
				if rerr != nil {
					return httpResult{Err: "无法读取文件 " + f.FilePath + ": " + rerr.Error()}, nil
				}
				part, perr := w.CreateFormFile(f.Key, filepath.Base(f.FilePath))
				if perr != nil {
					return httpResult{Err: "无法构造表单: " + perr.Error()}, nil
				}
				if _, werr := part.Write(data); werr != nil {
					return httpResult{Err: "无法写入表单: " + werr.Error()}, nil
				}
			} else {
				if werr := w.WriteField(f.Key, substituteVars(f.Value, o.Envs)); werr != nil {
					return httpResult{Err: "无法写入表单: " + werr.Error()}, nil
				}
			}
		}
		if cerr := w.Close(); cerr != nil {
			return httpResult{Err: "无法结束表单: " + cerr.Error()}, nil
		}
		bodyReader = buf
		contentType = w.FormDataContentType()
	}

	req, err := http.NewRequestWithContext(ctx, o.Method, target, bodyReader)
	if err != nil {
		return httpResult{Err: "无法构造请求: " + err.Error()}, nil
	}

	for _, h := range o.Headers {
		if h.Disabled {
			continue
		}
		k := substituteVars(h.Key, o.Envs)
		if k == "" {
			continue
		}
		req.Header.Set(k, substituteVars(h.Value, o.Envs))
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	if o.BodyType == "json" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if o.BodyType == "form" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if !o.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil && retryViaSystemProxy(err, req.URL) {
		// the direct connection died before it could talk HTTP: machines behind
		// a system-wide proxy (clash & friends) need the same fallback the
		// update checker uses
		if up := systemProxyURL(); up != nil {
			viaProxy := &http.Client{
				Timeout:   30 * time.Second,
				Transport: &http.Transport{Proxy: http.ProxyURL(up)},
			}
			if !o.FollowRedirects {
				viaProxy.CheckRedirect = client.CheckRedirect
			}
			start = time.Now()
			resp, err = viaProxy.Do(req)
		}
	}
	if err != nil {
		return httpResult{Err: err.Error(), Elapsed: time.Since(start)}, nil
	}
	defer resp.Body.Close()

	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	elapsed := time.Since(start)

	res := httpResult{
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		Elapsed:    elapsed,
		Size:       len(body),
	}
	if rerr != nil {
		res.Err = "读取响应失败: " + rerr.Error()
	}

	for name, values := range resp.Header {
		for _, v := range values {
			res.Headers = append(res.Headers, kv{Key: name, Value: v})
		}
	}
	sort.Slice(res.Headers, func(i, j int) bool { return res.Headers[i].Key < res.Headers[j].Key })

	res.Body = formatBody(resp.Header.Get("Content-Type"), body)
	return res, nil
}

// formatBody turns a raw response into display text. JSON is pretty-printed;
// text-like content is shown as is; binary content is reported as such.
func formatBody(contentType string, body []byte) string {
	ct := strings.ToLower(contentType)
	isText := strings.Contains(ct, "json") ||
		strings.Contains(ct, "text") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "html") ||
		strings.Contains(ct, "x-www-form-urlencoded") ||
		ct == ""

	if !isText || isBinary(body) {
		return "[二进制响应] Content-Type: " + contentType
	}

	s := string(body)
	if strings.Contains(ct, "json") {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err == nil {
			s = pretty.String()
		}
	}
	return s
}

// isBinary reports whether body looks like non-text data.
func isBinary(body []byte) bool {
	limit := len(body)
	if limit > 4096 {
		limit = 4096
	}
	for _, b := range body[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}
