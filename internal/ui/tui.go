package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/model"
)

type mode int

const (
	modeTable mode = iota
	modeFilter
	modeDetail
	modeHelp
	modeWarnings
	modeEdit
	modeQuick
)

type tuiModel struct {
	st            *State
	width, height int
	sel, off      int // selected row and first visible row
	mode          mode
	panel         []string
	panelOff      int
	panelKey      string // workload shown in the detail panel
	filterBefore  string
	watcher       *Watcher
	fingerprint   uint64

	editor          Editor
	editUnavailable string
	reload          func() (*model.Report, error)
	form            *editForm
	quickGen        int
	flash           string // short status shown in the footer
	flashAt         time.Time
}

// TUIOptions are the optional live features of the interactive view.
type TUIOptions struct {
	// Watcher, when set, reloads the view whenever the chart's files change.
	Watcher *Watcher
	// Editor, when set, lets requests and limits be edited in place;
	// otherwise EditUnavailable says why not.
	Editor          Editor
	EditUnavailable string
	// Reload re-renders the chart after an edit when nothing is watching.
	Reload func() (*model.Report, error)
}

// RunTUI starts the interactive, htop-style viewer.
func RunTUI(st *State, opts TUIOptions) error {
	w := opts.Watcher
	m := &tuiModel{st: st, width: 120, height: 40, watcher: w,
		editor: opts.Editor, editUnavailable: opts.EditUnavailable, reload: opts.Reload}
	if w != nil {
		if w.Interval <= 0 {
			w.Interval = 500 * time.Millisecond
		}
		var files int
		m.fingerprint, files = w.Fingerprint()
		st.watch = &watchState{files: files}
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (m *tuiModel) Init() tea.Cmd {
	if m.watcher != nil {
		return m.watcher.poll(m.fingerprint)
	}
	return nil
}

func (m *tuiModel) header() []string { return m.st.headerLines(m.width, m.height-9) }

// header, blank line, table header | body | rule, total, footer
func (m *tuiModel) bodyHeight() int {
	return max(1, m.height-len(m.header())-5)
}

func (m *tuiModel) panelHeight() int {
	return max(3, m.height-len(m.header())-2)
}

func (m *tuiModel) clamp(n int) {
	if n == 0 {
		m.sel, m.off = 0, 0
		return
	}
	m.sel = min(max(m.sel, 0), n-1)
	h := m.bodyHeight()
	if m.sel < m.off {
		m.off = m.sel
	}
	if m.sel >= m.off+h {
		m.off = m.sel - h + 1
	}
	m.off = min(max(m.off, 0), max(0, n-h))
}

// keepSelection re-finds the selected row after the row order changed.
func (m *tuiModel) keepSelection(change func()) {
	rows := m.st.rows()
	var prevKey, prevWorkload string
	if m.sel < len(rows) {
		prevKey = rowKey(rows[m.sel].w, rows[m.sel].c)
		prevWorkload = rowKey(rows[m.sel].w, nil)
	}
	change()
	rows = m.st.rows()
	// Rows are matched by key, not pointer, so the selection survives a
	// live reload. If the row is gone the cursor stays where it was.
	found := false
	for i, r := range rows {
		if k := rowKey(r.w, r.c); k == prevKey {
			m.sel, found = i, true
			break
		}
	}
	if !found {
		for i, r := range rows {
			if r.c == nil && rowKey(r.w, nil) == prevWorkload {
				m.sel = i
				break
			}
		}
	}
	m.clamp(len(rows))
}

// refreshPanel rebuilds an open panel after a resize or a live reload,
// keeping its scroll position.
func (m *tuiModel) refreshPanel() {
	switch m.mode {
	case modeDetail:
		for _, w := range m.st.Report.Workloads {
			if rowKey(w, nil) == m.panelKey {
				m.panel = detailLines(w, m.width-6)
				return
			}
		}
		m.mode = modeTable // the workload was removed from the chart
	case modeWarnings:
		m.panel = warningsLines(m.st, m.width-6)
	}
}

func (m *tuiModel) openPanel(md mode, lines []string) {
	m.mode, m.panel, m.panelOff = md, lines, 0
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	rows := m.st.rows()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.refreshPanel()
		m.clamp(len(rows))
	case pollMsg:
		m.st.watch.files = msg.files
		return m, m.watcher.poll(msg.fp)
	case reloadMsg:
		if m.watcher == nil {
			// A reload after an edit, without live watching.
			if msg.err == nil {
				m.keepSelection(func() { m.st.Report = msg.rep })
				m.refreshPanel()
				return m, nil
			}
			return m, m.flashMsg("reload failed: " + msg.err.Error())
		}
		m.fingerprint = msg.fp
		m.keepSelection(func() { m.st.applyReload(msg) })
		m.refreshPanel()
		cmds := []tea.Cmd{m.watcher.poll(msg.fp)}
		if m.mode == modeQuick && msg.err == nil {
			cmds = append(cmds, m.rebaseQuickEdit())
		}
		return m, tea.Batch(cmds...)
	case quickLocatedMsg:
		return m, m.quickLocated(msg)
	case quickSavedMsg:
		return m, m.quickSaved(msg)
	case locatedMsg:
		if m.form != nil {
			m.form.locating = false
			m.form.results = msg.results
			if msg.err != nil {
				m.form.err = msg.err.Error()
			}
		}
	case savedMsg:
		if m.form == nil {
			return m, nil
		}
		m.form.saving = false
		if msg.err != nil {
			m.form.err = msg.err.Error()
			return m, nil
		}
		m.mode, m.form = modeTable, nil
		note := "✔ saved " + plural(len(msg.summary), "change")
		if len(msg.summary) > 0 {
			note += ": " + msg.summary[0]
			if len(msg.summary) > 1 {
				note += fmt.Sprintf(" (+%d more)", len(msg.summary)-1)
			}
		}
		cmds := []tea.Cmd{m.flashMsg(note)}
		if m.watcher == nil && m.reload != nil {
			reload := m.reload
			cmds = append(cmds, func() tea.Msg {
				rep, err := reload()
				return reloadMsg{rep: rep, err: err}
			})
		}
		return m, tea.Batch(cmds...)
	case tea.MouseMsg:
		return m, m.mouse(msg, len(rows))
	case tea.KeyMsg:
		return m, m.key(msg, rows)
	}
	return m, nil
}

func (m *tuiModel) mouse(msg tea.MouseMsg, n int) tea.Cmd {
	if msg.Action != tea.MouseActionPress {
		return nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.mode == modeQuick {
			m.moveFocus(m.st.rows(), -1)
		} else if m.mode == modeTable {
			m.sel -= 3
			m.clamp(n)
		} else {
			m.panelOff = max(0, m.panelOff-3)
		}
	case tea.MouseButtonWheelDown:
		if m.mode == modeQuick {
			m.moveFocus(m.st.rows(), 1)
		} else if m.mode == modeTable {
			m.sel += 3
			m.clamp(n)
		} else {
			m.scrollPanel(3)
		}
	case tea.MouseButtonLeft:
		top := len(m.header()) + 2
		if (m.mode == modeTable || m.mode == modeQuick) && msg.Y >= top && msg.Y < top+m.bodyHeight() {
			m.sel = m.off + msg.Y - top
			m.clamp(n)
			if m.mode == modeQuick {
				m.moveFocus(m.st.rows(), 0)
			}
		}
	}
	return nil
}

func (m *tuiModel) scrollPanel(d int) {
	m.panelOff = min(max(0, m.panelOff+d), max(0, len(m.panel)-(m.panelHeight()-2)))
}

func (m *tuiModel) key(msg tea.KeyMsg, rows []row) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		return tea.Quit
	}

	switch m.mode {
	case modeFilter:
		switch msg.Type {
		case tea.KeyEnter:
			m.mode = modeTable
		case tea.KeyEsc:
			m.st.Filter, m.mode = m.filterBefore, modeTable
		case tea.KeyBackspace:
			if r := []rune(m.st.Filter); len(r) > 0 {
				m.st.Filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.st.Filter += string(msg.Runes)
		}
		m.sel = 0
		m.clamp(len(m.st.rows()))
		return nil

	case modeEdit:
		return m.editKey(msg)
	case modeQuick:
		return m.quickKey(msg)

	case modeDetail, modeHelp, modeWarnings:
		switch k {
		case "esc", "q", "enter", "f10", "f1", "?", "f4", "w", "left", "h":
			m.mode = modeTable
		case "up", "k":
			m.scrollPanel(-1)
		case "down", "j":
			m.scrollPanel(1)
		case "pgup":
			m.scrollPanel(-(m.panelHeight() - 3))
		case "pgdown", " ":
			m.scrollPanel(m.panelHeight() - 3)
		}
		return nil
	}

	n := len(rows)
	switch k {
	case "q", "f10", "esc":
		if k == "esc" && m.st.Filter != "" {
			m.st.Filter = ""
			m.clamp(len(m.st.rows()))
			return nil
		}
		return tea.Quit
	case "up", "k":
		m.sel--
	case "down", "j":
		m.sel++
	case "pgup":
		m.sel -= m.bodyHeight()
	case "pgdown", " ":
		m.sel += m.bodyHeight()
	case "home", "g":
		m.sel = 0
	case "end", "G":
		m.sel = n - 1
	case "enter", "right", "l":
		if m.sel < n {
			m.panelKey = rowKey(rows[m.sel].w, nil)
			m.openPanel(modeDetail, detailLines(rows[m.sel].w, m.width-6))
		}
	case "f1", "?", "h":
		m.openPanel(modeHelp, helpLines())
	case "f8", "E":
		return m.startQuickEdit()
	case "f7", "e":
		if m.sel < n {
			return m.openEditor(rows[m.sel])
		}
	case "f4", "w":
		m.openPanel(modeWarnings, warningsLines(m.st, m.width-6))
	case "f3", "/":
		m.filterBefore, m.mode = m.st.Filter, modeFilter
	case "f2", "r":
		m.keepSelection(func() { m.st.PerPod = !m.st.PerPod })
	case "f5", "t":
		m.keepSelection(func() { m.st.Flat = !m.st.Flat })
	case "f6", "s", ">", ".":
		m.keepSelection(func() { m.st.Sort, m.st.Invert = (m.st.Sort+1)%sortKeyCount, false })
	case "<", ",":
		m.keepSelection(func() { m.st.Sort, m.st.Invert = (m.st.Sort+sortKeyCount-1)%sortKeyCount, false })
	case "I":
		m.keepSelection(func() { m.st.Invert = !m.st.Invert })
	case "c", "C", "m", "M", "n", "p":
		key := map[string]SortKey{"c": SortCPUReq, "C": SortCPULim, "m": SortMemReq, "M": SortMemLim, "n": SortName, "p": SortPods}[k]
		m.keepSelection(func() {
			if m.st.Sort == key {
				m.st.Invert = !m.st.Invert
			} else {
				m.st.Sort, m.st.Invert = key, false
			}
		})
	}
	m.clamp(n)
	return nil
}

func (m *tuiModel) View() string {
	if m.width < 40 || m.height < 12 {
		return "rvis needs at least 40×12 — enlarge the terminal (q to quit)"
	}
	header := m.header()
	var b strings.Builder
	for _, l := range header {
		b.WriteString(fitL(l, m.width) + "\n")
	}
	b.WriteString("\n")

	switch m.mode {
	case modeEdit:
		m.panel, m.panelOff = m.editLines(m.width-6), 0
		b.WriteString(m.viewPanel())
	case modeDetail, modeHelp, modeWarnings:
		b.WriteString(m.viewPanel())
	default:
		b.WriteString(m.viewTable())
	}
	b.WriteString(m.footer())
	return b.String()
}

func (m *tuiModel) viewTable() string {
	rows := m.st.rows()
	m.clamp(len(rows))
	l := m.st.layout(m.width)
	cpuScale, memScale := m.st.scale(rows)
	h := m.bodyHeight()

	var b strings.Builder
	b.WriteString(m.st.tableHeader(l) + "\n")
	for i := m.off; i < m.off+h; i++ {
		if i < len(rows) {
			b.WriteString(m.st.renderRow(rows[i], l, cpuScale, memScale, i == m.sel))
		} else if i == 0 {
			b.WriteString(stDim.Render(fitL("  no workloads match", m.width)))
		}
		b.WriteString("\n")
	}
	if m.mode == modeQuick {
		b.WriteString(fitL(m.st.editing.statusLine(m.width), m.width) + "\n")
	} else {
		b.WriteString(rule(m.width) + "\n")
	}
	b.WriteString(fitL(m.st.totalRow(l), m.width) + "\n")
	return b.String()
}

func (m *tuiModel) viewPanel() string {
	h := m.panelHeight()
	inner := h - 2
	m.scrollPanel(0)
	end := min(len(m.panel), m.panelOff+inner)
	lines := make([]string, 0, inner)
	for _, l := range m.panel[m.panelOff:end] {
		lines = append(lines, fitL(l, m.width-6))
	}
	for len(lines) < inner {
		lines = append(lines, strings.Repeat(" ", m.width-6))
	}
	box := stBox.Render(strings.Join(lines, "\n"))
	if len(m.panel) > inner {
		box = strings.TrimRight(box, "\n")
		pos := stDim.Render(fmt.Sprintf(" %d–%d of %d ", m.panelOff+1, end, len(m.panel)))
		bl := strings.Split(box, "\n")
		last := bl[len(bl)-1]
		bl[len(bl)-1] = ansi.Truncate(last, m.width-ansi.StringWidth(pos)-3, "") + pos + stLabel.Render("─╯")
		box = strings.Join(bl, "\n")
	}
	return box + "\n"
}

func (m *tuiModel) footer() string {
	type item struct{ key, label string }
	var items []item
	switch m.mode {
	case modeFilter:
		prompt := stFKey.Render("Filter: ") + m.st.Filter + "█"
		hint := stFKey.Render("Enter") + stFLabel.Render("Done  ") + stFKey.Render("Esc") + stFLabel.Render("Cancel")
		gap := m.width - ansi.StringWidth(prompt) - ansi.StringWidth(hint)
		return prompt + strings.Repeat(" ", max(1, gap)) + hint
	case modeQuick:
		items = []item{{"^S", "Save "}, {"Esc", "Exit "}, {"Tab/←→↑↓", "Move "}, {"^U", "Clear "}, {"^Z", "Undo "}}
	case modeEdit:
		items = []item{{"Enter", "Save "}, {"Esc", "Cancel "}, {"Tab/←→↑↓", "Move "}, {"^U", "Clear "}, {"^Z", "Undo "}}
	case modeDetail, modeHelp, modeWarnings:
		items = []item{{"Esc", "Close "}, {"↑↓", "Scroll "}, {"PgUp/PgDn", "Page "}}
	default:
		view := "PerPod"
		if m.st.PerPod {
			view = "Total "
		}
		tree := "Flat  "
		if m.st.Flat {
			tree = "Tree  "
		}
		items = []item{
			{"F1", "Help  "}, {"F2", view}, {"F3", "Filter"}, {"F4", "Issues"},
			{"F5", tree}, {"F6", "SortBy"}, {"F7", "Edit  "}, {"F8", "QEdit "}, {"F10", "Quit  "},
		}
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(stFKey.Render(it.key) + stFLabel.Render(it.label+" "))
	}
	if m.mode == modeTable {
		rows := m.st.rows()
		info := ""
		if m.st.Filter != "" {
			info = "filter \"" + m.st.Filter + "\"  "
		}
		info += fmt.Sprintf("%d/%d ", min(m.sel+1, len(rows)), len(rows))
		// A status message (e.g. after saving an edit) takes the whole line
		// for a few seconds.
		if m.flash != "" && time.Since(m.flashAt) < flashTTL {
			return fitBg(stFLabel.Render(" "+m.flash+"  "+info), m.width, stFLabel)
		}
		gap := m.width - ansi.StringWidth(b.String()) - ansi.StringWidth(info)
		if gap > 0 {
			b.WriteString(stFLabel.Render(strings.Repeat(" ", gap) + info))
		}
	}
	return fitBg(b.String(), m.width, stFLabel)
}
