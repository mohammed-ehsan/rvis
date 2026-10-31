package edit

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/mohammed-ehsan/rvis/internal/model"
)

// insertion adds keys that do not exist yet next to a traced sibling: a
// resource into an existing requests/limits map, or a whole requests/limits
// map next to the other one.
type insertion struct {
	src       *srcFile
	at        int
	flow      bool
	indent    string // block style: indentation of the new key
	child     string // block style: extra indentation for keys of a new group
	group     string // non-empty: create this group map
	prefix    string // newline needed before the insertion
	resources []string
}

func (in *insertion) text(vals map[string]string) string {
	var keys []string
	for _, r := range Resources {
		if v, ok := vals[r]; ok {
			keys = append(keys, r+": "+v)
		}
	}
	var b strings.Builder
	b.WriteString(in.prefix)
	switch {
	case in.group == "" && in.flow:
		for _, k := range keys {
			b.WriteString(k + ", ")
		}
	case in.group == "":
		for _, k := range keys {
			b.WriteString(in.indent + k + "\n")
		}
	case in.flow:
		b.WriteString(in.group + ": {" + strings.Join(keys, ", ") + "}, ")
	default:
		b.WriteString(in.indent + in.group + ":\n")
		for _, k := range keys {
			b.WriteString(in.indent + in.child + k + "\n")
		}
	}
	return b.String()
}

func parentPath(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[:i]
	}
	return ""
}

func otherGroup(g string) string {
	if g == "requests" {
		return "limits"
	}
	return "requests"
}

// planInserts finds a place for every missing field next to a sibling that
// was traced to a values file, then verifies each placement with a marker
// render: a key only counts as editable if the marker reaches the field.
func (e *Editor) planInserts(files []*srcFile, t Target, missing []Field, located map[Field]candidate, results map[Field]Result, base []*model.Workload, budget *int) {
	type groupKey struct{ container, group string }
	byGroup := map[groupKey][]Field{}
	var order []groupKey
	for _, f := range missing {
		k := groupKey{f.Container, f.Group}
		if byGroup[k] == nil {
			order = append(order, k)
		}
		byGroup[k] = append(byGroup[k], f)
	}

	type plan struct {
		ins    *insertion
		fields []Field
		path   string
	}
	var plans []*plan
	for _, k := range order {
		fields := byGroup[k]
		noPlace := Result{Reason: "the chart sets no " + k.group + " for this container in a values file; add a resources block there first"}
		for _, f := range fields {
			results[f] = noPlace
		}

		// A sibling in the same group: add the key into that map.
		var sib *candidate
		for _, r := range Resources {
			if c, ok := located[Field{k.container, k.group, r}]; ok {
				sib = &c
				break
			}
		}
		if sib != nil {
			if sib.key == nil {
				for _, f := range fields {
					results[f] = Result{Reason: "the other " + k.group + " value is written in a template; add this one there by hand"}
				}
				continue
			}
			ins := &insertion{src: sib.src}
			if sib.parent.Style&yaml.FlowStyle != 0 {
				ins.at, ins.flow = sib.src.offset(sib.key.Line, sib.key.Column), true
			} else {
				ins.at = sib.src.nextLineStart(sib.end)
				ins.indent = strings.Repeat(" ", sib.key.Column-1)
				_, ins.prefix = sib.src.atLineStart(ins.at)
			}
			for _, f := range fields {
				ins.resources = append(ins.resources, f.Resource)
			}
			plans = append(plans, &plan{ins, fields, parentPath(sib.path)})
			continue
		}

		// Otherwise a value in the other group: add a new group map
		// next to it.
		for _, r := range Resources {
			if c, ok := located[Field{k.container, otherGroup(k.group), r}]; ok && c.key != nil {
				sib = &c
				break
			}
		}
		if sib == nil || sib.parentKey == nil || sib.parentKey.Value != otherGroup(k.group) || sib.grand == nil {
			continue
		}
		pk := sib.parentKey
		ins := &insertion{src: sib.src, group: k.group}
		start := sib.src.offset(pk.Line, pk.Column)
		lineStart := sib.src.lines[pk.Line-1]
		if sib.grand.Style&yaml.FlowStyle != 0 {
			ins.at, ins.flow = start, true
		} else {
			if strings.TrimSpace(string(sib.src.data[lineStart:start])) != "" {
				continue
			}
			ins.at = lineStart
			ins.indent = strings.Repeat(" ", pk.Column-1)
			ins.child = "  "
			if sib.parent.Style&yaml.FlowStyle == 0 && sib.key.Column > pk.Column {
				ins.child = strings.Repeat(" ", sib.key.Column-pk.Column)
			}
		}
		for _, f := range fields {
			ins.resources = append(ins.resources, f.Resource)
		}
		path := parentPath(parentPath(sib.path))
		if path != "" {
			path += "."
		}
		plans = append(plans, &plan{ins, fields, path + k.group})
	}
	if len(plans) == 0 {
		return
	}

	// Verify, all at once first and one by one if that render fails.
	verify := func(ps []*plan) bool {
		if *budget <= 0 {
			return false
		}
		*budget--
		patches := map[*srcFile][]patch{}
		markerOf := map[Field]int{}
		n := 0
		for _, p := range ps {
			vals := map[string]string{}
			for _, f := range p.fields {
				vals[f.Resource] = `"` + marker(n) + `"`
				markerOf[f] = n
				n++
			}
			patches[p.ins.src] = append(patches[p.ins.src], patch{p.ins.at, p.ins.at, p.ins.text(vals)})
		}
		rep, err := e.render(files, patches)
		if err != nil || !complete(rep, base) {
			return false
		}
		landed := quantityMarkers{}.scan(rep, n)
		for _, p := range ps {
			for _, f := range p.fields {
				occ := landed[markerOf[f]]
				if !hits(occ, t, f) {
					results[f] = Result{Reason: "found no place in the values files that reaches this " + strings.TrimSuffix(f.Group, "s") + "; add it to the chart first"}
					continue
				}
				loc := &Location{File: p.ins.src.show, Line: p.ins.src.lineOf(p.ins.at) + boolInt(p.ins.prefix != ""), Path: p.path, Insert: true, src: p.ins.src, ins: p.ins}
				for _, o := range occ {
					if !(t.matches(o.workload) && o.container == f.Container && o.field == f.key()) {
						loc.Shared = append(loc.Shared, fmt.Sprintf("%s/%s %s %s", o.workload.Kind, o.workload.Name, o.container, o.field))
					}
				}
				sort.Strings(loc.Shared)
				results[f] = Result{Loc: loc}
			}
		}
		return true
	}
	if !verify(plans) {
		for _, p := range plans {
			verify([]*plan{p})
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
