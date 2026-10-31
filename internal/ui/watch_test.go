package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/model"
)

func reportWith(t *testing.T, cpu string) *model.Report {
	t.Helper()
	rep, err := model.Parse([]byte(`
kind: Deployment
metadata: {name: api}
spec:
  replicas: 2
  template: {spec: {containers: [{name: app, resources: {requests: {cpu: `+cpu+`, memory: 1Gi}, limits: {memory: 1Gi}}}, {name: proxy, resources: {requests: {cpu: 10m, memory: 8Mi}, limits: {memory: 8Mi}}}]}}
---
kind: StatefulSet
metadata: {name: db}
spec:
  template: {spec: {containers: [{name: pg, resources: {requests: {cpu: 1, memory: 1Gi}, limits: {memory: 1Gi}}}]}}
`), model.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestReloadMarksChangesAndKeepsSelection(t *testing.T) {
	st := &State{Report: reportWith(t, "100m"), Sort: SortName}
	m := &tuiModel{st: st, width: 120, height: 30, watcher: &Watcher{
		Fingerprint: func() (uint64, int) { return 1, 3 },
	}}
	st.watch = &watchState{files: 3}

	// Select the "proxy" container of "api".
	for i, r := range st.rows() {
		if r.c != nil && r.c.Name == "proxy" {
			m.sel = i
		}
	}
	m.Update(reloadMsg{fp: 2, files: 3, rep: reportWith(t, "500m")})

	if got := st.rows()[m.sel]; got.c == nil || got.c.Name != "proxy" {
		t.Errorf("selection moved to %+v", got)
	}
	changed := st.watch.changed
	if !changed["Deployment//api"] || !changed["Deployment//api/app"] || changed["Deployment//api/proxy"] || changed["StatefulSet//db"] {
		t.Errorf("changed = %v", changed)
	}
	if st.watch.delta.CPU.Req.Value != 800 {
		t.Errorf("cpu delta = %d, want 800m (2 replicas × 400m)", st.watch.delta.CPU.Req.Value)
	}
	if !strings.Contains(ansi.Strip(st.watchSummary()), "cpu +800m") {
		t.Errorf("summary = %q", ansi.Strip(st.watchSummary()))
	}
}

func TestReloadErrorKeepsLastGoodReport(t *testing.T) {
	good := reportWith(t, "100m")
	st := &State{Report: good}
	m := &tuiModel{st: st, watcher: &Watcher{Fingerprint: func() (uint64, int) { return 1, 1 }}}
	st.watch = &watchState{}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(reloadMsg{fp: 2, files: 1, err: errors.New("parse error at (shop/templates/api.yaml:12): unexpected }}\nsecond line")})

	if st.Report != good {
		t.Error("report replaced despite the error")
	}
	frame := m.View()
	lines := strings.Split(frame, "\n")
	if !strings.Contains(ansi.Strip(lines[0]), "parse error at") {
		t.Errorf("first line should show the error, got %q", ansi.Strip(lines[0]))
	}
	if len(lines) != 30 {
		t.Errorf("frame has %d lines, want 30", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}

	m.Update(reloadMsg{fp: 3, files: 1, rep: reportWith(t, "200m")})
	if st.watch.err != "" || st.Report == good {
		t.Error("successful reload did not clear the error")
	}
}

func TestMemoryShownInChartUnits(t *testing.T) {
	rep, err := model.Parse([]byte(`
kind: Deployment
metadata: {name: svc}
spec:
  template: {spec: {containers: [{name: app, resources: {requests: {cpu: 1, memory: 2G}, limits: {cpu: 1, memory: 3G}}}]}}
`), model.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Report: rep}
	var b strings.Builder
	if err := RenderStatic(&b, st, 140); err != nil {
		t.Fatal(err)
	}
	out := ansi.Strip(b.String())
	if !strings.Contains(out, "3G") || !strings.Contains(out, "2G") || strings.Contains(out, "2.8Gi") {
		t.Errorf("expected values in decimal units as written:\n%s", out)
	}
}
