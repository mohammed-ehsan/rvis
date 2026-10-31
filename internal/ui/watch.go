package ui

import (
	"reflect"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/units"
)

// Watcher re-renders the chart while the TUI is open whenever its files
// change.
type Watcher struct {
	// Fingerprint summarizes the watched files; any change in the value
	// triggers a reload. It also returns how many files are watched.
	Fingerprint func() (uint64, int)
	Reload      func() (*model.Report, error)
	Interval    time.Duration
}

// How long rows touched by a reload keep their change marker.
const changeMarkerTTL = 5 * time.Second

type watchState struct {
	files     int
	reloads   int
	at        time.Time
	err       string
	delta     model.Resources // requested totals: new − previous
	changed   map[string]bool // row keys touched by the last reload
	changedAt time.Time
}

type pollMsg struct {
	fp    uint64
	files int
}

type reloadMsg struct {
	fp    uint64
	files int
	rep   *model.Report
	err   error
}

// poll waits one interval and checks the files. On a change it waits for
// the writes to settle (editors often save in several steps), then reloads.
// Polls are chained one after another, so reloads never overlap.
func (w *Watcher) poll(last uint64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(w.Interval)
		fp, n := w.Fingerprint()
		if fp == last {
			return pollMsg{fp, n}
		}
		for range 10 {
			time.Sleep(150 * time.Millisecond)
			next, m := w.Fingerprint()
			if next == fp {
				break
			}
			fp, n = next, m
		}
		rep, err := w.Reload()
		return reloadMsg{fp: fp, files: n, rep: rep, err: err}
	}
}

func rowKey(w *model.Workload, c *model.Container) string {
	k := w.Kind + "/" + w.Namespace + "/" + w.Name
	if c != nil {
		k += "/" + c.Name
	}
	return k
}

// diffReports returns the keys of rows that are new or whose resources,
// replicas or autoscaling changed between two reports.
func diffReports(old, cur *model.Report) map[string]bool {
	prev := map[string]*model.Workload{}
	prevC := map[string]*model.Container{}
	for _, w := range old.Workloads {
		prev[rowKey(w, nil)] = w
		for _, c := range w.Containers {
			prevC[rowKey(w, c)] = c
		}
	}
	changed := map[string]bool{}
	for _, w := range cur.Workloads {
		k := rowKey(w, nil)
		p, ok := prev[k]
		if !ok || p.Replicas != w.Replicas || !reflect.DeepEqual(p.Pod, w.Pod) || !reflect.DeepEqual(p.HPA, w.HPA) {
			changed[k] = true
		}
		for _, c := range w.Containers {
			ck := rowKey(w, c)
			pc, ok := prevC[ck]
			if !ok || pc.Role != c.Role || !reflect.DeepEqual(pc.Resources, c.Resources) {
				changed[ck] = true
				changed[k] = true
			}
		}
	}
	return changed
}

// applyReload swaps in a freshly rendered report, or records the error and
// keeps showing the last good one.
func (s *State) applyReload(msg reloadMsg) {
	ws := s.watch
	ws.files = msg.files
	ws.at = time.Now()
	if msg.err != nil {
		ws.err = msg.err.Error()
		return
	}
	ws.err = ""
	ws.reloads++
	old := s.Report
	ws.changed = diffReports(old, msg.rep)
	ws.changedAt = time.Now()
	ws.delta = model.Resources{}
	ws.delta.CPU.Req.Value = msg.rep.Total.CPU.Req.Value - old.Total.CPU.Req.Value
	ws.delta.Memory.Req.Value = msg.rep.Total.Memory.Req.Value - old.Total.Memory.Req.Value
	ws.delta.Memory.Req.Units = msg.rep.Total.Memory.Req.Units
	s.Report = msg.rep
}

func (s *State) recentlyChanged(r row) bool {
	if s.watch == nil || s.watch.changed == nil || time.Since(s.watch.changedAt) > changeMarkerTTL {
		return false
	}
	return s.watch.changed[rowKey(r.w, r.c)]
}

// watchSummary is the "Watch:" line of the header.
func (s *State) watchSummary() string {
	ws := s.watch
	if ws.err != "" {
		return stErr.Render("✖ render failed") + stDim.Render(" at "+ws.at.Format("15:04:05")+", showing last good render")
	}
	out := stReq.Render("●") + " live" + stDim.Render(" · "+plural(ws.files, "file"))
	if ws.reloads == 0 {
		return out + stDim.Render(" · waiting for changes")
	}
	out += stDim.Render(" · reloaded " + ws.at.Format("15:04:05"))
	if d := deltaText(ws.delta); d != "" {
		out += "  " + d
	}
	return out
}

func deltaText(d model.Resources) string {
	var parts []string
	one := func(label string, v int64, f fmtFn) {
		switch {
		case v > 0:
			parts = append(parts, stLim.Render(label+" +"+f(v)))
		case v < 0:
			parts = append(parts, stReq.Render(label+" −"+f(-v)))
		}
	}
	one("cpu", d.CPU.Req.Value, units.CPU)
	one("mem", d.Memory.Req.Value, memFmt(d.Memory.Req))
	return strings.Join(parts, " ")
}

func plural(n int, word string) string {
	s := units.Plain(int64(n) * 1000)
	if n == 1 {
		return s + " " + word
	}
	return s + " " + word + "s"
}

// wrap breaks text into lines of at most width cells.
func wrap(text string, width int) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		r := []rune(line)
		for len(r) > width && width > 0 {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}
