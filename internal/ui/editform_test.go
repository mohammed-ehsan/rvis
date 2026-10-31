package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/edit"
)

type fakeEditor struct {
	applied map[edit.Field]string
	all     map[edit.Target]map[edit.Field]string
}

func (f *fakeEditor) Locate(t edit.Target) (map[edit.Field]edit.Result, error) {
	return map[edit.Field]edit.Result{
		{Container: "api", Group: "limits", Resource: "cpu"}:     {Loc: &edit.Location{File: "values.yaml", Line: 11, Path: "api.resources.limits.cpu", Shared: []string{"Deployment/x app limits.cpu"}}},
		{Container: "api", Group: "requests", Resource: "cpu"}:   {Loc: &edit.Location{File: "values.yaml", Line: 10, Path: "api.resources.requests.cpu"}},
		{Container: "migrate", Group: "limits", Resource: "cpu"}: {Reason: "computed in a template"},
	}, nil
}

func (f *fakeEditor) ApplyAll(changes map[edit.Target]map[edit.Field]string) ([]string, error) {
	f.all = changes
	var out []string
	for t, fs := range changes {
		for fl, v := range fs {
			out = append(out, fmt.Sprintf("values.yaml:1  %s %s %s = %s", t.Name, fl.Container, fl.Group+"."+fl.Resource, v))
		}
	}
	return out, nil
}

func (f *fakeEditor) Apply(t edit.Target, changes map[edit.Field]string) ([]string, error) {
	f.applied = changes
	return []string{"values.yaml:11  api limits.cpu = 2"}, nil
}

// run executes a command and feeds its messages back, like Bubble Tea
// would. Timers (which would block for seconds) are skipped.
func run(m *tuiModel, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond):
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			run(m, c)
		}
		return
	}
	if msg != nil {
		_, next := m.Update(msg)
		run(m, next)
	}
}

func keys(m *tuiModel, ks ...tea.KeyMsg) {
	for _, k := range ks {
		_, cmd := m.Update(k)
		run(m, cmd)
	}
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func editModel(t *testing.T, ed Editor) *tuiModel {
	st := demoState(t)
	st.Sort = SortName
	m := &tuiModel{st: st, editor: ed}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	// Select the "api" container of rvis-api.
	for i, r := range st.rows() {
		if r.c != nil && r.c.Name == "api" {
			m.sel = i
		}
	}
	return m
}

func TestEditFormSavesChanges(t *testing.T) {
	ed := &fakeEditor{}
	m := editModel(t, ed)
	keys(m, runes("e"))
	if m.mode != modeEdit || m.form == nil || m.form.cur().field.Container != "api" {
		t.Fatalf("edit form did not open on the api container")
	}
	frame := ansi.Strip(m.View())
	for _, want := range []string{"Edit resources", "Deployment/rvis-api", "values.yaml:10", "api.resources.requests.cpu"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q", want)
		}
	}

	// Move to CPU LIM and type a new value: the first key replaces it.
	keys(m, tea.KeyMsg{Type: tea.KeyTab}, runes("2"))
	if c := m.form.cur(); c.value != "2" || !c.changed() {
		t.Fatalf("cell = %+v", c)
	}
	frame = ansi.Strip(m.View())
	if !strings.Contains(frame, "shared value, also sets: Deployment/x") || !strings.Contains(frame, "1 change pending") {
		t.Errorf("missing shared warning or pending count:\n%s", frame)
	}

	keys(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeTable {
		t.Fatalf("form still open: %s", m.form.err)
	}
	if got := ed.applied[edit.Field{Container: "api", Group: "limits", Resource: "cpu"}]; got != "2" || len(ed.applied) != 1 {
		t.Errorf("applied = %v", ed.applied)
	}
	if !strings.Contains(ansi.Strip(m.View()), "saved 1 change") {
		t.Error("footer does not confirm the save")
	}
}

func TestEditFormValidation(t *testing.T) {
	ed := &fakeEditor{}
	m := editModel(t, ed)
	keys(m, runes("e"))

	// Request above its limit (limit is 1 core) is refused.
	keys(m, runes("4"), tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeEdit || !strings.Contains(m.form.err, "above its limit") {
		t.Fatalf("expected request>limit error, got %q", m.form.err)
	}
	// Not a quantity.
	keys(m, tea.KeyMsg{Type: tea.KeyCtrlU}, runes("fast"), tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.form.err, "not a quantity") {
		t.Fatalf("expected quantity error, got %q", m.form.err)
	}
	// A field that cannot be located.
	keys(m, tea.KeyMsg{Type: tea.KeyCtrlZ})
	m.form.row, m.form.col = 0, 1 // migrate, CPU LIM
	keys(m, runes("3"), tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.form.err, "cannot be edited: computed in a template") {
		t.Fatalf("expected not-editable error, got %q", m.form.err)
	}
	if ed.applied != nil {
		t.Error("nothing should have been written")
	}
	keys(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeTable || m.form != nil {
		t.Error("Esc should cancel the form")
	}
}

func TestEditUnavailable(t *testing.T) {
	m := editModel(t, nil)
	m.editUnavailable = "editing needs a local chart directory, not an installed release"
	keys(m, runes("e"))
	if m.mode != modeTable || !strings.Contains(ansi.Strip(m.View()), "can't edit: editing needs a local chart") {
		t.Error("expected an explanation in the footer")
	}
}

func TestEditFormFitsTerminal(t *testing.T) {
	for _, sz := range [][2]int{{40, 12}, {80, 24}, {120, 40}, {200, 60}} {
		m := editModel(t, &fakeEditor{})
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		keys(m, runes("e"), tea.KeyMsg{Type: tea.KeyTab}, runes("12345678901234567890"))
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
