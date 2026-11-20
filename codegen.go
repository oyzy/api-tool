package main

import (
	"fmt"
	"strconv"
	"strings"
)

// codeLangs are the languages a request can be copied as.
var codeLangs = []string{"curl", "go", "javascript", "python"}

func langLabel(lang string) string {
	switch lang {
	case "curl":
		return "cURL"
	case "go":
		return "Go"
	case "javascript":
		return "JavaScript"
	case "python":
		return "Python"
	}
	return lang
}

// finalURL resolves the request URL with its query params and env vars applied.
func finalURL(o *outgoing) string {
	u, err := buildURL(o.URL, o.Params, o.Envs)
	if err != nil {
		return o.URL
	}
	return u
}

// hasBody reports whether the request carries a body of any kind.
func hasBody(o *outgoing) bool {
	switch o.BodyType {
	case "json", "text", "form":
		return o.Body != ""
	case "form-data":
		return len(o.FormFields) > 0
	}
	return false
}

// baseName returns the last path segment of a file path.
func baseName(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// generateCode returns the request as source in the given language.
func generateCode(lang string, o *outgoing, follow bool) string {
	switch lang {
	case "curl":
		return curlCode(o, follow)
	case "go":
		return goCode(o)
	case "javascript":
		return jsCode(o)
	case "python":
		return pythonCode(o)
	}
	return ""
}

func curlCode(o *outgoing, follow bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s '%s'", o.Method, finalURL(o))
	if follow {
		b.WriteString(" -L")
	}
	for _, h := range o.Headers {
		if h.Key == "" || h.Disabled {
			continue
		}
		fmt.Fprintf(&b, " \\\n  -H '%s: %s'", h.Key, h.Value)
	}
	switch o.BodyType {
	case "json", "text", "form":
		if o.Body != "" {
			fmt.Fprintf(&b, " \\\n  --data '%s'", o.Body)
		}
	case "form-data":
		for _, f := range o.FormFields {
			if f.Disabled {
				continue
			}
			if f.IsFile {
				fmt.Fprintf(&b, " \\\n  -F '%s=@%s'", f.Key, f.FilePath)
			} else {
				fmt.Fprintf(&b, " \\\n  -F '%s=%s'", f.Key, f.Value)
			}
		}
	}
	return b.String()
}

func goCode(o *outgoing) string {
	var b strings.Builder
	if o.BodyType == "form-data" {
		b.WriteString("var buf bytes.Buffer\n")
		b.WriteString("w := multipart.NewWriter(&buf)\n")
		for _, f := range o.FormFields {
			if f.Disabled {
				continue
			}
			if f.IsFile {
				fmt.Fprintf(&b, "data, _ := os.ReadFile(%s)\n", strconv.Quote(f.FilePath))
				fmt.Fprintf(&b, "fw, _ := w.CreateFormFile(%s, %s)\n", strconv.Quote(f.Key), strconv.Quote(baseName(f.FilePath)))
				b.WriteString("fw.Write(data)\n")
			} else {
				fmt.Fprintf(&b, "w.WriteField(%s, %s)\n", strconv.Quote(f.Key), strconv.Quote(f.Value))
			}
		}
		b.WriteString("w.Close()\n")
		fmt.Fprintf(&b, "req, err := http.NewRequest(%s, %s, &buf)\n", strconv.Quote(o.Method), strconv.Quote(finalURL(o)))
		b.WriteString("req.Header.Set(\"Content-Type\", w.FormDataContentType())\n")
	} else {
		if hasBody(o) {
			fmt.Fprintf(&b, "body := strings.NewReader(%s)\n", strconv.Quote(o.Body))
		}
		bodyArg := "nil"
		if hasBody(o) {
			bodyArg = "body"
		}
		fmt.Fprintf(&b, "req, err := http.NewRequest(%s, %s, %s)\n", strconv.Quote(o.Method), strconv.Quote(finalURL(o)), bodyArg)
	}
	for _, h := range o.Headers {
		if h.Key == "" || h.Disabled {
			continue
		}
		fmt.Fprintf(&b, "req.Header.Set(%s, %s)\n", strconv.Quote(h.Key), strconv.Quote(h.Value))
	}
	if o.BodyType == "json" && hasBody(o) {
		b.WriteString("req.Header.Set(\"Content-Type\", \"application/json\")\n")
	}
	b.WriteString("resp, err := http.DefaultClient.Do(req)\n")
	return b.String()
}

func jsCode(o *outgoing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "fetch(%s, {\n", strconv.Quote(finalURL(o)))
	fmt.Fprintf(&b, "  method: %s,\n", strconv.Quote(o.Method))
	if len(o.Headers) > 0 {
		b.WriteString("  headers: {")
		var hs []string
		for _, h := range o.Headers {
			if h.Key == "" || h.Disabled {
				continue
			}
			hs = append(hs, fmt.Sprintf("%s: %s", strconv.Quote(h.Key), strconv.Quote(h.Value)))
		}
		b.WriteString(strings.Join(hs, ", "))
		b.WriteString("},\n")
	}
	switch o.BodyType {
	case "form-data":
		b.WriteString("  body: formData,\n")
	case "json", "text", "form":
		if o.Body != "" {
			fmt.Fprintf(&b, "  body: %s,\n", strconv.Quote(o.Body))
		}
	}
	b.WriteString("})\n")
	return b.String()
}

func pythonCode(o *outgoing) string {
	var b strings.Builder
	b.WriteString("import requests\n")
	var dataFields, fileFields []formField
	for _, f := range o.FormFields {
		if f.Disabled {
			continue
		}
		if f.IsFile {
			fileFields = append(fileFields, f)
		} else {
			dataFields = append(dataFields, f)
		}
	}
	fmt.Fprintf(&b, "resp = requests.request(%s, %s", strconv.Quote(o.Method), strconv.Quote(finalURL(o)))
	if len(o.Headers) > 0 {
		b.WriteString(", headers={")
		var hs []string
		for _, h := range o.Headers {
			if h.Key == "" || h.Disabled {
				continue
			}
			hs = append(hs, fmt.Sprintf("%s: %s", strconv.Quote(h.Key), strconv.Quote(h.Value)))
		}
		b.WriteString(strings.Join(hs, ", "))
		b.WriteString("}")
	}
	switch o.BodyType {
	case "form-data":
		if len(dataFields) > 0 {
			b.WriteString(", data={")
			var ds []string
			for _, f := range dataFields {
				ds = append(ds, fmt.Sprintf("%s: %s", strconv.Quote(f.Key), strconv.Quote(f.Value)))
			}
			b.WriteString(strings.Join(ds, ", "))
			b.WriteString("}")
		}
		if len(fileFields) > 0 {
			b.WriteString(", files={")
			var fs []string
			for _, f := range fileFields {
				fs = append(fs, fmt.Sprintf("%s: open(%s, \"rb\")", strconv.Quote(f.Key), strconv.Quote(f.FilePath)))
			}
			b.WriteString(strings.Join(fs, ", "))
			b.WriteString("}")
		}
	case "json", "text", "form":
		if o.Body != "" {
			fmt.Fprintf(&b, ", data=%s", strconv.Quote(o.Body))
		}
	}
	b.WriteString(")\n")
	return b.String()
}
