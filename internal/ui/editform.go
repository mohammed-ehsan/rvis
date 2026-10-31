package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mohammed-ehsan/rvis/internal/edit"
	"github.com/mohammed-ehsan/rvis/internal/model"
)

// Editor writes resource changes back to the chart's files.
type Editor interface {
	Locate(edit.Target) (map[edit.Field]edit.Result, error)
	Apply(edit.Target, map[edit.Field]string) ([]string, error)
	ApplyAll(map[edit.Target]map[edit.Field]string) ([]string, error)
}

// editColumns are the form's columns, in the same order as the table.
var editColumns = []struct {
	title           string
	group, resource string
}{
	{"CPU REQ", "requests", "cpu"},
	{"CPU LIM", "limits", "cpu"},
	{"MEM REQ", "requests", "memory"},
	{"MEM LIM", "limits", "memory"},
}

type editCell struct {
	field edit.Field
	orig  string // rendered value; "" when unset
	value string
	fresh bool // the next keystroke replaces the value
}

func (c *editCell) changed() bool { return c.value != c.orig }

type editForm struct {
	target   edit.Target
	title    string
	w        *model.Workload
	replicas *editCell     // the replica count; row -1
	cells    [][]*editCell // [container][column]
	row, col int
	locating bool
	results  map[edit.Field]edit.Result
	err      string
	saving   bool
}

type locatedMsg struct {
	results map[edit.Field]edit.Result
	err     error
}

type savedMsg struct {
	summary []string
	err     error
}

func newEditForm(w *model.Workload, focus *model.Container) *editForm {
	f := &editForm{
		target:   edit.Target{Kind: w.Kind, Name: w.Name, Namespace: w.Namespace},
		title:    w.Kind + "/" + w.Name,
		w:        w,
		locating: true,
	}
	r := replicaText(w)
	f.replicas = &editCell{field: edit.ReplicasField, orig: r, value: r, fresh: true}
	// Focus the selected container, or else the first app container.
	f.row = -1
	for i, c := range w.Containers {
		if c == focus || (focus == nil && f.row < 0 && c.Role == model.RoleApp) {
			f.row = i
		}
		row := make([]*editCell, len(editColumns))
		for j, col := range editColumns {
			v := c.Raw[col.group+"."+col.resource]
			row[j] = &editCell{field: edit.Field{Container: c.Name, Group: col.group, Resource: col.resource}, orig: v, value: v, fresh: true}
		}
		f.cells = append(f.cells, row)
	}
	f.row = max(f.row, 0)
	return f
}

func (f *editForm) cur() *editCell {
	if f.row < 0 {
		return f.replicas
	}
	return f.cells[f.row][f.col]
}

// move moves the focus; the replica row (-1) sits above the containers.
func (f *editForm) move(dr, dc int) {
	n := len(f.cells) + 1
	f.row = (f.row+1+dr+n)%n - 1
	if f.row >= 0 {
		f.col = (f.col + dc + len(editColumns)) % len(editColumns)
	}
	f.cur().fresh = true
}

func (f *editForm) replicasEditable() (bool, string) {
	src, reason := f.w.ReplicaSource()
	return src != model.ReplicasNotEditable, reason
}

func (f *editForm) changes() map[edit.Field]string {
	out := map[edit.Field]string{}
	if f.replicas.changed() {
		out[edit.ReplicasField] = strings.TrimSpace(f.replicas.value)
	}
	for _, row := range f.cells {
		for _, c := range row {
			if c.changed() {
				out[c.field] = strings.TrimSpace(c.value)
			}
		}
	}
	return out
}

// validate checks every changed value and that no request exceeds its limit.
func (f *editForm) validate() string {
	if c := f.replicas; c.changed() {
		key := model.Override{Workload: model.WorkloadKey(f.w), Field: model.ReplicasField}
		if _, errs := (&model.Report{Workloads: []*model.Workload{f.w}}).Preview(map[model.Override]string{key: c.value}); errs[key] != nil {
			return "replicas: " + errs[key].Error()
		}
		if r, ok := f.results[edit.ReplicasField]; ok && r.Loc == nil && !f.locating {
			return "replicas cannot be edited: " + r.Reason
		}
	}
	for _, row := range f.cells {
		parsed := map[string]resource.Quantity{}
		for _, c := range row {
			v := strings.TrimSpace(c.value)
			if v == "" {
				if c.changed() {
					return fmt.Sprintf("%s %s: removing a value is not supported; enter a quantity", c.field.Container, c.field.Group+"."+c.field.Resource)
				}
				continue
			}
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Sprintf("%s %s: %q is not a quantity (examples: 250m, 1.5, 512Mi, 2Gi)", c.field.Container, c.field.Group+"."+c.field.Resource, v)
			}
			parsed[c.field.Group+"."+c.field.Resource] = q
			if c.changed() && !f.locating {
				if r, ok := f.results[c.field]; ok && r.Loc == nil {
					return fmt.Sprintf("%s %s cannot be edited: %s", c.field.Container, c.field.Group+"."+c.field.Resource, r.Reason)
				}
			}
		}
		for _, r := range edit.Resources {
			req, okR := parsed["requests."+r]
			lim, okL := parsed["limits."+r]
			if okR && okL && req.Cmp(lim) > 0 {
				return fmt.Sprintf("%s: %s request %s is above its limit %s", row[0].field.Container, r, req.String(), lim.String())
			}
		}
	}
	return ""
}

// key handles a keystroke and returns a command to run, if any.
func (m *tuiModel) editKey(msg tea.KeyMsg) tea.Cmd {
	f := m.form
	if f.saving {
		return nil
	}
	c := f.cur()
	switch msg.Type {
	case tea.KeyEsc:
		m.mode, m.form = modeTable, nil
		return nil
	case tea.KeyEnter:
		changes := f.changes()
		if len(changes) == 0 {
			m.mode, m.form = modeTable, nil
			return nil
		}
		if f.locating {
			f.err = "still locating the values in the chart, try again in a moment"
			return nil
		}
		if f.err = f.validate(); f.err != "" {
			return nil
		}
		f.saving = true
		target, ed := f.target, m.editor
		return func() tea.Msg {
			summary, err := ed.Apply(target, changes)
			return savedMsg{summary, err}
		}
	case tea.KeyTab, tea.KeyRight:
		f.move(0, 1)
	case tea.KeyShiftTab, tea.KeyLeft:
		f.move(0, -1)
	case tea.KeyUp:
		f.move(-1, 0)
	case tea.KeyDown:
		f.move(1, 0)
	case tea.KeyBackspace:
		if r := []rune(c.value); len(r) > 0 && !c.fresh {
			c.value = string(r[:len(r)-1])
		} else if c.fresh {
			c.value = ""
		}
		c.fresh = false
	case tea.KeyCtrlU:
		c.value, c.fresh = "", false
	case tea.KeyCtrlZ:
		c.value, c.fresh = c.orig, true
	case tea.KeyRunes:
		if ok, _ := f.replicasEditable(); f.row < 0 && !ok {
			return nil
		}
		if c.fresh {
			c.value = ""
		}
		c.value += string(msg.Runes)
		c.fresh = false
	}
	f.err = ""
	return nil
}

func (m *tuiModel) openEditor(r row) tea.Cmd {
	if m.editor == nil {
		return m.flashMsg("can't edit: " + m.editUnavailable)
	}
	m.form = newEditForm(r.w, r.c)
	m.mode = modeEdit
	target, ed := m.form.target, m.editor
	return func() tea.Msg {
		res, err := ed.Locate(target)
		return locatedMsg{res, err}
	}
}

type clearFlashMsg struct{}

// flashMsg shows a status message in the footer and returns the command
// that redraws the screen once it expires.
func (m *tuiModel) flashMsg(s string) tea.Cmd {
	m.flash, m.flashAt = s, time.Now()
	return tea.Tick(flashTTL, func(time.Time) tea.Msg { return clearFlashMsg{} })
}

const flashTTL = 8 * time.Second

// editLines renders the form inside the panel box.
func (m *tuiModel) editLines(width int) []string {
	f := m.form
	const cellW = 11
	nameW := 9
	for _, row := range f.cells {
		nameW = max(nameW, ansi.StringWidth(row[0].field.Container))
	}
	nameW = min(nameW, max(10, width-2-len(editColumns)*(cellW+2)))

	out := []string{stTitle.Render("Edit resources") + "  " + stBold.Render(f.title), ""}

	// The replica count, above the containers.
	{
		c := f.replicas
		text, st := c.value, stPods
		switch {
		case c.changed():
			st = stChanged
		case text == "":
			text, st = podsText(f.w), stDim
		}
		if ok, _ := f.replicasEditable(); !ok {
			st = stDim
		}
		cell := st.Render(fitL(text, cellW))
		if f.row < 0 {
			cell = stSelected.Render(fitL(text+"▏", cellW))
		}
		out = append(out, " "+stLabelBold.Render(fitL("REPLICAS", nameW))+"  "+cell+"  "+stDim.Render(replicaLabel(f.w)), "")
	}
	head := " " + fitL("CONTAINER", nameW)
	for _, c := range editColumns {
		head += "  " + fitL(c.title, cellW)
	}
	out = append(out, stHeader.Render(head+" "))

	for i, row := range f.cells {
		line := " " + fitL(row[0].field.Container, nameW)
		for j, c := range row {
			text := c.value
			focused := i == f.row && j == f.col
			if focused {
				text += "▏"
			}
			st := stBase
			switch {
			case text == "":
				text = "—"
				st = stDim
			case c.changed():
				st = stChanged
			}
			if r, ok := f.results[c.field]; ok && r.Loc == nil && !c.changed() {
				st = stDim
			}
			cell := fitL(text, cellW)
			if focused {
				cell = stSelected.Render(cell)
			} else {
				cell = st.Render(cell)
			}
			line += "  " + cell
		}
		out = append(out, line)
	}
	out = append(out, "")

	c := f.cur()
	label := fmt.Sprintf("%s · %s %s", c.field.Container, c.field.Resource, strings.TrimSuffix(c.field.Group, "s"))
	editable, why := true, ""
	if f.row < 0 {
		label = "replicas · " + replicaLabel(f.w)
		editable, why = f.replicasEditable()
	}
	if c.changed() {
		was := c.orig
		if was == "" {
			was = "unset"
		}
		label += stDim.Render("  (was " + was + ")")
	}
	out = append(out, " "+stBold.Render(label))
	switch r, ok := f.results[c.field]; {
	case !editable:
		out = append(out, " "+stWarn.Render("not editable: ")+why)
	case f.locating:
		out = append(out, stDim.Render(" locating where this value is defined…"))
	case !ok:
		out = append(out, stDim.Render(" (no information)"))
	case r.Loc == nil:
		out = append(out, " "+stWarn.Render("not editable: ")+r.Reason)
	default:
		l := r.Loc
		out = append(out, " "+stLabel.Render("file   ")+fmt.Sprintf("%s:%d", l.File, l.Line))
		switch {
		case l.Insert:
			out = append(out, " "+stLabel.Render("adds   ")+"a new key under "+stBold.Render(l.Path))
		case l.Path != "":
			out = append(out, " "+stLabel.Render("key    ")+stBold.Render(l.Path))
		default:
			out = append(out, " "+stLabel.Render("key    ")+"literal value in the template")
		}
		if len(r.Loc.Shared) > 0 {
			out = append(out, " "+stWarn.Render("▲ shared value, also sets: ")+strings.Join(r.Loc.Shared, ", "))
		}
	}

	n := len(f.changes())
	out = append(out, "")
	switch {
	case f.saving:
		out = append(out, " "+stLabel.Render("saving…"))
	case f.err != "":
		for i, l := range wrap(f.err, width-4) {
			prefix := "   "
			if i == 0 {
				prefix = " ✖ "
			}
			out = append(out, stErr.Render(prefix)+l)
		}
	case n > 0:
		out = append(out, " "+stChanged.Render(plural(n, "change"))+" pending · Enter writes them to the chart files")
	default:
		out = append(out, stDim.Render(" Values are per container. Type a quantity such as 250m, 1.5, 512Mi or 2Gi."))
	}
	return out
}
