package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mohammed-ehsan/rvis/internal/edit"
	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/units"
)

// quickEdit turns the whole table into a spreadsheet of container requests
// and limits. Every keystroke recomputes a preview report, so workload rows,
// totals and meters follow the typed values live. Ctrl+S writes everything
// in one pass.
type quickEdit struct {
	base    *model.Report // the saved chart
	preview *model.Report // base with values applied
	values  map[model.Override]string
	invalid map[model.Override]error
	order   map[string]int // frozen row order by row key
	col     int            // focused column: CPU REQ, CPU LIM, MEM REQ, MEM LIM
	fresh   bool           // the next keystroke replaces the cell

	located  map[string]map[edit.Field]edit.Result // by workload key
	locErr   map[string]string
	gen      int  // invalidates background locating from an earlier session
	inflight bool // one background locate at a time

	focus          row // focused container row, set by the model
	confirmDiscard bool
	status         string // save errors and prompts
	saving         bool

	prevFlat, prevPerPod bool
}

type quickLocatedMsg struct {
	gen     int
	key     string
	results map[edit.Field]edit.Result
	err     error
}

type quickSavedMsg struct {
	summary []string
	err     error
}

func newQuickEdit(st *State) *quickEdit {
	q := &quickEdit{
		base: st.Report, values: map[model.Override]string{}, fresh: true,
		located: map[string]map[edit.Field]edit.Result{}, locErr: map[string]string{},
		prevFlat: st.Flat, prevPerPod: st.PerPod,
	}
	q.recompute()
	return q
}

func (q *quickEdit) recompute() {
	q.preview, q.invalid = q.base.Preview(q.values)
}

// overrideKey is the value a cell edits: the replica count on a workload
// row, a request or limit on a container row.
func overrideKey(r row, col int) model.Override {
	if r.c == nil {
		return model.Override{Workload: model.WorkloadKey(r.w), Field: model.ReplicasField}
	}
	c := editColumns[col]
	return model.Override{Workload: model.WorkloadKey(r.w), Container: r.c.Name, Field: c.group + "." + c.resource}
}

func fieldOf(o model.Override) edit.Field {
	if o.Field == model.ReplicasField {
		return edit.ReplicasField
	}
	g, r, _ := strings.Cut(o.Field, ".")
	return edit.Field{Container: o.Container, Group: g, Resource: r}
}

// replicaText is the editable replica count of a workload as the chart
// sets it, or "" when there is none.
func replicaText(w *model.Workload) string {
	switch src, _ := w.ReplicaSource(); {
	case src == model.ReplicasHPAMin:
		return strconv.FormatInt(w.HPA.Min, 10)
	case src == model.ReplicasSpec && w.SpecSet:
		return strconv.FormatInt(w.Spec, 10)
	}
	return ""
}

// replicaLabel says which value a workload's replica cell edits.
func replicaLabel(w *model.Workload) string {
	switch src, _ := w.ReplicaSource(); src {
	case model.ReplicasHPAMin:
		return fmt.Sprintf("autoscaler minReplicas (max %d)", w.HPA.Max)
	case model.ReplicasSpec:
		if w.HPA != nil {
			return fmt.Sprintf("%s (autoscaled %d–%d)", w.ReplicaField(), w.HPA.Min, w.HPA.Max)
		}
		return w.ReplicaField()
	}
	return "replicas"
}

// arrange orders rows as they were when editing started.
func (q *quickEdit) arrange(rows []row) []row {
	key := func(r row) string { return rowKey(r.w, r.c) }
	if q.order == nil {
		q.order = map[string]int{}
		for i, r := range rows {
			q.order[key(r)] = i
		}
		return rows
	}
	pos := func(r row) int {
		if i, ok := q.order[key(r)]; ok {
			return i
		}
		return len(q.order)
	}
	sort.SliceStable(rows, func(i, j int) bool { return pos(rows[i]) < pos(rows[j]) })
	return rows
}

// value is the text of a cell: the pending value, or the chart's own.
func (q *quickEdit) value(r row, col int) (string, bool) {
	if v, ok := q.values[overrideKey(r, col)]; ok {
		return v, true
	}
	c := editColumns[col]
	for _, w := range q.base.Workloads {
		if model.WorkloadKey(w) != model.WorkloadKey(r.w) {
			continue
		}
		if r.c == nil {
			return replicaText(w), false
		}
		for _, bc := range w.Containers {
			if bc.Name == r.c.Name {
				return bc.Raw[c.group+"."+c.resource], false
			}
		}
	}
	return "", false
}

func (q *quickEdit) result(r row, col int) (edit.Result, bool) {
	res, ok := q.located[model.WorkloadKey(r.w)]
	if !ok {
		return edit.Result{}, false
	}
	v, ok := res[fieldOf(overrideKey(r, col))]
	return v, ok
}

// podsCell renders the replica cell of a workload row.
func (q *quickEdit) podsCell(r row, width int) string {
	text, changed := q.value(r, 0)
	focused := q.focus.c == nil && q.focus.w != nil && rowKey(q.focus.w, nil) == rowKey(r.w, nil)
	src, _ := r.w.ReplicaSource()
	st := stPods
	switch {
	case q.invalid[overrideKey(r, 0)] != nil:
		st = stErr
	case changed:
		st = stChanged
	case src == model.ReplicasNotEditable || text == "":
		text, st = podsText(r.w), stDim
	case src == model.ReplicasHPAMin:
		text += "↑"
	}
	if focused {
		if !changed && src == model.ReplicasHPAMin {
			text = strings.TrimSuffix(text, "↑")
		}
		return stSelected.Render(fitR(text+"▏", width))
	}
	return st.Render(fitR(text, width))
}

// cell renders one editable cell of a container row.
func (q *quickEdit) cell(r row, col, width int) string {
	text, changed := q.value(r, col)
	focused := r.c != nil && q.focus.c != nil && rowKey(r.w, r.c) == rowKey(q.focus.w, q.focus.c) && col == q.col
	c := editColumns[col]
	pair := r.c.Resources.CPU
	f := fmtFn(units.CPU)
	if c.resource == "memory" {
		pair = r.c.Resources.Memory
		f = memFmt(pair.Req)
	}
	st := stBase
	switch {
	case q.invalid[overrideKey(r, col)] != nil:
		st = stErr
	case changed:
		st = stChanged
	case text == "" && c.group == "requests" && pair.ReqFromLim:
		// Not set: Kubernetes uses the limit, shown dimmed.
		text, st = f(pair.Req.Value)+"*", stDim
	case text == "" && c.group == "limits":
		text, st = "∞", stUnbounded
	case text == "":
		text, st = "—", stMissing
	}
	if res, ok := q.result(r, col); ok && res.Loc == nil && !changed {
		st = stDim
	}
	if focused {
		if !changed && (text == "—" || text == "∞" || strings.HasSuffix(text, "*")) {
			text = ""
		}
		return stSelected.Render(fitR(text+"▏", width))
	}
	return st.Render(fitR(text, width))
}

// pending reports whether a row (or, for workload rows, any of its
// containers) has unsaved values.
func (q *quickEdit) pending(r row) bool {
	for o := range q.values {
		if o.Workload == model.WorkloadKey(r.w) && (r.c == nil || o.Container == r.c.Name) {
			return true
		}
	}
	return false
}

// set stores a cell's value; typing the chart's own value back clears it.
func (q *quickEdit) set(r row, col int, v string) {
	k := overrideKey(r, col)
	delete(q.values, k)
	if orig, _ := q.value(r, col); v != orig {
		q.values[k] = v
	}
	q.recompute()
	q.confirmDiscard, q.status = false, ""
}

func signed(v int64, f fmtFn) string {
	switch {
	case v > 0:
		return stLim.Render("+" + f(v))
	case v < 0:
		return stReq.Render("−" + f(-v))
	}
	return stDim.Render("±0")
}

// summary is the header line: pending changes and their effect on totals.
func (q *quickEdit) summary() string {
	if len(q.values) == 0 {
		return stBold.Render("quick edit") + stDim.Render(" · type to change a value, ^S saves")
	}
	b, p := q.base.Total, q.preview.Total
	return stChanged.Render(plural(len(q.values), "change")) + "  " +
		stDim.Render("cpu ") + signed(p.CPU.Req.Value-b.CPU.Req.Value, units.CPU) + stDim.Render("/") + signed(p.CPU.Lim.Value-b.CPU.Lim.Value, units.CPU) +
		stDim.Render("  mem ") + signed(p.Memory.Req.Value-b.Memory.Req.Value, memFmt(p.Memory.Req)) + stDim.Render("/") + signed(p.Memory.Lim.Value-b.Memory.Lim.Value, memFmt(p.Memory.Lim)) +
		stDim.Render(" req/lim")
}

// statusLine describes the focused cell: where it will be written, or why
// it cannot be, plus prompts and errors.
func (q *quickEdit) statusLine(width int) string {
	if q.saving {
		return " " + stLabel.Render("saving…")
	}
	if q.status != "" {
		return " " + q.status
	}
	r := q.focus
	if r.w == nil {
		return ""
	}
	var label string
	if r.c == nil {
		label = stBold.Render(r.w.Name+" · "+replicaLabel(r.w)) + " "
		if src, reason := r.w.ReplicaSource(); src == model.ReplicasNotEditable {
			return " " + label + stWarn.Render("not editable: ") + reason
		}
	} else {
		c := editColumns[q.col]
		label = stBold.Render(r.c.Name+" · "+c.resource+" "+strings.TrimSuffix(c.group, "s")) + " "
	}
	if err := q.invalid[overrideKey(r, q.col)]; err != nil {
		return " " + label + stErr.Render(err.Error())
	}
	if msg, ok := q.locErr[model.WorkloadKey(r.w)]; ok {
		return " " + label + stWarn.Render("cannot locate: ") + msg
	}
	if v, changed := q.value(r, q.col); v == "" && !changed {
		label += stDim.Render("not set in the chart; typing a value adds it · ")
	}
	res, ok := q.result(r, q.col)
	switch {
	case !ok:
		return " " + label + stDim.Render("locating where this value is defined…")
	case res.Loc == nil:
		return " " + label + stWarn.Render("not editable: ") + res.Reason
	}
	l := res.Loc
	where := fmt.Sprintf("%s:%d", l.File, l.Line)
	switch {
	case l.Insert:
		where += stDim.Render(" adds a key under ") + l.Path
	case l.Path != "":
		where += " " + stDim.Render(l.Path)
	default:
		where += stDim.Render(" template literal")
	}
	if len(l.Shared) > 0 {
		where += "  " + stWarn.Render(fmt.Sprintf("▲ shared with %d more", len(l.Shared)))
	}
	return " " + label + stLabel.Render("→ ") + where
}

// ------------------------------------------------------------ the model --

func (m *tuiModel) startQuickEdit() tea.Cmd {
	if m.editor == nil {
		return m.flashMsg("can't edit: " + m.editUnavailable)
	}
	q := newQuickEdit(m.st)
	m.st.Flat, m.st.PerPod = false, false
	m.st.editing = q
	m.mode = modeQuick
	// Start on the selected container, or the first container below.
	rows := m.st.rows()
	m.moveFocus(rows, 0)
	q.gen = m.quickGen + 1
	m.quickGen = q.gen
	return m.locateNext()
}

func (m *tuiModel) stopQuickEdit() {
	q := m.st.editing
	m.st.Flat, m.st.PerPod = q.prevFlat, q.prevPerPod
	m.st.editing = nil
	m.mode = modeTable
	m.clamp(len(m.st.rows()))
}

// moveFocus moves the selection dir rows. Workload rows edit the replica
// count, container rows their requests and limits.
func (m *tuiModel) moveFocus(rows []row, dir int) {
	if len(rows) == 0 {
		return
	}
	m.sel = min(max(m.sel+dir, 0), len(rows)-1)
	m.clamp(len(rows))
	m.st.editing.focus = rows[m.sel]
	m.st.editing.fresh = true
}

// locateNext locates the next workload in the background, starting with
// the focused one, so the status line can show where each value lives.
func (m *tuiModel) locateNext() tea.Cmd {
	q := m.st.editing
	if q == nil || m.editor == nil || q.inflight {
		return nil
	}
	var next *model.Workload
	if f := q.focus.w; f != nil && !q.isLocated(f) {
		next = f
	}
	if next == nil {
		for _, w := range q.base.Workloads {
			if !q.isLocated(w) {
				next = w
				break
			}
		}
	}
	if next == nil {
		return nil
	}
	q.inflight = true
	gen, ed, key := q.gen, m.editor, model.WorkloadKey(next)
	target := edit.Target{Kind: next.Kind, Name: next.Name, Namespace: next.Namespace}
	return func() tea.Msg {
		res, err := ed.Locate(target)
		return quickLocatedMsg{gen: gen, key: key, results: res, err: err}
	}
}

func (q *quickEdit) isLocated(w *model.Workload) bool {
	k := model.WorkloadKey(w)
	_, ok := q.located[k]
	_, failed := q.locErr[k]
	return ok || failed
}

func (m *tuiModel) quickLocated(msg quickLocatedMsg) tea.Cmd {
	q := m.st.editing
	if q == nil || msg.gen != q.gen {
		return nil
	}
	q.inflight = false
	if msg.err != nil {
		q.locErr[msg.key] = msg.err.Error()
	} else {
		q.located[msg.key] = msg.results
	}
	return m.locateNext()
}

// validate checks every pending value before saving and moves the focus to
// the first problem.
func (m *tuiModel) quickValidate(rows []row) string {
	q := m.st.editing
	focusOn := func(o model.Override) {
		for i, r := range rows {
			if model.WorkloadKey(r.w) != o.Workload {
				continue
			}
			if (o.Field == model.ReplicasField && r.c == nil) || (r.c != nil && r.c.Name == o.Container) {
				m.sel = i
				for j, c := range editColumns {
					if c.group+"."+c.resource == o.Field {
						q.col = j
					}
				}
			}
		}
		m.clamp(len(rows))
		q.focus = rows[m.sel]
	}
	keys := make([]model.Override, 0, len(q.values))
	for o := range q.values {
		keys = append(keys, o)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, o := range keys {
		who := o.Container + " " + o.Field
		if o.Field == model.ReplicasField {
			who = strings.Replace(o.Workload, "//", "/", 1) + " replicas"
		}
		if err := q.invalid[o]; err != nil {
			focusOn(o)
			return fmt.Sprintf("%s: %v", who, err)
		}
		if strings.TrimSpace(q.values[o]) == "" {
			focusOn(o)
			return who + ": removing a value is not supported; enter a value"
		}
		if res, ok := q.located[o.Workload]; ok {
			if r, ok := res[fieldOf(o)]; ok && r.Loc == nil {
				focusOn(o)
				return fmt.Sprintf("%s cannot be edited: %s", who, r.Reason)
			}
		}
	}
	for _, w := range q.preview.Workloads {
		for _, c := range w.Containers {
			for _, p := range []struct {
				name string
				pair model.Pair
			}{{"cpu", c.Resources.CPU}, {"memory", c.Resources.Memory}} {
				if p.pair.Req.Set() && p.pair.Lim.Set() && p.pair.Req.Value > p.pair.Lim.Value {
					o := model.Override{Workload: model.WorkloadKey(w), Container: c.Name, Field: "requests." + p.name}
					focusOn(o)
					return fmt.Sprintf("%s/%s %s: %s request is above its limit", w.Kind, w.Name, c.Name, p.name)
				}
			}
		}
	}
	return ""
}

func (m *tuiModel) quickKey(msg tea.KeyMsg) tea.Cmd {
	q := m.st.editing
	if q.saving {
		return nil
	}
	rows := m.st.rows()
	if m.sel >= len(rows) {
		return nil
	}
	r := rows[m.sel]
	if r.c == nil {
		if src, _ := r.w.ReplicaSource(); src == model.ReplicasNotEditable {
			switch msg.Type {
			case tea.KeyRunes, tea.KeyBackspace, tea.KeyCtrlU:
				return nil // the status line says why
			}
		}
	}
	switch msg.Type {
	case tea.KeyEsc:
		if len(q.values) > 0 && !q.confirmDiscard {
			q.confirmDiscard = true
			q.status = stWarn.Render(fmt.Sprintf("Esc again discards %s · ^S saves them", plural(len(q.values), "change")))
			return nil
		}
		m.stopQuickEdit()
		return nil
	case tea.KeyCtrlS:
		if len(q.values) == 0 {
			m.stopQuickEdit()
			return nil
		}
		if err := m.quickValidate(rows); err != "" {
			q.status = stErr.Render("✖ ") + err
			return nil
		}
		changes := map[edit.Target]map[edit.Field]string{}
		for o, v := range q.values {
			for _, w := range q.base.Workloads {
				if model.WorkloadKey(w) == o.Workload {
					t := edit.Target{Kind: w.Kind, Name: w.Name, Namespace: w.Namespace}
					if changes[t] == nil {
						changes[t] = map[edit.Field]string{}
					}
					changes[t][fieldOf(o)] = strings.TrimSpace(v)
				}
			}
		}
		q.saving, q.status = true, ""
		ed := m.editor
		return func() tea.Msg {
			summary, err := ed.ApplyAll(changes)
			return quickSavedMsg{summary, err}
		}
	case tea.KeyUp:
		m.moveFocus(rows, -1)
	case tea.KeyDown, tea.KeyEnter:
		m.moveFocus(rows, 1)
	case tea.KeyPgUp:
		m.sel = max(0, m.sel-m.bodyHeight())
		m.moveFocus(rows, 0)
	case tea.KeyPgDown:
		m.sel = min(len(rows)-1, m.sel+m.bodyHeight())
		m.moveFocus(rows, 0)
	case tea.KeyRight:
		if r.c != nil {
			q.col, q.fresh = (q.col+1)%len(editColumns), true
		}
	case tea.KeyLeft:
		if r.c != nil {
			q.col, q.fresh = (q.col+len(editColumns)-1)%len(editColumns), true
		}
	case tea.KeyTab:
		// Spreadsheet order: across the row, then down. A workload row has
		// a single cell, its replica count.
		if r.c == nil {
			q.col = 0
			m.moveFocus(rows, 1)
		} else if q.col == len(editColumns)-1 {
			q.col = 0
			m.moveFocus(rows, 1)
		} else {
			q.col, q.fresh = q.col+1, true
		}
	case tea.KeyShiftTab:
		if r.c == nil {
			q.col = len(editColumns) - 1
			m.moveFocus(rows, -1)
		} else if q.col == 0 {
			q.col = len(editColumns) - 1
			m.moveFocus(rows, -1)
		} else {
			q.col, q.fresh = q.col-1, true
		}
	case tea.KeyBackspace:
		v, _ := q.value(r, q.col)
		if q.fresh {
			v = ""
		} else if rs := []rune(v); len(rs) > 0 {
			v = string(rs[:len(rs)-1])
		}
		q.fresh = false
		q.set(r, q.col, v)
	case tea.KeyCtrlU:
		q.fresh = false
		q.set(r, q.col, "")
	case tea.KeyCtrlZ:
		delete(q.values, overrideKey(r, q.col))
		q.recompute()
		q.fresh = true
	case tea.KeyRunes:
		v, _ := q.value(r, q.col)
		if q.fresh {
			v = ""
		}
		q.fresh = false
		q.set(r, q.col, v+string(msg.Runes))
	}
	return m.locateNext()
}

func (m *tuiModel) quickSaved(msg quickSavedMsg) tea.Cmd {
	q := m.st.editing
	if q == nil {
		return nil
	}
	q.saving = false
	if msg.err != nil {
		q.status = stErr.Render("✖ ") + msg.err.Error()
		return nil
	}
	m.stopQuickEdit()
	files := map[string]bool{}
	for _, s := range msg.summary {
		f, _, _ := strings.Cut(s, ":")
		files[f] = true
	}
	cmds := []tea.Cmd{m.flashMsg(fmt.Sprintf("✔ saved %s in %s", plural(len(msg.summary), "value"), plural(len(files), "file")))}
	if m.watcher == nil && m.reload != nil {
		reload := m.reload
		cmds = append(cmds, func() tea.Msg {
			rep, err := reload()
			return reloadMsg{rep: rep, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// rebase applies a live reload under pending edits: the new chart becomes
// the base, pending values stay, and sources are located again.
func (m *tuiModel) rebaseQuickEdit() tea.Cmd {
	q := m.st.editing
	q.base = m.st.Report
	q.recompute()
	q.located, q.locErr = map[string]map[edit.Field]edit.Result{}, map[string]string{}
	q.inflight = false
	m.quickGen++
	q.gen = m.quickGen
	return m.locateNext()
}
