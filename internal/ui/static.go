package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mohammed-ehsan/rvis/internal/model"
)

// RenderStatic prints the same screen as the TUI, with every row expanded
// and no interactive footer, for pipes, CI logs and quick looks.
func RenderStatic(w io.Writer, st *State, width int) error {
	var b strings.Builder
	for _, l := range st.headerLines(width, 0) {
		b.WriteString(strings.TrimRight(l, " ") + "\n")
	}
	b.WriteString("\n")

	rows := st.rows()
	l := st.layout(width)
	cpuScale, memScale := st.scale(rows)
	b.WriteString(st.tableHeader(l) + "\n")
	for _, r := range rows {
		b.WriteString(strings.TrimRight(st.renderRow(r, l, cpuScale, memScale, false), " ") + "\n")
	}
	if len(rows) == 0 {
		b.WriteString(stDim.Render("  no workloads found") + "\n")
	}
	b.WriteString(rule(width) + "\n")
	b.WriteString(st.totalRow(l) + "\n")

	var infos int
	var shown []model.Warning
	for _, wr := range st.Report.Warnings {
		if wr.Severity == model.SevInfo {
			infos++
		} else {
			shown = append(shown, wr)
		}
	}
	if len(shown) > 0 || infos > 0 {
		b.WriteString("\n")
	}
	for _, wr := range shown {
		b.WriteString(" " + warningLine(wr, true) + "\n")
	}
	if infos > 0 {
		b.WriteString(stDim.Render(fmt.Sprintf(" + %s (e.g. no cpu limit, decimal memory units); see the TUI's F4 panel or -o json", plural(infos, "info finding"))) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// RenderJSON writes the full report for scripting.
func RenderJSON(w io.Writer, rep *model.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
