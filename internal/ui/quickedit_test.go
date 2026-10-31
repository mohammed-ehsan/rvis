package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/edit"
)

func ctrl(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func quickModel(t *testing.T, ed Editor, w, h int) *tuiModel {
	t.Helper()
	m := &tuiModel{st: demoState(t), editor: ed}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	keys(m, runes("E"))
	if m.mode != modeQuick {
		t.Fatal("quick edit did not open")
	}
	return m
}

func totalLine(m *tuiModel) string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "TOTAL") {
			return strings.Join(strings.Fields(l), " ")
		}
	}
	return ""
}

func TestQuickEditUpdatesTotalsLive(t *testing.T) {
	ed := &fakeEditor{}
	m := quickModel(t, ed, 140, 45)
	q := m.st.editing
	// Default sort is cpu-req: rvis-api first. The cursor starts on its
	// workload row (the replica count); one down is its first container.
	if q.focus.c != nil || q.focus.w.Name != "rvis-api" {
		t.Fatalf("initial focus = %+v", q.focus)
	}
	keys(m, ctrl(tea.KeyDown))
	if q.focus.c == nil || q.focus.w.Name != "rvis-api" || q.focus.c.Name != "migrate" {
		t.Fatalf("focus = %+v", q.focus)
	}
	order := func() []string {
		var out []string
		for _, r := range m.st.rows() {
			out = append(out, rowKey(r.w, r.c))
		}
		return out
	}
	before := order()
	if got := totalLine(m); !strings.Contains(got, "TOTAL 13 9.58+ 10.05+") {
		t.Fatalf("initial total = %q", got)
	}

	// migrate's cpu request dominates the api pod (1 core × 4 replicas).
	// Lowering it to 100m makes api 350m per pod: total −2.6 cores.
	keys(m, runes("1"), runes("0"), runes("0"), runes("m"))
	if got := totalLine(m); !strings.Contains(got, "TOTAL 13 6.98+") {
		t.Errorf("total after edit = %q", got)
	}
	frame := ansi.Strip(m.View())
	if !strings.Contains(frame, "1 change") || !strings.Contains(frame, "cpu −2.6") {
		t.Errorf("header does not summarize the change:\n%s", frame)
	}
	if strings.Join(order(), ",") != strings.Join(before, ",") {
		t.Error("rows moved while editing")
	}

	// Tab to the memory request of the same container, then down to api's
	// memory limit with the arrows.
	keys(m, ctrl(tea.KeyTab), ctrl(tea.KeyTab), runes("200Mi"))
	keys(m, ctrl(tea.KeyDown), ctrl(tea.KeyRight), runes("3Gi"))
	if len(q.values) != 3 {
		t.Fatalf("values = %v", q.values)
	}
	// Typing the original value back clears the change.
	keys(m, ctrl(tea.KeyCtrlU), runes("1Gi"))
	if len(q.values) != 2 {
		t.Errorf("restoring the original value should drop the change: %v", q.values)
	}

	keys(m, ctrl(tea.KeyCtrlS))
	if m.mode != modeTable || m.st.editing != nil {
		t.Fatalf("still editing: %s", q.status)
	}
	api := ed.all[edit.Target{Kind: "Deployment", Name: "rvis-api"}]
	if api[edit.Field{Container: "migrate", Group: "requests", Resource: "cpu"}] != "100m" ||
		api[edit.Field{Container: "migrate", Group: "requests", Resource: "memory"}] != "200Mi" || len(api) != 2 {
		t.Errorf("saved = %v", ed.all)
	}
	if !strings.Contains(ansi.Strip(m.View()), "saved 2 values in 1 file") {
		t.Error("no save confirmation")
	}
}

func TestQuickEditValidationAndDiscard(t *testing.T) {
	ed := &fakeEditor{}
	m := quickModel(t, ed, 120, 40)
	q := m.st.editing

	keys(m, ctrl(tea.KeyDown), runes("lots"))
	if !strings.Contains(ansi.Strip(m.View()), "is not a quantity") {
		t.Error("invalid value not flagged in the status line")
	}
	keys(m, ctrl(tea.KeyCtrlS))
	if m.mode != modeQuick || !strings.Contains(ansi.Strip(q.status), "not a quantity") || ed.all != nil {
		t.Fatalf("invalid save went through: %q", q.status)
	}

	// A request above its limit (migrate limit is 1 core).
	keys(m, ctrl(tea.KeyCtrlU), runes("3"), ctrl(tea.KeyCtrlS))
	if !strings.Contains(ansi.Strip(q.status), "request is above its limit") {
		t.Fatalf("status = %q", ansi.Strip(q.status))
	}

	// Esc asks before discarding, then discards.
	keys(m, ctrl(tea.KeyEsc))
	if m.mode != modeQuick || !strings.Contains(ansi.Strip(q.status), "Esc again discards") {
		t.Fatal("first Esc should ask")
	}
	keys(m, ctrl(tea.KeyEsc))
	if m.mode != modeTable || ed.all != nil {
		t.Error("second Esc should discard without saving")
	}
	if totalLine(m) != "TOTAL 13 9.58+ 10.05+ 13.1Gi+ 16.1Gi+ | req : lim » unbounded" {
		t.Errorf("totals not restored: %q", totalLine(m))
	}
}

func TestQuickEditShowsSources(t *testing.T) {
	m := quickModel(t, &fakeEditor{}, 140, 40)
	// Focus api's cpu limit; the fake editor locates it in values.yaml:11.
	for i, r := range m.st.rows() {
		if r.c != nil && r.c.Name == "api" {
			m.sel = i
		}
	}
	m.moveFocus(m.st.rows(), 0)
	keys(m, ctrl(tea.KeyRight))
	if s := ansi.Strip(m.View()); !strings.Contains(s, "values.yaml:11 api.resources.limits.cpu") || !strings.Contains(s, "shared with 1 more") {
		t.Errorf("status line lacks the source:\n%s", s)
	}
}

func TestQuickEditFitsTerminal(t *testing.T) {
	for _, sz := range [][2]int{{40, 12}, {80, 24}, {120, 40}, {200, 60}} {
		m := quickModel(t, &fakeEditor{}, sz[0], sz[1])
		keys(m, runes("123456789012345"), ctrl(tea.KeyDown), runes("x"), ctrl(tea.KeyDown), runes("9"))
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > sz[0] {
				t.Errorf("%dx%d: line %d is %d wide", sz[0], sz[1], i, w)
			}
		}
	}
}

func focusWorkload(t *testing.T, m *tuiModel, name string) {
	t.Helper()
	for i, r := range m.st.rows() {
		if r.c == nil && r.w.Name == name {
			m.sel = i
			m.moveFocus(m.st.rows(), 0)
			return
		}
	}
	t.Fatalf("workload %s not found", name)
}

func TestQuickEditReplicas(t *testing.T) {
	ed := &fakeEditor{}
	m := quickModel(t, ed, 140, 45)
	q := m.st.editing

	// frontend: 3 → 6 replicas of a 110m pod: +330m and 3 more pods.
	focusWorkload(t, m, "rvis-frontend")
	keys(m, runes("6"))
	if got := totalLine(m); !strings.Contains(got, "TOTAL 16 9.91+") {
		t.Errorf("total after replicas edit = %q", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "rvis-frontend · replicas") {
		t.Error("status line does not name the replica field")
	}

	// api is autoscaled with min 4, max 12: the cell edits minReplicas.
	focusWorkload(t, m, "rvis-api")
	if s := ansi.Strip(m.View()); !strings.Contains(s, "autoscaler minReplicas (max 12)") {
		t.Errorf("api replica label missing:\n%s", s)
	}
	keys(m, runes("13"))
	if !strings.Contains(ansi.Strip(m.View()), "above the autoscaler's maxReplicas 12") {
		t.Error("minReplicas above max not flagged")
	}
	keys(m, ctrl(tea.KeyCtrlU), runes("5"))

	// A DaemonSet's count is not editable: typing is ignored, with a reason.
	focusWorkload(t, m, "rvis-log-agent")
	keys(m, runes("4"))
	if len(q.values) != 2 || !strings.Contains(ansi.Strip(m.View()), "one pod per node") {
		t.Errorf("daemonset edit: values=%v", q.values)
	}

	keys(m, ctrl(tea.KeyCtrlS))
	if m.mode != modeTable {
		t.Fatalf("still editing: %s", ansi.Strip(q.status))
	}
	if ed.all[edit.Target{Kind: "Deployment", Name: "rvis-frontend"}][edit.ReplicasField] != "6" ||
		ed.all[edit.Target{Kind: "Deployment", Name: "rvis-api"}][edit.ReplicasField] != "5" {
		t.Errorf("saved = %v", ed.all)
	}
}

func TestEditFormReplicas(t *testing.T) {
	ed := &fakeEditor{}
	m := editModel(t, ed)
	keys(m, runes("e"))
	// The form opens on the api container; the replica row is above the
	// first container.
	for i := 0; i < 5 && m.form.row != -1; i++ {
		keys(m, ctrl(tea.KeyUp))
	}
	if m.form.row != -1 {
		t.Fatalf("row = %d, want the replica row", m.form.row)
	}
	if s := ansi.Strip(m.View()); !strings.Contains(s, "REPLICAS") || !strings.Contains(s, "autoscaler minReplicas (max 12)") {
		t.Errorf("form lacks the replica row:\n%s", s)
	}
	keys(m, runes("0"), ctrl(tea.KeyEnter))
	if !strings.Contains(m.form.err, "at least 1") {
		t.Fatalf("minReplicas 0 accepted: %q", m.form.err)
	}
	keys(m, ctrl(tea.KeyCtrlU), runes("8"), ctrl(tea.KeyEnter))
	if m.mode != modeTable || ed.applied[edit.ReplicasField] != "8" {
		t.Errorf("applied = %v (mode %v)", ed.applied, m.mode)
	}
}
