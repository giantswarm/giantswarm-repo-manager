package teamfiles

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
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
	FieldName          = "name"
	FieldComponentType = "componentType"
	FieldLifecycle     = "lifecycle"
	// FieldAlign is the entry's opt-in to alignment: with `align: true` the
	// reconciler changes the repository to its declared set-up on every
	// trigger; without it every run is a check.
	FieldAlign = "align"
	// FieldGen is the generators' block, the tail of an entry in the team
	// files: a field an edit adds goes before it.
	FieldGen = "gen"
)

// Lifecycle values (the schema's; the reconciler acts on them): deprecated
// and archived per PRD D5, deleted per the deletion decision of 2026-09-18.
const (
	LifecycleDeprecated = "deprecated"
	LifecycleArchived   = "archived"
	LifecycleDeleted    = "deleted"
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

// rerenderFile re-applies a pull request's change of one team file to the
// file as it reads now: the entries the pull request changes — different
// from, or absent in, the file at the pull request's merge base (was) — are
// taken from the pull request's version (want) and written into now, each
// in place, at its alphabetical place when now lacks it, and out of now when
// the pull request removes it. Every other byte of now stays. It returns the
// file and the names of the entries applied.
func rerenderFile(team string, was, want, now []byte) ([]byte, []string, error) {
	if want == nil {
		return nil, nil, errors.New("the pull request removes the file")
	}
	before, err := reposetup.ParseTeamFile(team, bytes.NewReader(was))
	if err != nil {
		return nil, nil, fmt.Errorf("at the merge base: %w", err)
	}
	after, err := reposetup.ParseTeamFile(team, bytes.NewReader(want))
	if err != nil {
		return nil, nil, fmt.Errorf("on the branch: %w", err)
	}
	current, err := reposetup.ParseTeamFile(team, bytes.NewReader(now))
	if err != nil {
		return nil, nil, fmt.Errorf("on the base: %w", err)
	}
	out := now
	var names []string
	for _, d := range after.Entries {
		if declares(before, d) {
			continue
		}
		if _, ok := current.Entry(d.Name); ok {
			out, err = ReplaceEntry(out, d.Name, d)
		} else {
			out, err = reposetup.InsertEntry(team, out, d)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", d.Name, err)
		}
		names = append(names, d.Name)
	}
	for _, d := range before.Entries {
		if _, kept := after.Entry(d.Name); kept {
			continue
		}
		if _, ok := current.Entry(d.Name); ok {
			if out, err = RemoveEntry(out, d.Name); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", d.Name, err)
			}
		}
		names = append(names, d.Name)
	}
	return out, names, nil
}

// declares says whether tf carries d as it is: an entry of the same name
// that renders the same.
func declares(tf *reposetup.TeamFile, d reposetup.Declaration) bool {
	e, ok := tf.Entry(d.Name)
	if !ok {
		return false
	}
	a, aerr := e.YAML()
	b, berr := d.YAML()
	return aerr == nil && berr == nil && a == b
}

// SetField returns d with the top-level scalar field set: a key the entry
// has keeps its place; an absent one is added before the gen block, the
// entry's tail in the team files, else after the other keys the author
// wrote.
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
	val := &yaml.Node{Kind: yaml.ScalarNode, Value: value}
	at := len(m.Content)
	for i := 0; i+1 < len(m.Content); i += 2 {
		switch m.Content[i].Value {
		case field:
			m.Content[i+1] = val
			at = -1
		case FieldGen:
			if at > i {
				at = i
			}
		}
	}
	if at >= 0 {
		m.Content = slices.Insert(m.Content, at, &yaml.Node{Kind: yaml.ScalarNode, Value: field}, val)
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
var keyOrder = []string{FieldName, FieldComponentType, "description", "visibility", FieldLifecycle, "defaultBranch", FieldAlign, "system", "choreReviewers", "requiredChecks", "replace", FieldGen}

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
