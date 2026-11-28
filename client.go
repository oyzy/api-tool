package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	TimeoutSec      int // 0 means the default 30s
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

// substituteVars replaces {{name}} in s with the matching env value. The
// {{$...}} built-ins are evaluated first, so every send carries fresh test
// data. Unknown names stay as they are, so a typo is visible instead of
// silently empty.
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
				if v, ok := dynamicVar(name, envs); ok {
					b.WriteString(v)
				} else if v := envValue(envs, name); v != "" {
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

// dynamicVar evaluates the built-in {{$...}} functions: test data that must be
// fresh on every send, plus the hashing and encoding a signature scheme needs.
// Postman's names are kept where they overlap, so pasted examples keep working.
// The hashing functions take a comma-separated argument list — a bare word is
// an env var's name, a quoted span is literal, a $token is another dynamic
// value — so a scheme like sign = md5(secret + ":" + ts) reads as
// {{$md5(secret, ":", $timestamp)}}.
func dynamicVar(name string, envs []kv) (string, bool) {
	n := strings.TrimSpace(name)
	switch n {
	case "$timestamp":
		return strconv.FormatInt(time.Now().Unix(), 10), true
	case "$timestampMs":
		return strconv.FormatInt(time.Now().UnixMilli(), 10), true
	case "$uuid", "$guid":
		return newUUID(), true
	}
	if rest, ok := strings.CutPrefix(n, "$randomInt"); ok {
		lo, hi := 0, 1000
		if args := strings.TrimSuffix(strings.TrimPrefix(rest, "("), ")"); args != "" {
			a, b, ok := strings.Cut(args, ",")
			if !ok {
				if x, err := strconv.Atoi(strings.TrimSpace(args)); err == nil {
					lo, hi = 0, x
				}
			} else if x, err1 := strconv.Atoi(strings.TrimSpace(a)); err1 == nil {
				if y, err2 := strconv.Atoi(strings.TrimSpace(b)); err2 == nil {
					lo, hi = x, y
				}
			}
		}
		if hi < lo {
			lo, hi = hi, lo
		}
		return strconv.Itoa(lo + rand.IntN(hi-lo+1)), true
	}
	if rest, ok := strings.CutPrefix(n, "$randomString"); ok {
		length := 8
		if args := strings.TrimSuffix(strings.TrimPrefix(rest, "("), ")"); args != "" {
			if x, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && x > 0 && x <= 256 {
				length = x
			}
		}
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		b := make([]byte, length)
		for i := range b {
			b[i] = alphabet[rand.IntN(len(alphabet))]
		}
		return string(b), true
	}
	// the hashing/encoding functions: $name(arg, arg, ...)
	if fn, rest, ok := strings.Cut(n, "("); ok && strings.HasSuffix(rest, ")") {
		return hashFunc(fn, strings.TrimSuffix(rest, ")"), envs)
	}
	return "", false
}

// hashFunc evaluates the $md5/$sha256/$base64/... family. The parts join with
// no separator; $hmacsha256's first argument is the key, the rest the message.
func hashFunc(fn, argList string, envs []kv) (string, bool) {
	args := splitArgs(argList)
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = evalArg(a, envs)
	}
	msg := strings.Join(parts, "")
	switch fn {
	case "$md5":
		sum := md5.Sum([]byte(msg))
		return hex.EncodeToString(sum[:]), true
	case "$sha1":
		sum := sha1.Sum([]byte(msg))
		return hex.EncodeToString(sum[:]), true
	case "$sha256":
		sum := sha256.Sum256([]byte(msg))
		return hex.EncodeToString(sum[:]), true
	case "$hmacsha1", "$hmacsha256":
		if len(parts) == 0 {
			return "", true
		}
		var mac hash.Hash
		if fn == "$hmacsha1" {
			mac = hmac.New(sha1.New, []byte(parts[0]))
		} else {
			mac = hmac.New(sha256.New, []byte(parts[0]))
		}
		mac.Write([]byte(strings.Join(parts[1:], "")))
		return hex.EncodeToString(mac.Sum(nil)), true
	case "$base64":
		return base64.StdEncoding.EncodeToString([]byte(msg)), true
	case "$base64decode":
		b, err := base64.StdEncoding.DecodeString(msg)
		if err != nil {
			if b, err = base64.RawStdEncoding.DecodeString(msg); err != nil {
				if b, err = base64.URLEncoding.DecodeString(msg); err != nil {
					if b, err = base64.RawURLEncoding.DecodeString(msg); err != nil {
						return msg, true // not valid base64: hand it back unchanged
					}
				}
			}
		}
		return string(b), true
	case "$urlencode":
		return url.QueryEscape(msg), true
	case "$urldecode":
		if v, err := url.QueryUnescape(msg); err == nil {
			return v, true
		}
		return msg, true
	}
	return "", false
}

// splitArgs splits a function argument list at its top-level commas; a quoted
// span (single or double) holds commas and spaces literally.
func splitArgs(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
			b.WriteByte(c)
		case c == '\'' || c == '"':
			inQuote = c
			b.WriteByte(c)
		case c == ',':
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if t := strings.TrimSpace(b.String()); t != "" || len(out) > 0 {
		out = append(out, t)
	}
	return out
}

// evalArg resolves one function argument: a quoted span is literal, a $token
// is another dynamic value, anything else names an env var — and an unknown
// name stands as its own literal, so a typo is visible in the result.
func evalArg(tok string, envs []kv) string {
	if len(tok) >= 2 && (tok[0] == '\'' || tok[0] == '"') && tok[len(tok)-1] == tok[0] {
		return tok[1 : len(tok)-1]
	}
	if strings.HasPrefix(tok, "$") {
		if v, ok := dynamicVar(tok, envs); ok {
			return v
		}
		return tok
	}
	if v := envValue(envs, tok); v != "" {
		return v
	}
	return tok
}

// newUUID returns a random RFC 4122 version 4 UUID.
func newUUID() string {
	var b [16]byte
	_, _ = cryptorand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// composeGraphQL folds the GraphQL editor's query and variables into the JSON
// body the wire expects: {"query": ..., "variables": ...}. Variables that do
// not parse as JSON are sent empty, so a half-typed editor never blocks a send.
func composeGraphQL(query, vars string) string {
	v := strings.TrimSpace(vars)
	if v != "" && !json.Valid([]byte(v)) {
		v = ""
	}
	b, _ := json.Marshal(struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables,omitempty"`
	}{Query: query, Variables: json.RawMessage(v)})
	return string(b)
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

	timeout := 30 * time.Second
	if o.TimeoutSec > 0 {
		timeout = time.Duration(o.TimeoutSec) * time.Second
	}
	client := &http.Client{Timeout: timeout}
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
				Timeout:   timeout,
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
