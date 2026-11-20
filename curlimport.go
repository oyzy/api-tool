package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// curlRequest is what parsing a pasted cURL command yields — exactly the
// editor's fields, so applying it fills the request panel.
type curlRequest struct {
	method          string
	url             string
	params          []kv
	headers         []kv
	body            string
	bodyType        string // "json" | "form" | "text" | "" (= none)
	formFields      []formField
	followRedirects bool
}

var errCurlEmpty = errors.New("粘贴的内容里没有 cURL 命令")

// curlTokens splits a pasted cURL command into argv the way the shell would:
// '…' is literal, "…" honours \ escapes, \x and ^x escape outside quotes, and
// \ or ^ before a newline joins the lines (bash and cmd continuations).
func curlTokens(text string) []string {
	text = strings.ReplaceAll(text, "\\\r\n", " ")
	text = strings.ReplaceAll(text, "\\\n", " ")
	text = strings.ReplaceAll(text, "^\r\n", " ")
	text = strings.ReplaceAll(text, "^\n", " ")
	var tokens []string
	var cur strings.Builder
	hasToken := false
	inSingle, inDouble := false, false
	flush := func() {
		if hasToken {
			tokens = append(tokens, cur.String())
			cur.Reset()
			hasToken = false
		}
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '\\' && i+1 < len(text) {
				i++
				if n := text[i]; n == '"' || n == '\\' || n == '$' || n == '`' {
					cur.WriteByte(n)
				} else {
					cur.WriteByte('\\')
					cur.WriteByte(n)
				}
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
			hasToken = true
		case c == '"':
			inDouble = true
			hasToken = true
		case (c == '\\' || c == '^') && i+1 < len(text):
			i++
			cur.WriteByte(text[i])
			hasToken = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			hasToken = true
		}
	}
	flush()
	return tokens
}

// parseCurl parses a pasted cURL command into the editor's fields.
func parseCurl(text string) (curlRequest, error) {
	tokens := curlTokens(text)
	if len(tokens) == 0 {
		return curlRequest{}, errCurlEmpty
	}
	var req curlRequest
	req.method = "GET"
	var bodyParts []string
	getFlag := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		arg := func() (string, bool) {
			if i+1 < len(tokens) {
				i++
				return tokens[i], true
			}
			return "", false
		}
		switch tok {
		case "curl":
			// the command name itself
		case "-X", "--request":
			if v, ok := arg(); ok {
				req.method = strings.ToUpper(v)
			}
		case "--url":
			if v, ok := arg(); ok {
				req.url = v
			}
		case "-H", "--header":
			if v, ok := arg(); ok {
				if k, value, ok2 := strings.Cut(v, ":"); ok2 {
					req.headers = append(req.headers, kv{Key: strings.TrimSpace(k), Value: strings.TrimSpace(value)})
				}
			}
		case "-d", "--data", "--data-raw", "--data-ascii", "--data-binary", "--data-urlencode":
			if v, ok := arg(); ok {
				bodyParts = append(bodyParts, v)
			}
		case "-F", "--form":
			if v, ok := arg(); ok {
				if name, value, ok2 := strings.Cut(v, "="); ok2 && name != "" {
					if strings.HasPrefix(value, "@") {
						req.formFields = append(req.formFields, formField{Key: name, Value: value[1:], IsFile: true, FilePath: value[1:]})
					} else {
						req.formFields = append(req.formFields, formField{Key: name, Value: value})
					}
				}
			}
		case "-u", "--user":
			if v, ok := arg(); ok {
				req.headers = append(req.headers, kv{Key: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte(v))})
			}
		case "-A", "--user-agent":
			if v, ok := arg(); ok {
				req.headers = append(req.headers, kv{Key: "User-Agent", Value: v})
			}
		case "-e", "--referer":
			if v, ok := arg(); ok {
				req.headers = append(req.headers, kv{Key: "Referer", Value: v})
			}
		case "-b", "--cookie":
			if v, ok := arg(); ok {
				req.headers = append(req.headers, kv{Key: "Cookie", Value: v})
			}
		case "-L", "--location":
			req.followRedirects = true
		case "-I", "--head":
			req.method = "HEAD"
		case "-G", "--get":
			getFlag = true
		case "-o", "--output", "-T", "--upload-file", "-D", "--dump-header",
			"-m", "--max-time", "--connect-timeout", "--retry", "-x", "--proxy":
			// flags whose argument we ignore — eat it when it is separate
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				i++
			}
		case "-O", "-k", "--insecure", "-s", "--silent", "-S", "--show-error", "-v", "--verbose",
			"-i", "--include", "--compressed", "-n", "--no-buffer":
			// no-argument flags with no effect on the request
		default:
			if !strings.HasPrefix(tok, "-") && req.url == "" {
				req.url = tok
			}
		}
	}
	if req.url == "" {
		return req, errors.New("没有找到请求地址")
	}
	if getFlag && len(bodyParts) > 0 {
		// curl -G moves the data into the query: the params table shows it
		for _, part := range strings.Split(strings.Join(bodyParts, "&"), "&") {
			if part == "" {
				continue
			}
			k, v, _ := strings.Cut(part, "=")
			if k = queryDecode(k); k != "" {
				req.params = append(req.params, kv{Key: k, Value: queryDecode(v)})
			}
		}
		bodyParts = nil
		req.method = "GET"
	}
	if len(bodyParts) > 0 {
		req.body = strings.Join(bodyParts, "&")
		if req.method == "GET" {
			req.method = "POST" // curl -d implies POST
		}
		req.bodyType = sniffBodyType(req.body, req.headers)
	}
	return req, nil
}

// sniffBodyType picks the editor's body type: the Content-Type header first,
// then the body's own shape.
func sniffBodyType(body string, headers []kv) string {
	for _, h := range headers {
		if strings.EqualFold(h.Key, "Content-Type") {
			ct := strings.ToLower(h.Value)
			switch {
			case strings.Contains(ct, "json"):
				return "json"
			case strings.Contains(ct, "x-www-form-urlencoded"):
				return "form"
			}
		}
	}
	t := strings.TrimSpace(body)
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		if json.Valid([]byte(t)) {
			return "json"
		}
	}
	if looksFormEncoded(t) {
		return "form"
	}
	return "text"
}

// looksFormEncoded reports whether s reads like k=v&k=v pairs.
func looksFormEncoded(s string) bool {
	parts := strings.Split(s, "&")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || !strings.Contains(p, "=") {
			return false
		}
	}
	return true
}
