package ui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/units"
)

// SortKey selects the column rows are ordered by.
type SortKey int

const (
	SortCPUReq SortKey = iota
	SortCPULim
	SortMemReq
	SortMemLim
	SortPods
	SortName
	SortKind
	SortManifest
	sortKeyCount
)

var sortNames = map[SortKey]string{
	SortCPUReq: "cpu-req", SortCPULim: "cpu-lim", SortMemReq: "mem-req", SortMemLim: "mem-lim",
	SortPods: "pods", SortName: "name", SortKind: "kind", SortManifest: "manifest",
}

// ParseSortKey converts a --sort flag value.
func ParseSortKey(s string) (SortKey, error) {
	for k, n := range sortNames {
		if n == s {
			return k, nil
		}
	}
	names := make([]string, 0, len(sortNames))
	for k := SortKey(0); k < sortKeyCount; k++ {
		names = append(names, sortNames[k])
	}
	return 0, fmt.Errorf("unknown sort key %q (one of: %s)", s, strings.Join(names, ", "))
}

// Capacity optionally sizes the header meters against a node or cluster.
type Capacity struct {
	CPU    int64 // millicores
	Memory int64 // bytes
}

// State is everything that determines what the screen shows.
type State struct {
	Report   *model.Report
	Source   string
	PerPod   bool // show one pod instead of pod × replicas
	Flat     bool // hide containers below workloads
	Sort     SortKey
	Invert   bool // reverse the column's natural order
	Filter   string
	Capacity Capacity
	Nodes    int64

	watch   *watchState // set while the TUI watches the chart's files
	editing *quickEdit  // set while the table is in quick edit mode
}

type row struct {
	w    *model.Workload
	c    *model.Container // nil for the workload row
	last bool             // last container of its workload (tree glyph)
}

// shown is the report on screen: the live preview while quick editing.
func (s *State) shown() *model.Report {
	if s.editing != nil {
		return s.editing.preview
	}
	return s.Report
}

// res returns the resources displayed for a row under the current mode.
// While quick editing, container rows show single-container values (what
// is being typed) and workload rows the replica-weighted footprint.
func (s *State) res(r row) model.Resources {
	n := r.w.Replicas
	if s.PerPod || (s.editing != nil && r.c != nil) {
		n = 1
	}
	if r.c != nil {
		return r.c.Resources.Scale(n)
	}
	return r.w.Pod.Scale(n)
}

func (s *State) total() model.Resources {
	if !s.PerPod {
		return s.shown().Total
	}
	var t model.Resources
	for _, w := range s.shown().Workloads {
		t = t.Add(w.Pod)
	}
	return t
}

func (s *State) sortValue(r model.Resources) int64 {
	switch s.Sort {
	case SortCPUReq:
		return r.CPU.Req.Value
	case SortCPULim:
		return r.CPU.Lim.Value
	case SortMemReq:
		return r.Memory.Req.Value
	case SortMemLim:
		return r.Memory.Lim.Value
	}
	return 0
}

func (s *State) less(a, b row) bool {
	var c int
	switch s.Sort {
	case SortManifest:
		return false
	case SortName:
		c = strings.Compare(nameOf(a), nameOf(b))
	case SortKind:
		c = strings.Compare(a.w.Kind, b.w.Kind)
		if c == 0 {
			c = strings.Compare(nameOf(a), nameOf(b))
		}
	case SortPods:
		c = cmpInt(a.w.Replicas, b.w.Replicas)
	default:
		c = cmpInt(s.sortValue(s.res(a)), s.sortValue(s.res(b)))
	}
	// Numeric columns read best biggest-first; text columns A→Z.
	if s.numericSort() {
		c = -c
	}
	if s.Invert {
		c = -c
	}
	return c < 0
}

func (s *State) numericSort() bool {
	return s.Sort != SortName && s.Sort != SortKind && s.Sort != SortManifest
}

func nameOf(r row) string {
	if r.c != nil {
		return r.c.Name
	}
	return r.w.Name
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *State) matches(w *model.Workload) bool {
	if s.Filter == "" {
		return true
	}
	f := strings.ToLower(s.Filter)
	if strings.Contains(strings.ToLower(w.Name), f) || strings.Contains(strings.ToLower(w.Kind), f) {
		return true
	}
	for _, c := range w.Containers {
		if strings.Contains(strings.ToLower(c.Name), f) || strings.Contains(strings.ToLower(c.Image), f) {
			return true
		}
	}
	return false
}

// rows returns the visible table rows in display order.
func (s *State) rows() []row {
	var ws []row
	for _, w := range s.shown().Workloads {
		if s.matches(w) {
			ws = append(ws, row{w: w})
		}
	}
	sort.SliceStable(ws, func(i, j int) bool { return s.less(ws[i], ws[j]) })
	var out []row
	for _, wr := range ws {
		out = append(out, wr)
		if s.Flat {
			continue
		}
		cs := make([]row, 0, len(wr.w.Containers))
		for _, c := range wr.w.Containers {
			cs = append(cs, row{w: wr.w, c: c})
		}
		if s.Sort != SortPods && s.Sort != SortKind {
			sort.SliceStable(cs, func(i, j int) bool { return s.less(cs[i], cs[j]) })
		}
		if len(cs) > 0 {
			cs[len(cs)-1].last = true
		}
		out = append(out, cs...)
	}
	if s.editing != nil {
		// Keep rows where they were when editing started, so they don't
		// jump around as typed values change the sort order.
		out = s.editing.arrange(out)
	}
	return out
}

// ---------------------------------------------------------------- layout --

type layout struct {
	width               int
	kind, name, pods    int
	qos, num, bar       int
	showBars, showKinds bool
}

const (
	colNum  = 8
	colPods = 6
	colQoS  = 4
	colKind = 7
)

func (s *State) layout(width int) layout {
	l := layout{width: width, kind: colKind, pods: colPods, qos: colQoS, num: colNum, showKinds: true}
	longest := 12
	for _, w := range s.shown().Workloads {
		longest = max(longest, ansi.StringWidth(w.Name))
		for _, c := range w.Containers {
			longest = max(longest, ansi.StringWidth(c.Name)+3+len(roleTag(c)))
		}
	}
	// gutter + kind + pods + qos + 4 numbers, each followed by a space
	fixed := 1 + (l.kind + 1) + (l.pods + 1) + (l.qos + 1) + 4*(l.num+1)
	if width < fixed+14 {
		l.showKinds = false
		fixed -= l.kind + 1
		l.kind = 0
	}
	// Bars are the point of the tool, so names give way to them, down to a
	// readable minimum.
	const minBar = 11
	l.name = min(longest+1, 36)
	if width-fixed-l.name < 2*minBar+2 {
		l.name = max(18, width-fixed-2*minBar-2)
	}
	rest := width - fixed - l.name
	if rest >= 2*minBar+2 {
		l.showBars = true
		l.bar = (rest - 2) / 2
		if l.bar > 50 {
			// Very wide terminal: give some space back to names.
			l.name += min(2*(l.bar-50), max(0, longest+1-l.name))
			l.bar = (width - fixed - l.name - 2) / 2
		}
	} else {
		l.name = max(8, width-fixed)
	}
	return l
}

// ----------------------------------------------------------------- cells --

func fitL(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

func fitR(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return strings.Repeat(" ", w-ansi.StringWidth(s)) + s
}

type fmtFn func(int64) string

// memFmt formats memory in the units the chart used: decimal (3G) when
// every contributing value was decimal, binary (2.8Gi) otherwise.
func memFmt(q model.Quantity) fmtFn {
	if q.Units == model.UnitsDecimal {
		return units.DecimalBytes
	}
	return units.Bytes
}

func cpuFmt(model.Quantity) fmtFn { return units.Cores }

// qty renders an aggregated quantity: "—" for a missing request, "∞" for a
// missing limit, and a "+" suffix when only part of the pod is bounded.
func qty(q model.Quantity, lim bool, f fmtFn) (string, lipgloss.Style) {
	switch {
	case q.Missing == 0:
		return f(q.Value), stBase
	case q.Value == 0 && lim:
		return "∞", stUnbounded
	case q.Value == 0:
		return "—", stMissing
	}
	return f(q.Value) + "+", stPartial
}

var kindShort = map[string]string{
	"Deployment": "deploy", "StatefulSet": "sts", "DaemonSet": "ds", "ReplicaSet": "rs",
	"ReplicationController": "rc", "Job": "job", "CronJob": "cronjob", "Pod": "pod", "Rollout": "rollout",
}

func shortKind(k string) string {
	if s, ok := kindShort[k]; ok {
		return s
	}
	return strings.ToLower(k)
}

func roleTag(c *model.Container) string {
	switch c.Role {
	case model.RoleInit:
		return " init"
	case model.RoleSidecar:
		return " sidecar"
	}
	return ""
}

func podsText(w *model.Workload) string {
	if w.PerNode {
		return strconv.FormatInt(w.Replicas, 10) + "ds"
	}
	s := strconv.FormatInt(w.Replicas, 10)
	if w.HPA != nil {
		s += "↑"
	}
	return s
}

func qosText(q model.QoS) (string, lipgloss.Style) {
	switch q {
	case model.QoSGuaranteed:
		return "Gua", stQoSGuaranteed
	case model.QoSBestEffort:
		return "BE", stQoSBestEffort
	}
	return "Bur", stQoSBurstable
}

// ------------------------------------------------------------------ bars --

type cls uint8

const (
	clsEmpty cls = iota
	clsReq
	clsLim
	clsText
	clsOver
	clsUnbounded
)

var clsStyle = map[cls]lipgloss.Style{
	clsEmpty: stBase, clsReq: stReq, clsLim: stLim, clsText: stMeterText,
	clsOver: stOver, clsUnbounded: stUnbounded,
}

type barCell struct {
	r rune
	c cls
}

// fillBar lays out request ("|") and limit headroom (":") cells scaled to
// scale. An unbounded limit is marked with "»" at the far end.
func fillBar(p model.Pair, scale int64, inner int) []barCell {
	cells := make([]barCell, inner)
	for i := range cells {
		cells[i] = barCell{' ', clsEmpty}
	}
	if inner <= 0 {
		return cells
	}
	n := func(v int64) int {
		if scale <= 0 || v <= 0 {
			return 0
		}
		k := int(math.Round(float64(v) / float64(scale) * float64(inner)))
		return min(max(k, 1), inner)
	}
	r := n(p.Req.Value)
	l := max(r, n(p.Lim.Value))
	for i := 0; i < r; i++ {
		cells[i] = barCell{'|', clsReq}
	}
	for i := r; i < l; i++ {
		cells[i] = barCell{':', clsLim}
	}
	if p.Lim.Missing > 0 && (p.Req.Value > 0 || p.Lim.Value > 0 || p.Req.Missing > 0) {
		cells[inner-1] = barCell{'»', clsUnbounded}
	}
	return cells
}

func renderCells(cells []barCell, plain bool) string {
	var b strings.Builder
	for i := 0; i < len(cells); {
		j := i
		for j < len(cells) && cells[j].c == cells[i].c {
			j++
		}
		var seg strings.Builder
		for _, c := range cells[i:j] {
			seg.WriteRune(c.r)
		}
		if plain || cells[i].c == clsEmpty {
			b.WriteString(seg.String())
		} else {
			b.WriteString(clsStyle[cells[i].c].Render(seg.String()))
		}
		i = j
	}
	return b.String()
}

func bracketed(inner string, plain bool) string {
	if plain {
		return "[" + inner + "]"
	}
	return stBracket.Render("[") + inner + stBracket.Render("]")
}

// meter renders an htop-style header meter: "CPU[|||||:::   1.2 / 3 cores]".
func meter(label string, p model.Pair, capacity int64, fq func(model.Quantity) fmtFn, unit string, width int) string {
	scale := max(p.Req.Value, p.Lim.Value)
	if capacity > 0 {
		scale = capacity
	}
	inner := width - len(label) - 2
	if inner < 4 {
		return ""
	}
	cells := fillBar(p, scale, inner)

	req, _ := qty(p.Req, false, fq(p.Req))
	lim, _ := qty(p.Lim, true, fq(p.Lim))
	text := "req " + req + " / lim " + lim
	if capacity > 0 {
		text += " of " + fq(model.Quantity{})(capacity)
	}
	text += unit + " "
	over := capacity > 0 && (p.Req.Value > capacity)
	if over {
		text = "! " + text
	}
	runes := []rune(text)
	if len(runes) < inner-2 {
		c := clsText
		if over {
			c = clsOver
		}
		start := inner - len(runes)
		for i, r := range runes {
			cells[start+i] = barCell{r, c}
		}
	}
	return stLabelBold.Render(label) + bracketed(renderCells(cells, false), false)
}

// --------------------------------------------------------------- header ---

// headerLines renders the meters and the summary; maxLines > 0 caps the
// height for small terminals, keeping meters before summary lines.
func (s *State) headerLines(width, maxLines int) []string {
	out := s.allHeaderLines(width)
	if s.watch != nil && s.watch.err != "" {
		// The render error goes first so it is never cut off.
		msg, _, _ := strings.Cut(s.watch.err, "\n")
		errLine := stErr.Render(" ✖ ") + fitL(msg, width-12) + stDim.Render(" F4 more")
		out = append([]string{fitL(errLine, width)}, out...)
	}
	if maxLines > 0 && len(out) > maxLines {
		out = out[:max(1, maxLines)]
	}
	return out
}

func (s *State) allHeaderLines(width int) []string {
	rep := s.shown()
	tot := s.total()
	meters := 2
	if tot.HasEphemeral() {
		meters = 3
	}

	var gua, bur, be int
	for _, w := range rep.Workloads {
		switch w.QoS {
		case model.QoSGuaranteed:
			gua++
		case model.QoSBurstable:
			bur++
		default:
			be++
		}
	}
	var errs, warns int
	for _, w := range rep.Warnings {
		switch w.Severity {
		case model.SevError:
			errs++
		case model.SevWarn:
			warns++
		}
	}
	kv := func(k string, v ...string) string { return stLabel.Render(k+": ") + strings.Join(v, "") }
	b := func(v any) string { return stBold.Render(fmt.Sprint(v)) }

	title := rep.Title
	if rep.Chart != "" {
		title = rep.Chart
	}
	if title == "" {
		title = s.Source
	} else if s.Source != "" && s.Source != title {
		title += stDim.Render(" (" + s.Source + ")")
	}
	view := b("total") + stDim.Render(" pods × replicas")
	if s.PerPod {
		view = b("per pod") + stDim.Render(" single replica")
	}
	right := []string{
		kv("Chart", stBold.Render(title)),
	}
	if s.watch != nil {
		right = append(right, kv("Watch", s.watchSummary()))
	}
	right = append(right,
		kv("Workloads", b(len(rep.Workloads)), ", ", stPods.Render(strconv.FormatInt(rep.Pods, 10)), " pods, ", b(rep.Containers), " containers"),
		kv("QoS", stQoSGuaranteed.Render(fmt.Sprint(gua)), " guaranteed, ", stQoSBurstable.Render(fmt.Sprint(bur)), " burstable, ", stQoSBestEffort.Render(fmt.Sprint(be)), " best-effort"),
	)
	issues := stReq.Render("none")
	if errs+warns > 0 {
		issues = stErr.Render(fmt.Sprint(errs)) + " errors, " + stWarn.Render(fmt.Sprint(warns)) + " warnings"
	}
	right = append(right, kv("Issues", issues))
	if s.editing != nil {
		right = append(right, kv("Editing", s.editing.summary()))
	} else {
		right = append(right, kv("View", view))
	}
	if rep.HasHPA && !s.PerPod {
		right = append(right, kv("HPA max", "cpu "+b(units.Cores(rep.TotalMax.CPU.Req.Value)), " / mem "+b(memFmt(rep.TotalMax.Memory.Req)(rep.TotalMax.Memory.Req.Value)), stDim.Render(" requested")))
	}
	if len(tot.Extended) > 0 {
		keys := make([]string, 0, len(tot.Extended))
		for k := range tot.Extended {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+" "+b(units.Plain(tot.Extended[k].Req.Value)))
		}
		right = append(right, kv("Extended", strings.Join(parts, ", ")))
	}
	if s.Nodes > 1 {
		right = append(right, kv("DaemonSets", "× "+b(s.Nodes)+" nodes"))
	}

	// htop puts meters on the left and summary text on the right; on narrow
	// terminals the two columns are stacked instead.
	if width >= 96 {
		lw := width / 2
		if width >= 120 {
			lw = width * 11 / 20
		}
		rw := width - lw - 2
		n := max(meters, len(right))
		out := make([]string, n)
		for i := range n {
			l, r := "", ""
			if i < meters {
				l = s.meterLine(i, tot, lw-2)
			}
			if i < len(right) {
				r = right[i]
			}
			out[i] = " " + fitL(l, lw-1) + "  " + fitL(r, rw)
		}
		return out
	}
	var out []string
	for i := range meters {
		out = append(out, " "+s.meterLine(i, tot, width-2))
	}
	// Pack summary items side by side to keep the table visible.
	sep := stDim.Render("  │  ")
	line := ""
	for _, r := range right {
		switch {
		case line == "":
			line = r
		case ansi.StringWidth(line+sep+r) <= width-2:
			line += sep + r
		default:
			out = append(out, " "+fitL(line, width-1))
			line = r
		}
	}
	if line != "" {
		out = append(out, " "+fitL(line, width-1))
	}
	return out
}

func (s *State) meterLine(i int, tot model.Resources, w int) string {
	switch i {
	case 0:
		return meter("CPU", tot.CPU, s.Capacity.CPU, cpuFmt, " cores", w)
	case 1:
		return meter("Mem", tot.Memory, s.Capacity.Memory, memFmt, "", w)
	}
	return meter("Eph", tot.Ephemeral, 0, memFmt, "", w)
}

// ---------------------------------------------------------------- table ---

type column struct {
	title string
	width int
	right bool
	sort  SortKey
}

func (s *State) columns(l layout) []column {
	var cols []column
	if l.showKinds {
		cols = append(cols, column{"KIND", l.kind, false, SortKind})
	}
	cols = append(cols,
		column{"NAME", l.name, false, SortName},
		column{"PODS", l.pods, true, SortPods},
		column{"QOS", l.qos, false, -1},
		column{"CPU REQ", l.num, true, SortCPUReq},
		column{"CPU LIM", l.num, true, SortCPULim},
		column{"MEM REQ", l.num, true, SortMemReq},
		column{"MEM LIM", l.num, true, SortMemLim},
	)
	if l.showBars {
		cols = append(cols, column{"CPU", l.bar, false, -1}, column{"MEMORY", l.bar, false, -1})
	}
	return cols
}

func (s *State) tableHeader(l layout) string {
	var b strings.Builder
	b.WriteString(stHeader.Render(" "))
	for _, c := range s.columns(l) {
		title := c.title
		st := stHeader
		if c.sort == s.Sort {
			st = stHeaderSorted
			arrow := "▲"
			if s.numericSort() != s.Invert {
				arrow = "▼"
			}
			title += arrow
		}
		cell := fitL(title, c.width)
		if c.right {
			cell = fitR(title, c.width)
		}
		b.WriteString(st.Render(cell))
		b.WriteString(stHeader.Render(" "))
	}
	return fitBg(b.String(), l.width, stHeader)
}

// fitBg pads a styled line to width using the style's background.
func fitBg(line string, width int, st lipgloss.Style) string {
	w := ansi.StringWidth(line)
	if w > width {
		return ansi.Truncate(line, width, "")
	}
	return line + st.Render(strings.Repeat(" ", width-w))
}

// scale is the largest value any bar in a column has to represent.
func (s *State) scale(rows []row) (cpu, mem int64) {
	for _, r := range rows {
		if r.c != nil {
			continue
		}
		res := s.res(r)
		cpu = max(cpu, res.CPU.Req.Value, res.CPU.Lim.Value)
		mem = max(mem, res.Memory.Req.Value, res.Memory.Lim.Value)
	}
	return
}

// renderRow renders one table row. A selected row is rendered without inner
// colors so the selection bar reads as one solid line, like htop.
func (s *State) renderRow(r row, l layout, cpuScale, memScale int64, selected bool) string {
	plain := selected
	st := func(style lipgloss.Style, text string) string {
		if plain {
			return text
		}
		return style.Render(text)
	}
	res := s.res(r)
	var parts []string
	add := func(text string) { parts = append(parts, text) }

	if l.showKinds {
		k := ""
		if r.c == nil {
			k = shortKind(r.w.Kind)
		}
		add(st(stKind, fitL(k, l.kind)))
	}
	if r.c == nil {
		add(st(stWorkload, fitL(r.w.Name, l.name)))
		if s.editing != nil {
			add(s.editing.podsCell(r, l.pods))
		} else {
			add(st(stPods, fitR(podsText(r.w), l.pods)))
		}
		q, qs := qosText(r.w.QoS)
		add(st(qs, fitL(q, l.qos)))
	} else {
		glyph := " ├─ "
		if r.last {
			glyph = " └─ "
		}
		tag := roleTag(r.c)
		name := ansi.Truncate(r.c.Name, l.name-4-len(tag), "…")
		cell := st(stTree, glyph) + name
		if tag != "" {
			cell += st(stRole, tag)
		}
		add(fitL(cell, l.name))
		add(strings.Repeat(" ", l.pods))
		add(strings.Repeat(" ", l.qos))
	}
	col := 0
	num := func(q model.Quantity, lim bool, f fmtFn) {
		if s.editing != nil && r.c != nil {
			add(s.editing.cell(r, col, l.num))
			col++
			return
		}
		text, style := qty(q, lim, f)
		add(st(style, fitR(text, l.num)))
		col++
	}
	num(res.CPU.Req, false, units.CPU)
	num(res.CPU.Lim, true, units.CPU)
	num(res.Memory.Req, false, memFmt(res.Memory.Req))
	num(res.Memory.Lim, true, memFmt(res.Memory.Lim))
	if l.showBars {
		add(bracketed(renderCells(fillBar(res.CPU, cpuScale, l.bar-2), plain), plain))
		add(bracketed(renderCells(fillBar(res.Memory, memScale, l.bar-2), plain), plain))
	}
	// The gutter marks rows that changed in the latest live reload.
	gutter := " "
	switch {
	case s.editing != nil && s.editing.pending(r):
		gutter = st(stChanged, "✎")
	case s.recentlyChanged(r):
		gutter = st(stChanged, "●")
	}
	line := gutter + strings.Join(parts, " ")
	if selected {
		return stSelected.Render(fitL(line, l.width))
	}
	return fitL(line, l.width)
}

func (s *State) totalRow(l layout) string {
	tot := s.total()
	var pods int64
	for _, w := range s.shown().Workloads {
		if s.PerPod {
			pods++
		} else {
			pods += w.Replicas
		}
	}
	var parts []string
	label := "TOTAL"
	if s.PerPod {
		label = "TOTAL (1 pod each)"
	}
	nameW := l.name
	if l.showKinds {
		nameW += l.kind + 1
	}
	parts = append(parts, stTotal.Render(fitL(label, nameW)), stPods.Render(fitR(strconv.FormatInt(pods, 10), l.pods)), strings.Repeat(" ", l.qos))
	num := func(q model.Quantity, lim bool, f fmtFn) {
		text, style := qty(q, lim, f)
		parts = append(parts, style.Bold(true).Render(fitR(text, l.num)))
	}
	num(tot.CPU.Req, false, units.CPU)
	num(tot.CPU.Lim, true, units.CPU)
	num(tot.Memory.Req, false, memFmt(tot.Memory.Req))
	num(tot.Memory.Lim, true, memFmt(tot.Memory.Lim))
	if l.showBars {
		parts = append(parts, legend(l.bar*2+1))
	}
	return " " + strings.Join(parts, " ")
}

func legend(w int) string {
	full := stReq.Render("|") + " request  " + stLim.Render(":") + " limit  " + stUnbounded.Render("»") + " no limit  " +
		stPartial.Render("+") + " partial  " + stMissing.Render("—") + " unset"
	if ansi.StringWidth(full) > w {
		full = stReq.Render("|") + " req " + stLim.Render(":") + " lim " + stUnbounded.Render("»") + " unbounded"
	}
	return stDim.Render(fitR("", w-ansi.StringWidth(full))) + full
}

func rule(width int) string {
	return stRule.Render(strings.Repeat("─", max(0, width)))
}
