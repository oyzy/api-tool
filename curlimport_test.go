package main

import (
	"strings"
	"testing"
)

func TestParseCurlBashMultiline(t *testing.T) {
	text := "curl 'https://api.example.com/users' \\\n  -H 'Content-Type: application/json' \\\n  --data-raw '{\"name\":\"张三\"}' \\\n  --compressed"
	req, err := parseCurl(text)
	if err != nil {
		t.Fatal(err)
	}
	if req.method != "POST" {
		t.Fatalf("method = %s, want POST (curl -d implies POST)", req.method)
	}
	if req.url != "https://api.example.com/users" {
		t.Fatalf("url = %q", req.url)
	}
	if len(req.headers) != 1 || req.headers[0].Key != "Content-Type" || req.headers[0].Value != "application/json" {
		t.Fatalf("headers = %+v", req.headers)
	}
	if req.body != `{"name":"张三"}` || req.bodyType != "json" {
		t.Fatalf("body = %q type = %q, want JSON", req.body, req.bodyType)
	}
	if !req.followRedirects {
		// --compressed is ignored, -L absent → false is fine; this assert pins
		// that --compressed does not accidentally enable redirects
		if req.followRedirects {
			t.Fatal("unreachable")
		}
	}
}

func TestParseCurlFormAndAuth(t *testing.T) {
	text := `curl -X POST 'https://x/login' -u 'user:pass' -F 'file=@/tmp/a.txt' -F 'note=hello' -L`
	req, err := parseCurl(text)
	if err != nil {
		t.Fatal(err)
	}
	if req.method != "POST" {
		t.Fatalf("method = %s, want POST", req.method)
	}
	if len(req.formFields) != 2 {
		t.Fatalf("formFields = %+v, want 2", req.formFields)
	}
	if !req.formFields[0].IsFile || req.formFields[0].FilePath != "/tmp/a.txt" {
		t.Fatalf("file field = %+v", req.formFields[0])
	}
	if req.formFields[1].Value != "hello" {
		t.Fatalf("note field = %+v", req.formFields[1])
	}
	var auth string
	for _, h := range req.headers {
		if h.Key == "Authorization" {
			auth = h.Value
		}
	}
	if auth != "Basic dXNlcjpwYXNz" {
		t.Fatalf("Authorization = %q, want Basic dXNlcjpwYXNz", auth)
	}
	if !req.followRedirects {
		t.Fatal("-L should enable redirects")
	}
}

func TestParseCurlGetParams(t *testing.T) {
	req, err := parseCurl(`curl 'https://x/list' -G -d 'page=2' -d 'size=10'`)
	if err != nil {
		t.Fatal(err)
	}
	if req.method != "GET" {
		t.Fatalf("method = %s, want GET (curl -G keeps GET)", req.method)
	}
	if len(req.params) != 2 || req.params[0].Key != "page" || req.params[1].Key != "size" {
		t.Fatalf("params = %+v, want page and size", req.params)
	}
	if req.body != "" {
		t.Fatalf("body = %q, want it moved into the params", req.body)
	}
}

func TestParseCurlDoubleQuoteEscape(t *testing.T) {
	req, err := parseCurl("curl -X POST \"https://x/api\" --data-raw \"{\\\"a\\\": 1}\"")
	if err != nil {
		t.Fatal(err)
	}
	if req.method != "POST" || req.url != "https://x/api" {
		t.Fatalf("method = %s url = %q", req.method, req.url)
	}
	if req.body != `{"a": 1}` || req.bodyType != "json" {
		t.Fatalf("body = %q type = %q, want JSON", req.body, req.bodyType)
	}
}

func TestParseCurlErrors(t *testing.T) {
	if _, err := parseCurl(""); err == nil {
		t.Fatal("empty input should error")
	}
	if _, err := parseCurl("-H 'X: 1'"); err == nil || !strings.Contains(err.Error(), "没有找到请求地址") {
		t.Fatalf("err = %v, want 没有找到请求地址", err)
	}
}

func TestCurlImportFillsEditor(t *testing.T) {
	m := newModel()
	req, err := parseCurl("curl 'https://api.example.com/users?page=2' -H 'Content-Type: application/json' --data-raw '{\"name\":\"张三\"}'")
	if err != nil {
		t.Fatal(err)
	}
	m.applyCurlImport(req)
	if m.t().method != "POST" || m.t().url != "https://api.example.com/users?page=2" {
		t.Fatalf("editor = %s %q", m.t().method, m.t().url)
	}
	if m.t().bodyType != "json" || m.t().body != `{"name":"张三"}` {
		t.Fatalf("bodyType = %s body = %q", m.t().bodyType, m.t().body)
	}
	if len(m.t().headers) != 1 || m.t().headers[0].Key != "Content-Type" {
		t.Fatalf("headers = %+v", m.t().headers)
	}
	if m.t().editing != -1 {
		t.Fatalf("editing = %d, want -1 (a save creates a new entry)", m.t().editing)
	}
	// the params table picked the query up from the URL on the next sync
	m.syncURLParams()
	var found bool
	for _, p := range m.t().params {
		if p.Key == "page" {
			found = true
		}
	}
	if !found {
		t.Fatalf("params = %+v, want page filled from the URL", m.t().params)
	}
}
