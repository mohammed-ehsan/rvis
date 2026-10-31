// Package edit writes resource requests and limits back into a chart's
// source files. It finds where each rendered value comes from by tracing:
// every values leaf and template literal that could be the source gets a
// unique marker value in a temporary copy of the chart, the copy is rendered,
// and whichever marker shows up in a container's field is its source. Edits
// are then applied as minimal text patches, so comments and formatting are
// kept.
package edit

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

type fileKind int

const (
	valuesFile fileKind = iota
	templateFile
)

type srcFile struct {
	path  string // absolute path of the original
	show  string // path shown to the user
	kind  fileKind
	data  []byte
	lines []int      // byte offset of each line start
	root  *yaml.Node // values files only
	// rank orders the values files that share the chart's top-level
	// namespace by Helm's merge precedence: 0 for the chart's values.yaml,
	// 1+i for the i-th -f file. -1 for subchart values and templates.
	rank int
	// defined holds every path the file sets, for shadowing checks.
	defined map[string]bool
}

func loadFile(path, chartDir string, kind fileKind) (*srcFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &srcFile{path: path, show: displayPath(path, chartDir), kind: kind, data: data, lines: []int{0}, rank: -1}
	for i, b := range data {
		if b == '\n' {
			f.lines = append(f.lines, i+1)
		}
	}
	if kind == valuesFile {
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f.show, err)
		}
		if len(doc.Content) > 0 {
			f.root = doc.Content[0]
		}
	}
	return f, nil
}

// displayPath shortens p: relative to the working directory when below it,
// otherwise relative to the chart's parent ("mychart/values.yaml").
func displayPath(p, chartDir string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if rel, err := filepath.Rel(filepath.Dir(chartDir), p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

// offset converts a 1-based line and rune column (as yaml.v3 reports them)
// into a byte offset.
func (f *srcFile) offset(line, col int) int {
	if line < 1 || line > len(f.lines) {
		return -1
	}
	off := f.lines[line-1]
	for i := 1; i < col && off < len(f.data); i++ {
		_, n := utf8.DecodeRune(f.data[off:])
		off += n
	}
	return off
}

// lineOf returns the 1-based line containing a byte offset.
func (f *srcFile) lineOf(off int) int {
	return sort.Search(len(f.lines), func(i int) bool { return f.lines[i] > off })
}

// nextLineStart is the offset just after the line containing off.
func (f *srcFile) nextLineStart(off int) int {
	if l := f.lineOf(off); l < len(f.lines) {
		return f.lines[l]
	}
	return len(f.data)
}

// patch replaces data[start:end] with text; start == end inserts.
type patch struct {
	start, end int
	text       string
}

func applyPatches(data []byte, ps []patch) ([]byte, error) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].start > ps[j].start })
	out := append([]byte(nil), data...)
	limit := len(out)
	for _, p := range ps {
		if p.start < 0 || p.end < p.start || p.end > limit {
			return nil, fmt.Errorf("overlapping or invalid edit at byte %d", p.start)
		}
		out = append(out[:p.start], append([]byte(p.text), out[p.end:]...)...)
		limit = p.start
	}
	return out, nil
}

// atLineStart returns the prefix an insertion at off needs to start on its
// own line: a newline when off is the end of a file without a trailing one.
func (f *srcFile) atLineStart(off int) (int, string) {
	if off == len(f.data) && len(f.data) > 0 && !bytes.HasSuffix(f.data, []byte("\n")) {
		return off, "\n"
	}
	return off, ""
}

// scalarSpan returns the byte span of a scalar node's text, including any
// quotes, and the quote character used.
func (f *srcFile) scalarSpan(n *yaml.Node) (start, end int, quote byte, ok bool) {
	start = f.offset(n.Line, n.Column)
	if start < 0 || start >= len(f.data) {
		return 0, 0, 0, false
	}
	switch n.Style {
	case yaml.DoubleQuotedStyle:
		for i := start + 1; i < len(f.data); i++ {
			switch f.data[i] {
			case '\\':
				i++
			case '"':
				return start, i + 1, '"', true
			case '\n':
				return 0, 0, 0, false
			}
		}
	case yaml.SingleQuotedStyle:
		for i := start + 1; i < len(f.data); i++ {
			if f.data[i] == '\'' {
				if i+1 < len(f.data) && f.data[i+1] == '\'' {
					i++
					continue
				}
				return start, i + 1, '\'', true
			}
			if f.data[i] == '\n' {
				return 0, 0, 0, false
			}
		}
	case 0, yaml.TaggedStyle:
		end = start + len(n.Value)
		if end <= len(f.data) && string(f.data[start:end]) == n.Value {
			return start, end, 0, true
		}
	}
	return 0, 0, 0, false
}

func quoted(v string, q byte) string {
	if q == 0 {
		return v
	}
	return string(q) + v + string(q)
}
