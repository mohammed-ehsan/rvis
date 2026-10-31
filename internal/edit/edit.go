package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/source"
)

// Field identifies one request or limit of one container.
type Field struct {
	Container string
	Group     string // "requests" or "limits"
	Resource  string // "cpu" or "memory"
}

func (f Field) key() string {
	if f.Group == ReplicasGroup {
		return ReplicasGroup
	}
	return f.Group + "." + f.Resource
}

// ReplicasGroup is the Field group of a workload's replica count; such a
// field has no container or resource. Which value it edits (spec.replicas,
// parallelism or an autoscaler's minReplicas) follows
// model.Workload.ReplicaSource.
const ReplicasGroup = "replicas"

// ReplicasField is the field for a workload's replica count.
var ReplicasField = Field{Group: ReplicasGroup}

// Groups and Resources are the editable dimensions, in display order.
var (
	Groups    = []string{"requests", "limits"}
	Resources = []string{"cpu", "memory"}
)

// Target identifies the workload being edited.
type Target struct{ Kind, Name, Namespace string }

func (t Target) matches(w *model.Workload) bool {
	return w.Kind == t.Kind && w.Name == t.Name && w.Namespace == t.Namespace
}

// Location is where a field's value is defined.
type Location struct {
	File   string   // path shown to the user
	Line   int      // 1-based
	Path   string   // values path, e.g. "api.resources.limits.cpu"; empty for templates
	Insert bool     // the field does not exist yet and will be added
	Shared []string // other fields that read the same value

	src        *srcFile
	start, end int
	quote      byte
	ins        *insertion
}

// Result is the outcome of locating one field: a Location, or the reason
// it cannot be edited.
type Result struct {
	Loc    *Location
	Reason string
}

// Editor edits the files of a local chart.
type Editor struct {
	req      source.Request
	chartDir string
}

// New returns an editor, or an error explaining why the source is not
// editable (a release, stdin, a packaged or remote chart).
func New(req source.Request) (*Editor, error) {
	switch {
	case req.Release != "":
		return nil, errors.New("editing needs a local chart directory, not an installed release")
	case req.ManifestFile != "":
		return nil, errors.New("editing needs a local chart directory, not rendered manifests")
	case req.Repo != "":
		return nil, errors.New("editing needs a local chart directory, not a repository chart")
	}
	info, err := os.Stat(req.Chart)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("editing needs a local chart directory; %q is not one", req.Chart)
	}
	dir, err := filepath.Abs(req.Chart)
	if err != nil {
		return nil, err
	}
	return &Editor{req: req, chartDir: dir}, nil
}

// files loads the chart's values and template files plus the -f files.
func (e *Editor) files() ([]*srcFile, error) {
	var out []*srcFile
	seen := map[string]bool{}
	add := func(p string, kind fileKind, rank int) error {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			return err
		}
		seen[abs] = true
		f, err := loadFile(abs, e.chartDir, kind)
		if err != nil {
			return err
		}
		f.rank = rank
		if f.root != nil && rank >= 0 {
			f.defined = definedPaths(f.root)
		}
		out = append(out, f)
		return nil
	}
	for i, v := range e.req.ValueFiles {
		if _, err := os.Stat(v); err == nil {
			if err := add(v, valuesFile, 1+i); err != nil {
				return nil, err
			}
		}
	}
	rootValues := map[string]bool{
		filepath.Join(e.chartDir, "values.yaml"): true,
		filepath.Join(e.chartDir, "values.yml"):  true,
	}
	err := filepath.WalkDir(e.chartDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		switch {
		case name == "values.yaml" || name == "values.yml":
			rank := -1 // a subchart's own defaults
			if rootValues[p] {
				rank = 0
			}
			return add(p, valuesFile, rank)
		case strings.Contains(filepath.ToSlash(p), "/templates/") &&
			(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".tpl")):
			return add(p, templateFile, -1)
		}
		return nil
	})
	return out, err
}

// candidate is a place in a file that might define a field's value.
type candidate struct {
	src        *srcFile
	start, end int
	quote      byte
	path       string     // values path
	key        *yaml.Node // values: the leaf's key
	parent     *yaml.Node // values: the mapping holding the leaf
	parentKey  *yaml.Node // values: the key of that mapping, if any
	grand      *yaml.Node // values: the mapping holding parent
	qty        resource.Quantity
	// likely is set for values shaped like Kubernetes resources: a cpu or
	// memory key, or a key under requests/limits. These are traced first;
	// other equal numbers (replicaCount: 1 next to cpu: 1) only if needed.
	likely bool
}

func matchesAny(text string, want []resource.Quantity) (resource.Quantity, bool) {
	q, err := resource.ParseQuantity(strings.TrimSpace(text))
	if err != nil {
		return q, false
	}
	for _, w := range want {
		if q.Cmp(w) == 0 {
			return q, true
		}
	}
	return q, false
}

// resourceShaped reports whether a values key looks like it holds a
// container resource quantity.
func resourceShaped(key, parentKey string) bool {
	switch {
	case key == "cpu" || key == "memory" || key == "ephemeral-storage" || strings.Contains(key, "/"):
		return true
	case parentKey == "requests" || parentKey == "limits":
		return true
	}
	return false
}

// definedPaths lists every key path a values document sets.
func definedPaths(root *yaml.Node) map[string]bool {
	out := map[string]bool{}
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			out[p] = true
			walk(n.Content[i+1], p)
		}
	}
	walk(root, "")
	return out
}

// shadowed reports whether a later values file (by Helm's merge order)
// sets the same path, in which case this candidate cannot be the source.
func shadowed(c candidate, files []*srcFile) bool {
	if c.src.rank < 0 {
		return false
	}
	for _, f := range files {
		if f.rank > c.src.rank && f.defined[c.path] {
			return true
		}
	}
	return false
}

func valuesCandidates(f *srcFile, want []resource.Quantity) []candidate {
	var out []candidate
	var walk func(n *yaml.Node, path string, parentKey *yaml.Node, grand *yaml.Node)
	walk = func(n *yaml.Node, path string, parentKey *yaml.Node, grand *yaml.Node) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				p := k.Value
				if path != "" {
					p = path + "." + k.Value
				}
				if v.Kind == yaml.ScalarNode {
					if v.Tag != "!!str" && v.Tag != "!!int" && v.Tag != "!!float" {
						continue
					}
					if qty, ok := matchesAny(v.Value, want); ok {
						if s, e, q, ok := f.scalarSpan(v); ok {
							pk := ""
							if parentKey != nil {
								pk = parentKey.Value
							}
							out = append(out, candidate{src: f, start: s, end: e, quote: q, path: p, key: k, parent: n,
								parentKey: parentKey, grand: grand, qty: qty, likely: resourceShaped(k.Value, pk)})
						}
					}
					continue
				}
				walk(v, p, k, n)
			}
		case yaml.SequenceNode:
			for i, item := range n.Content {
				walk(item, path+"["+strconv.Itoa(i)+"]", nil, nil)
			}
		}
	}
	if f.root != nil {
		walk(f.root, "", nil, nil)
	}
	return out
}

var literalRe = regexp.MustCompile(`(?m)(?:^|[\s{,])(?:cpu|memory)\s*:[ \t]*("[^"\n]*"|'[^'\n]*'|[0-9][0-9.eE+-]*[a-zA-Z]*)`)

func templateCandidates(f *srcFile, want []resource.Quantity) []candidate {
	var out []candidate
	for _, m := range literalRe.FindAllSubmatchIndex(f.data, -1) {
		s, e := m[2], m[3]
		text, q := string(f.data[s:e]), byte(0)
		if text[0] == '"' || text[0] == '\'' {
			q, text = text[0], text[1:len(text)-1]
		}
		if strings.Contains(text, "{{") {
			continue
		}
		if qty, ok := matchesAny(text, want); ok {
			out = append(out, candidate{src: f, start: s, end: e, quote: q, qty: qty, likely: true})
		}
	}
	return out
}

// occurrence is a field of the rendered output where a marker appeared.
type occurrence struct {
	workload  *model.Workload
	container string
	field     string
}

func marker(i int) string { return strconv.Itoa(7_300_000_000+i) + "m" }

// render renders a temporary copy of the chart with the given patches.
func (e *Editor) render(files []*srcFile, patches map[*srcFile][]patch) (*model.Report, error) {
	tmp, err := os.MkdirTemp("", "rvis-trace-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	content := func(f *srcFile) ([]byte, error) {
		if ps := patches[f]; len(ps) > 0 {
			return applyPatches(f.data, append([]patch(nil), ps...))
		}
		return f.data, nil
	}
	byPath := map[string]*srcFile{}
	for _, f := range files {
		byPath[f.path] = f
	}

	chartCopy := filepath.Join(tmp, filepath.Base(e.chartDir))
	err = filepath.WalkDir(e.chartDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.chartDir, p)
		dst := filepath.Join(chartCopy, rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(dst, 0o755)
		}
		// Markers are not valid for the schema's types; validation is
		// irrelevant to tracing.
		if d.Name() == "values.schema.json" || !d.Type().IsRegular() {
			return nil
		}
		var data []byte
		if f := byPath[p]; f != nil {
			data, err = content(f)
		} else {
			data, err = os.ReadFile(p)
		}
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		return nil, err
	}

	req := e.req
	req.Chart = chartCopy
	req.ValueFiles = nil
	for i, v := range e.req.ValueFiles {
		abs, _ := filepath.Abs(v)
		f := byPath[abs]
		if f == nil {
			req.ValueFiles = append(req.ValueFiles, v)
			continue
		}
		if rel, err := filepath.Rel(e.chartDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			req.ValueFiles = append(req.ValueFiles, filepath.Join(chartCopy, rel))
			continue
		}
		data, err := content(f)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(tmp, fmt.Sprintf("values-%d.yaml", i))
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
		req.ValueFiles = append(req.ValueFiles, dst)
	}
	data, err := source.Load(req)
	if err != nil {
		return nil, err
	}
	return model.Parse(data, model.ParseOptions{})
}

// markerStyle decides what a candidate is replaced with while tracing and
// how the markers are recognized in the rendered output.
type markerStyle interface {
	text(i int, c candidate) string
	scan(rep *model.Report, n int) map[int][]occurrence // candidate index → where it landed
}

var markerRe = regexp.MustCompile(`^73\d{8}m$`)

// quantityMarkers are strings like "7300000004m": valid quantities that no
// chart uses, recognized in container requests and limits.
type quantityMarkers struct{}

func (quantityMarkers) text(i int, c candidate) string {
	if c.src.kind == templateFile {
		return quoted(marker(i), c.quote)
	}
	return `"` + marker(i) + `"`
}

func (quantityMarkers) scan(rep *model.Report, n int) map[int][]occurrence {
	out := map[int][]occurrence{}
	for _, w := range rep.Workloads {
		for _, c := range w.Containers {
			for k, v := range c.Raw {
				if !markerRe.MatchString(v) {
					continue
				}
				m, _ := strconv.Atoi(strings.TrimSuffix(v, "m"))
				if i := m - 7_300_000_000; i >= 0 && i < n {
					out[i] = append(out[i], occurrence{w, c.Name, k})
				}
			}
		}
	}
	return out
}

// replicaMarkers are plain integers base+i. Replica counts must stay
// integers to render, and stay small because charts loop over them (e.g.
// "until .Values.replicaCount"); base is chosen clear of every real count.
type replicaMarkers struct{ base int64 }

func (r replicaMarkers) text(i int, c candidate) string {
	return quoted(strconv.FormatInt(r.base+int64(i), 10), c.quote)
}

func (r replicaMarkers) scan(rep *model.Report, n int) map[int][]occurrence {
	out := map[int][]occurrence{}
	in := func(v int64) (int, bool) {
		i := int(v - r.base)
		return i, v >= r.base && i < n
	}
	for _, w := range rep.Workloads {
		if i, ok := in(w.Spec); ok && w.SpecSet {
			out[i] = append(out[i], occurrence{w, "", replicaKey(model.ReplicasSpec)})
		}
		if w.HPA != nil {
			if i, ok := in(w.HPA.Min); ok {
				out[i] = append(out[i], occurrence{w, "", replicaKey(model.ReplicasHPAMin)})
			}
		}
	}
	return out
}

// replicaKey names where a replica marker landed, so a marker in the spec
// count is not mistaken for one in the autoscaler minimum.
func replicaKey(src model.ReplicaSource) string {
	if src == model.ReplicasHPAMin {
		return "replicas.hpa"
	}
	return "replicas.spec"
}

// replicaBase picks a marker base above every replica count in the chart.
func replicaBase(ws []*model.Workload) int64 {
	base := int64(9000)
	for _, w := range ws {
		for _, v := range []int64{w.Spec, hpaMin(w)} {
			for v >= base {
				base += 10000
			}
		}
	}
	return base
}

func hpaMin(w *model.Workload) int64 {
	if w.HPA == nil {
		return 0
	}
	return w.HPA.Min
}

// complete reports whether a marker render still produced every workload
// of the original render. A marker in a non-resource value (replicas: 2 when
// a cpu value is also 2) can make a workload unparsable and drop it.
func complete(rep *model.Report, want []*model.Workload) bool {
	have := map[string]bool{}
	for _, w := range rep.Workloads {
		have[w.Kind+"/"+w.Namespace+"/"+w.Name] = true
	}
	for _, w := range want {
		if !have[w.Kind+"/"+w.Namespace+"/"+w.Name] {
			return false
		}
	}
	return true
}

// trace renders the given candidates with markers and records where each
// marker lands in found. When a render fails or loses a workload (a
// candidate was used as a number, a boolean, ...) the set is split in two
// and each half retried, so one bad candidate does not hide the others.
// Candidates that took part in a successful render are added to checked.
func (e *Editor) trace(files []*srcFile, cands []candidate, all []int, base []*model.Workload, style markerStyle, budget *int, found map[int][]occurrence, checked map[int]bool) {
	var run func(idx []int)
	run = func(idx []int) {
		if len(idx) == 0 || *budget <= 0 {
			return
		}
		*budget--
		patches := map[*srcFile][]patch{}
		for _, i := range idx {
			c := cands[i]
			patches[c.src] = append(patches[c.src], patch{c.start, c.end, style.text(i, c)})
		}
		rep, err := e.render(files, patches)
		if err == nil && !complete(rep, base) {
			err = errors.New("a workload disappeared")
		}
		if err != nil {
			if len(idx) > 1 {
				run(idx[:len(idx)/2])
				run(idx[len(idx)/2:])
			}
			return
		}
		for _, i := range idx {
			checked[i] = true
		}
		for i, occ := range style.scan(rep, len(cands)) {
			found[i] = occ
		}
	}
	run(all)
}

// traceBudget is how many renders tracing n candidates may take: enough to
// isolate several bad candidates by bisection, bounded for large charts.
func traceBudget(n int) int { return min(8+4*n, 160) }

// Locate finds where every request and limit of the target's containers is
// defined.
func (e *Editor) Locate(t Target) (map[Field]Result, error) {
	res, _, err := e.locate(t)
	return res, err
}

func (e *Editor) locate(t Target) (map[Field]Result, []*srcFile, error) {
	files, err := e.files()
	if err != nil {
		return nil, nil, err
	}
	data, err := source.Load(e.req)
	if err != nil {
		return nil, nil, err
	}
	rep, err := model.Parse(data, model.ParseOptions{})
	if err != nil {
		return nil, nil, err
	}
	var w *model.Workload
	for _, cand := range rep.Workloads {
		if t.matches(cand) {
			w = cand
		}
	}
	if w == nil {
		return nil, nil, fmt.Errorf("%s/%s is no longer in the rendered chart", t.Kind, t.Name)
	}

	var want []resource.Quantity
	for _, c := range w.Containers {
		for _, g := range Groups {
			for _, r := range Resources {
				if v, ok := c.Raw[g+"."+r]; ok {
					if q, err := resource.ParseQuantity(v); err == nil {
						want = append(want, q)
					}
				}
			}
		}
	}
	var cands []candidate
	for _, f := range files {
		var cs []candidate
		if f.kind == valuesFile {
			cs = valuesCandidates(f, want)
		} else {
			cs = templateCandidates(f, want)
		}
		for _, c := range cs {
			if !shadowed(c, files) {
				cands = append(cands, c)
			}
		}
	}

	// Trace resource-shaped candidates first. Only fields they do not
	// explain are traced through the remaining equal values, which are
	// often replica counts or ports that break renders when replaced.
	found, checked := map[int][]occurrence{}, map[int]bool{}
	var likely []int
	for i, c := range cands {
		if c.likely {
			likely = append(likely, i)
		}
	}
	budget := traceBudget(len(likely))
	e.trace(files, cands, likely, rep.Workloads, quantityMarkers{}, &budget, found, checked)

	unresolved := func() []resource.Quantity {
		var out []resource.Quantity
		for _, c := range w.Containers {
			for _, g := range Groups {
				for _, r := range Resources {
					f := Field{c.Name, g, r}
					v, ok := c.Raw[f.key()]
					if !ok || fieldFound(found, t, f) {
						continue
					}
					if q, err := resource.ParseQuantity(v); err == nil {
						out = append(out, q)
					}
				}
			}
		}
		return out
	}
	if open := unresolved(); len(open) > 0 {
		var rest []int
		for i, c := range cands {
			if !c.likely && matchesQty(c.qty, open) {
				rest = append(rest, i)
			}
		}
		budget = traceBudget(len(rest))
		e.trace(files, cands, rest, rep.Workloads, quantityMarkers{}, &budget, found, checked)
	}

	results := map[Field]Result{}
	located := map[Field]candidate{}
	var missing []Field
	for _, c := range w.Containers {
		for _, g := range Groups {
			for _, r := range Resources {
				f := Field{c.Name, g, r}
				if _, ok := c.Raw[f.key()]; !ok {
					missing = append(missing, f)
					continue
				}
				results[f] = Result{Reason: e.notFound(w, "the value")}
				if q, err := resource.ParseQuantity(c.Raw[f.key()]); err == nil {
					var unchecked int
					for i, cand := range cands {
						if !checked[i] && cand.qty.Cmp(q) == 0 {
							unchecked++
						}
					}
					if unchecked > 0 {
						results[f] = Result{Reason: fmt.Sprintf("could not be traced: %s with this value could not be tested (replacing them breaks the chart's rendering)", pluralize(unchecked, "candidate"))}
					}
				}
				for i, occ := range found {
					if !hits(occ, t, f) {
						continue
					}
					cand := cands[i]
					loc := &Location{
						File: cand.src.show, Line: cand.src.lineOf(cand.start), Path: cand.path,
						src: cand.src, start: cand.start, end: cand.end, quote: cand.quote,
					}
					for _, o := range occ {
						if !(t.matches(o.workload) && o.container == f.Container && o.field == f.key()) {
							loc.Shared = append(loc.Shared, fmt.Sprintf("%s/%s %s %s", o.workload.Kind, o.workload.Name, o.container, o.field))
						}
					}
					sort.Strings(loc.Shared)
					results[f] = Result{Loc: loc}
					located[f] = cand
					break
				}
			}
		}
	}
	if src, reason := w.ReplicaSource(); src == model.ReplicasNotEditable {
		results[ReplicasField] = Result{Reason: reason}
	} else {
		results[ReplicasField] = e.locateReplicas(files, t, w, src, rep.Workloads)
	}

	insertBudget := 2 + 2*len(missing)
	e.planInserts(files, t, missing, located, results, rep.Workloads, &insertBudget)
	for _, c := range w.Containers {
		if len(c.Raw) > 0 {
			continue
		}
		for _, g := range Groups {
			for _, r := range Resources {
				results[Field{c.Name, g, r}] = Result{Reason: "this container sets no resources anywhere in the chart; add a resources block to its template first"}
			}
		}
	}
	return results, files, nil
}

var (
	replicaLiteralRe = regexp.MustCompile(`(?m)(?:^|[\s{,])(?:replicas|parallelism|minReplicas)\s*:[ \t]*("\d+"|'\d+'|\d+)\b`)
	integerRe        = regexp.MustCompile(`^["']?\d+["']?$`)
)

// replicaShaped reports whether a values key looks like a replica count.
func replicaShaped(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "replica") || strings.Contains(k, "parallelism")
}

// locateReplicas traces the value that decides a workload's pod count,
// like locate does for resources but with integer markers.
func (e *Editor) locateReplicas(files []*srcFile, t Target, w *model.Workload, src model.ReplicaSource, base []*model.Workload) Result {
	value, set := w.Spec, w.SpecSet
	if src == model.ReplicasHPAMin {
		value, set = w.HPA.Min, true
	}
	if !set {
		return Result{Reason: fmt.Sprintf("the chart does not set %s for this workload (Kubernetes defaults it to 1); add it to the template first", w.ReplicaField())}
	}
	want := []resource.Quantity{*resource.NewQuantity(value, resource.DecimalSI)}
	var cands []candidate
	for _, f := range files {
		if f.kind == valuesFile {
			for _, c := range valuesCandidates(f, want) {
				if !integerRe.Match(f.data[c.start:c.end]) || shadowed(c, files) {
					continue
				}
				c.likely = replicaShaped(c.key.Value)
				cands = append(cands, c)
			}
			continue
		}
		for _, m := range replicaLiteralRe.FindAllSubmatchIndex(f.data, -1) {
			s, end := m[2], m[3]
			text, q := string(f.data[s:end]), byte(0)
			if text[0] == '"' || text[0] == '\'' {
				q, text = text[0], text[1:len(text)-1]
			}
			if n, err := strconv.ParseInt(text, 10, 64); err == nil && n == value {
				cands = append(cands, candidate{src: f, start: s, end: end, quote: q, likely: true})
			}
		}
	}

	style := replicaMarkers{base: replicaBase(base)}
	key := replicaKey(src)
	found, checked := map[int][]occurrence{}, map[int]bool{}
	var likely, rest []int
	for i, c := range cands {
		if c.likely {
			likely = append(likely, i)
		} else {
			rest = append(rest, i)
		}
	}
	budget := traceBudget(len(likely))
	e.trace(files, cands, likely, base, style, &budget, found, checked)
	if !hitsAny(found, t, key) {
		budget = traceBudget(len(rest))
		e.trace(files, cands, rest, base, style, &budget, found, checked)
	}
	for i, occ := range found {
		if !hitsKey(occ, t, "", key) {
			continue
		}
		c := cands[i]
		loc := &Location{File: c.src.show, Line: c.src.lineOf(c.start), Path: c.path, src: c.src, start: c.start, end: c.end, quote: c.quote}
		for _, o := range occ {
			if !(t.matches(o.workload) && o.field == key) {
				loc.Shared = append(loc.Shared, fmt.Sprintf("%s/%s %s", o.workload.Kind, o.workload.Name, o.field))
			}
		}
		sort.Strings(loc.Shared)
		return Result{Loc: loc}
	}
	for i := range cands {
		if !checked[i] {
			return Result{Reason: "could not be traced: some values equal to it could not be tested (replacing them breaks the chart's rendering)"}
		}
	}
	return Result{Reason: e.notFound(w, w.ReplicaField())}
}

// notFound explains why a value has no editable source. A workload rendered
// from a packaged subchart (charts/*.tgz) usually takes it from that
// subchart's defaults, which can be overridden under the subchart's name or
// alias in the parent's values.
func (e *Editor) notFound(w *model.Workload, what string) string {
	parts := strings.Split(w.Source, "/")
	if len(parts) >= 4 && parts[1] == "charts" {
		sub := parts[2]
		if info, err := os.Stat(filepath.Join(e.chartDir, "charts", sub)); err != nil || !info.IsDir() {
			return fmt.Sprintf("%s comes from the defaults of the packaged subchart %q, which can't be edited in place; override it under %q in your values file", what, sub, sub+":")
		}
	}
	return what + " is not defined by a plain value in the chart (computed in a template, or set with --set)"
}

func hitsAny(found map[int][]occurrence, t Target, key string) bool {
	for _, occ := range found {
		if hitsKey(occ, t, "", key) {
			return true
		}
	}
	return false
}

func hitsKey(occ []occurrence, t Target, container, key string) bool {
	for _, o := range occ {
		if t.matches(o.workload) && o.container == container && o.field == key {
			return true
		}
	}
	return false
}

func fieldFound(found map[int][]occurrence, t Target, f Field) bool {
	for _, occ := range found {
		if hits(occ, t, f) {
			return true
		}
	}
	return false
}

func matchesQty(q resource.Quantity, want []resource.Quantity) bool {
	for _, w := range want {
		if q.Cmp(w) == 0 {
			return true
		}
	}
	return false
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func hits(occ []occurrence, t Target, f Field) bool {
	for _, o := range occ {
		if t.matches(o.workload) && o.container == f.Container && o.field == f.key() {
			return true
		}
	}
	return false
}

// Apply writes new values ("500m", "1Gi") for one workload's fields and
// returns a line per change describing what was written where.
func (e *Editor) Apply(t Target, changes map[Field]string) ([]string, error) {
	return e.ApplyAll(map[Target]map[Field]string{t: changes})
}

// ApplyAll writes new values for several workloads in one pass. Every
// workload is located against the files as they are now, before anything is
// written, so a value shared between workloads is written once, conflicting
// values for it are rejected, and nothing is written if any field fails.
func (e *Editor) ApplyAll(changes map[Target]map[Field]string) ([]string, error) {
	for t, fields := range changes {
		for f, v := range fields {
			if f.Group == ReplicasGroup {
				if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 0 {
					return nil, fmt.Errorf("%s/%s: %q is not a replica count", t.Kind, t.Name, v)
				}
				continue
			}
			if _, err := resource.ParseQuantity(v); err != nil {
				return nil, fmt.Errorf("%s/%s %s %s: %q is not a valid quantity", t.Kind, t.Name, f.Container, f.key(), v)
			}
		}
	}

	// Locate all targets concurrently; each works in its own temp copy.
	targets := make([]Target, 0, len(changes))
	for t := range changes {
		targets = append(targets, t)
	}
	sort.Slice(targets, func(i, j int) bool { return fmt.Sprint(targets[i]) < fmt.Sprint(targets[j]) })
	located := make([]map[Field]Result, len(targets))
	errs := make([]error, len(targets))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			located[i], _, errs[i] = e.locate(t)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	type write struct {
		src   *srcFile
		p     patch
		value string // replacements: the value, to detect conflicts
		ins   *insertion
		vals  map[string]string // insertions: resource → value
		owner string
	}
	writes := map[string]*write{} // keyed by file and position
	var order []string
	original := map[string][]byte{}
	var summary []string
	for i, t := range targets {
		fields := make([]Field, 0, len(changes[t]))
		for f := range changes[t] {
			fields = append(fields, f)
		}
		sort.Slice(fields, func(a, b int) bool { return fmt.Sprint(fields[a]) < fmt.Sprint(fields[b]) })
		for _, f := range fields {
			v := changes[t][f]
			who := fmt.Sprintf("%s/%s %s %s", t.Kind, t.Name, f.Container, f.key())
			if f.Group == ReplicasGroup {
				who = fmt.Sprintf("%s/%s replicas", t.Kind, t.Name)
			}
			r := located[i][f]
			if r.Loc == nil {
				return nil, fmt.Errorf("%s cannot be edited: %s", who, r.Reason)
			}
			l := r.Loc
			if prev, ok := original[l.src.path]; ok && !bytes.Equal(prev, l.src.data) {
				return nil, fmt.Errorf("%s changed while saving; try again", l.File)
			}
			original[l.src.path] = l.src.data

			if l.ins != nil {
				key := fmt.Sprintf("%s#ins@%d#%s", l.src.path, l.ins.at, l.ins.group)
				w := writes[key]
				if w == nil {
					w = &write{src: l.src, ins: l.ins, vals: map[string]string{}, owner: who}
					writes[key] = w
					order = append(order, key)
				}
				if old, ok := w.vals[f.Resource]; ok {
					if old != v {
						return nil, fmt.Errorf("%s and %s add the same key (%s:%d) with different values", w.owner, who, l.File, l.Line)
					}
					continue
				}
				w.vals[f.Resource] = v
			} else {
				key := fmt.Sprintf("%s#%d-%d", l.src.path, l.start, l.end)
				if w := writes[key]; w != nil {
					if w.value != v {
						return nil, fmt.Errorf("%s and %s share one value (%s:%d); give them the same value", w.owner, who, l.File, l.Line)
					}
					continue
				}
				writes[key] = &write{src: l.src, p: patch{l.start, l.end, quoted(v, l.quote)}, value: v, owner: who}
				order = append(order, key)
			}
			summary = append(summary, fmt.Sprintf("%s:%d  %s = %s", l.File, l.Line, who, v))
		}
	}

	byFile := map[string][]patch{}
	srcs := map[string]*srcFile{}
	for _, key := range order {
		w := writes[key]
		p := w.p
		if w.ins != nil {
			p = patch{w.ins.at, w.ins.at, w.ins.text(w.vals)}
		}
		byFile[w.src.path] = append(byFile[w.src.path], p)
		srcs[w.src.path] = w.src
	}
	// Build every file's new content first so a bad patch writes nothing.
	out := map[string][]byte{}
	for path, ps := range byFile {
		data, err := applyPatches(original[path], ps)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", srcs[path].show, err)
		}
		out[path] = data
	}
	for path, data := range out {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
			return nil, err
		}
	}
	return summary, nil
}
