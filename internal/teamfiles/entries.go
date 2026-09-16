package teamfiles

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"gopkg.in/yaml.v3"
)

// Team files are hand-written: header comment, comments on entries, the
// authors' key order. Every edit here keeps the file's own bytes and touches
// only the lines of the one entry it changes — the way reposetup.InsertEntry
// adds one.

// Field names of an entry the tools edit.
const (
	FieldName      = "name"
	FieldLifecycle = "lifecycle"
)

// Lifecycle values (the schema's; the reconciler acts on them, PRD D5).
const (
	LifecycleDeprecated = "deprecated"
	LifecycleArchived   = "archived"
)

// entryRange locates entry name in file: the byte offsets of its lines
// (from its "- name" line to the line before the next entry's comment or
// content, or the end of the file).
func entryRange(file []byte, name string) (start, end int, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(file, &doc); err != nil {
		return 0, 0, fmt.Errorf("parse team file: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		return 0, 0, fmt.Errorf("team file is not a YAML list of entries")
	}
	items := doc.Content[0].Content
	idx := -1
	for i, it := range items {
		if it.Kind == yaml.MappingNode && mappingValue(it, FieldName) == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, 0, fmt.Errorf("%w: %s", ErrEntryNotFound, name)
	}
	lines := splitLines(file)
	startLine := items[idx].Line - 1 // yaml lines are 1-based
	endLine := len(lines)
	if idx+1 < len(items) {
		next := items[idx+1]
		endLine = next.Line - 1
		if next.HeadComment != "" {
			endLine -= strings.Count(next.HeadComment, "\n") + 1
		}
	}
	// A blank line between the entry and the next stays with the entry.
	return offsetOf(lines, startLine), offsetOf(lines, endLine), nil
}

// ReplaceEntry returns file with entry name rewritten as d renders in a team
// file; every other byte stays.
func ReplaceEntry(file []byte, name string, d reposetup.Declaration) ([]byte, error) {
	start, end, err := entryRange(file, name)
	if err != nil {
		return nil, err
	}
	rendered, err := d.YAML()
	if err != nil {
		return nil, fmt.Errorf("render entry: %w", err)
	}
	// The file keeps the entry's own head comment; the rendering carries it
	// too and would double it.
	var out bytes.Buffer
	out.Write(file[:start])
	out.WriteString(strings.TrimRight(stripHeadComment(rendered), "\n"))
	out.WriteByte('\n')
	out.Write(file[end:])
	return out.Bytes(), nil
}

// RemoveEntry returns file without entry name.
func RemoveEntry(file []byte, name string) ([]byte, error) {
	start, end, err := entryRange(file, name)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, file[:start]...), file[end:]...), nil
}

// SetField returns d with the top-level scalar field set (added at the end
// when absent, after the other keys the author wrote).
func SetField(d reposetup.Declaration, field, value string) (reposetup.Declaration, error) {
	text, err := d.YAML()
	if err != nil {
		return d, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return d, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode || len(doc.Content[0].Content) != 1 {
		return d, fmt.Errorf("entry %s did not render as one list item", d.Name)
	}
	m := doc.Content[0].Content[0]
	set := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == field {
			m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: value}
			set = true
		}
	}
	if !set {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field}, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return d, err
	}
	return ParseEntry(buf.Bytes())
}

// stripHeadComment drops the comment lines before the first list item.
func stripHeadComment(s string) string {
	for {
		line, rest, ok := strings.Cut(s, "\n")
		if !strings.HasPrefix(strings.TrimSpace(line), "#") || !ok {
			return s
		}
		s = rest
	}
}

// ParseEntry parses one list item (or a bare mapping) as a declaration.
func ParseEntry(text []byte) (reposetup.Declaration, error) {
	s := strings.TrimSpace(stripHeadComment(string(text)))
	if !strings.HasPrefix(s, "- ") {
		s = "- " + strings.ReplaceAll(s, "\n", "\n  ")
	}
	tf, err := reposetup.ParseTeamFile("entry", strings.NewReader(s))
	if err != nil {
		return reposetup.Declaration{}, err
	}
	if len(tf.Entries) != 1 {
		return reposetup.Declaration{}, fmt.Errorf("expected one entry, got %d", len(tf.Entries))
	}
	return tf.Entries[0], nil
}

// keyOrder is the order the team files write an entry's keys in; keys not
// listed follow alphabetically.
var keyOrder = []string{FieldName, "componentType", "description", "visibility", FieldLifecycle, "system", "choreReviewers", "replace", "gen"}

// EntryFromValue renders a JSON-compatible value (a tool argument) as a
// declaration, its keys in the team files' order (name first).
func EntryFromValue(v any) (reposetup.Declaration, error) {
	var node yaml.Node
	if err := node.Encode(orderKeys(v)); err != nil {
		return reposetup.Declaration{}, fmt.Errorf("encode entry: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{&node}}); err != nil {
		return reposetup.Declaration{}, fmt.Errorf("encode entry: %w", err)
	}
	return ParseEntry(buf.Bytes())
}

// orderKeys turns maps into yaml.MapSlice-like ordered nodes (yaml.v3 encodes
// a Go map with sorted keys; the team files put name first).
func orderKeys(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		if list, ok := v.([]any); ok {
			out := make([]any, len(list))
			for i, e := range list {
				out[i] = orderKeys(e)
			}
			return out
		}
		return v
	}
	node := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string) {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k})
		var val yaml.Node
		_ = val.Encode(orderKeys(m[k]))
		node.Content = append(node.Content, &val)
	}
	seen := map[string]bool{}
	for _, k := range keyOrder {
		if _, ok := m[k]; ok {
			add(k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		add(k)
	}
	return node
}

func mappingValue(m *yaml.Node, key string) string {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1].Value
		}
	}
	return ""
}

func splitLines(b []byte) [][]byte {
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// offsetOf is the byte offset of line n (0-based); len(b) past the end.
func offsetOf(lines [][]byte, n int) int {
	off := 0
	for i := 0; i < n && i < len(lines); i++ {
		off += len(lines[i])
	}
	return off
}
