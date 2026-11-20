package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// jsonNode is one row of the response JSON tree: an object, an array or a
// scalar. Its display text is precomputed for both states, so a paint only
// reads strings — nothing formats per frame.
type jsonNode struct {
	depth    int
	branch   bool // an object or array with at least one member
	open     bool
	keyText  string // `"name": ` or `0: ` or ``
	openText string // `{` or `[`, shown while expanded
	shutText string // `{…} 3 项`, shown while collapsed
	valText  string // a scalar's value, or {} / [] for an empty container
	valKind  byte   // 's' string, 'n' number, 'b' bool, 'z' null, 'e' empty container
	children []jsonNode
}

// parseJSONTree builds the tree of a response body, or nil when the body is
// not a JSON document. The root starts expanded; everything under it starts
// collapsed.
func parseJSONTree(body string) *jsonNode {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	root := buildJSONNode("", v, 0, false)
	root.open = true
	return &root
}

// buildJSONNode builds one node and its children. For an array member, key is
// the element's index and indexed is true.
func buildJSONNode(key string, v any, depth int, indexed bool) jsonNode {
	n := jsonNode{depth: depth}
	switch {
	case indexed:
		n.keyText = key + ": "
	case key != "":
		n.keyText = strconv.Quote(key) + ": "
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			n.valText, n.valKind = "{}", 'e'
			return n
		}
		n.branch, n.openText = true, "{"
		n.shutText = "{…} " + strconv.Itoa(len(t)) + " 项"
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n.children = append(n.children, buildJSONNode(k, t[k], depth+1, false))
		}
	case []any:
		if len(t) == 0 {
			n.valText, n.valKind = "[]", 'e'
			return n
		}
		n.branch, n.openText = true, "["
		n.shutText = "[…] " + strconv.Itoa(len(t)) + " 项"
		for i, e := range t {
			n.children = append(n.children, buildJSONNode(strconv.Itoa(i), e, depth+1, true))
		}
	default:
		n.valText, n.valKind = jsonValueText(v)
	}
	return n
}

// jsonValueText renders a scalar and says what it is, for its color.
func jsonValueText(v any) (string, byte) {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t), 's'
	case json.Number:
		return t.String(), 'n'
	case bool:
		if t {
			return "true", 'b'
		}
		return "false", 'b'
	case nil:
		return "null", 'z'
	}
	return "", 'z'
}

// rebuildJSONRows flattens the expanded branches into the visible rows the
// tree declares. It is a tabState method: a result landing in a background tab
// rebuilds that tab's rows, not the one on screen.
func (t *tabState) rebuildJSONRows() {
	t.jsonRows = t.jsonRows[:0]
	var walk func(*jsonNode)
	walk = func(n *jsonNode) {
		t.jsonRows = append(t.jsonRows, n)
		if n.branch && n.open {
			for i := range n.children {
				walk(&n.children[i])
			}
		}
	}
	if t.jsonRoot != nil {
		walk(t.jsonRoot)
	}
}

// toggleJSONRow opens or closes the branch of visible row i.
func (t *tabState) toggleJSONRow(i int) {
	if i < 0 || i >= len(t.jsonRows) {
		return
	}
	n := t.jsonRows[i]
	if !n.branch {
		return
	}
	n.open = !n.open
	t.rebuildJSONRows()
}

// jsonNodeAt describes visible row i for the tree.
func (m *model) jsonNodeAt(i int) ui.TreeNode {
	if i < 0 || i >= len(m.t().jsonRows) {
		return ui.TreeNode{}
	}
	n := m.t().jsonRows[i]
	return ui.TreeNode{Depth: n.depth, Branch: n.branch, Expanded: n.open}
}

// jsonLabelAt names visible row i, for assistive technology and type-ahead.
func (m *model) jsonLabelAt(i int) string {
	if i < 0 || i >= len(m.t().jsonRows) {
		return ""
	}
	n := m.t().jsonRows[i]
	if n.branch {
		if n.open {
			return n.keyText + n.openText
		}
		return n.keyText + n.shutText
	}
	return n.keyText + n.valText
}

// jsonRowContent paints visible row i: the key in the foreground, the value in
// its kind's color.
func (m *model) jsonRowContent(i int) {
	if i < 0 || i >= len(m.t().jsonRows) {
		return
	}
	n := m.t().jsonRows[i]
	theme := m.c.Theme()
	val, kind := n.valText, n.valKind
	if n.branch {
		if n.open {
			val = n.openText
		} else {
			val = n.shutText
		}
		kind = 'e'
	}
	color := theme.Foreground
	switch kind {
	case 's':
		color = theme.Success
	case 'n':
		color = theme.Primary
	case 'b':
		// Accent is a surface colour, nearly invisible as text on light: bools
		// get a purple that reads on both themes.
		color = gfx.RGB24(0x8250df)
	case 'z':
		color = theme.MutedForeground
	case 'e':
		color = theme.MutedForeground
	}
	ui.Row(m.c).Gap(0).Children(func() {
		if n.keyText != "" {
			// Shrink(0): a long value must squeeze itself (Ellipsis), not the key
			ui.Text(m.c, n.keyText).Mono().FontSize(13).Shrink(0)
		}
		ui.Text(m.c, val).Mono().FontSize(13).Color(color).Grow(1).Ellipsis()
	})
}
