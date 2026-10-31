package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/units"
)

// detailLines describes one workload in depth: every container, the
// effective pod footprint, replicas, autoscaling, extended resources,
// images and findings.
func detailLines(w *model.Workload, width int) []string {
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	kv := func(k, v string) string { return stLabel.Render(fmt.Sprintf("%-10s", k)) + v }

	add(stTitle.Render(w.Kind+"/"+w.Name), "")
	ns := w.Namespace
	if ns == "" {
		ns = stDim.Render("(release namespace)")
	}
	_, qs := qosText(w.QoS)
	reps := stPods.Render(fmt.Sprint(w.Replicas))
	if w.PerNode {
		reps += stDim.Render(" (one per node)")
	}
	if w.HPA != nil {
		reps += stDim.Render(fmt.Sprintf("  autoscaled %d → %d", w.HPA.Min, w.HPA.Max))
	}
	add(kv("Namespace", ns), kv("Replicas", reps), kv("QoS", qs.Render(string(w.QoS))))
	if w.Source != "" {
		add(kv("Template", stDim.Render(w.Source)))
	}
	add("")

	eph := w.Pod.HasEphemeral()
	nameW := 9
	for _, c := range w.Containers {
		nameW = max(nameW, ansi.StringWidth(c.Name))
	}
	nameW = min(nameW, max(12, width-2-9-6*9))
	head := []string{fitL("CONTAINER", nameW), fitL("ROLE", 9)}
	for _, h := range []string{"CPU REQ", "CPU LIM", "MEM REQ", "MEM LIM"} {
		head = append(head, fitR(h, colNum))
	}
	if eph {
		head = append(head, fitR("EPH REQ", colNum), fitR("EPH LIM", colNum))
	}
	add(stHeader.Render(" " + strings.Join(head, " ") + " "))

	defaulted := false
	line := func(name, role string, nameStyle func(string) string, r model.Resources) string {
		cells := []string{nameStyle(fitL(name, nameW)), stRole.Render(fitL(role, 9))}
		pair := func(p model.Pair, fq func(model.Quantity) fmtFn) {
			t, st := qty(p.Req, false, fq(p.Req))
			if p.ReqFromLim {
				t += "*"
				defaulted = true
			}
			cells = append(cells, st.Render(fitR(t, colNum)))
			t, st = qty(p.Lim, true, fq(p.Lim))
			cells = append(cells, st.Render(fitR(t, colNum)))
		}
		pair(r.CPU, func(model.Quantity) fmtFn { return units.CPU })
		pair(r.Memory, memFmt)
		if eph {
			pair(r.Ephemeral, memFmt)
		}
		return " " + strings.Join(cells, " ")
	}
	plain := func(s string) string { return s }
	bold := func(s string) string { return stBold.Render(s) }
	for _, c := range w.Containers {
		add(line(c.Name, string(c.Role), plain, c.Resources))
	}
	add(" " + rule(nameW+9+4*(colNum+1)+1+boolInt(eph)*2*(colNum+1)))
	add(line("pod", "effective", bold, w.Pod))
	if w.Replicas != 1 {
		add(line(fmt.Sprintf("× %d", w.Replicas), "replicas", bold, w.Total()))
	}
	if w.HPA != nil && w.HPA.Max != w.Replicas {
		add(line(fmt.Sprintf("× %d", w.HPA.Max), "hpa max", bold, w.Pod.Scale(w.HPA.Max)))
	}
	if defaulted {
		add(stDim.Render(" * request omitted, Kubernetes defaults it to the limit"))
	}

	if len(w.Pod.Extended) > 0 {
		add("", stLabelBold.Render("Extended resources"))
		keys := make([]string, 0, len(w.Pod.Extended))
		for k := range w.Pod.Extended {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := w.Pod.Extended[k]
			add(fmt.Sprintf("  %s  request %s  limit %s", fitL(k, 24), stBold.Render(units.Plain(p.Req.Value)), stBold.Render(units.Plain(p.Lim.Value))))
		}
	}

	add("", stLabelBold.Render("Images"))
	for _, c := range w.Containers {
		add("  " + fitL(c.Name, nameW) + "  " + stDim.Render(c.Image))
	}

	if len(w.Warnings) > 0 {
		add("", stLabelBold.Render("Findings"))
		for _, wr := range w.Warnings {
			add("  " + warningLine(wr, false))
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func warningLine(w model.Warning, withWorkload bool) string {
	var icon string
	switch w.Severity {
	case model.SevError:
		icon = stErr.Render("✖ error")
	case model.SevWarn:
		icon = stWarn.Render("▲ warn ")
	default:
		icon = stInfo.Render("● info ")
	}
	who := stBold.Render(w.Container)
	if withWorkload {
		who = stDim.Render(w.Workload+" ") + who
	}
	return icon + "  " + who + stDim.Render(": ") + w.Message
}

func warningsLines(st *State, width int) []string {
	rep := st.Report
	var out []string
	if st.watch != nil && st.watch.err != "" {
		out = append(out, stTitle.Render("Render error"), "")
		for _, l := range wrap(st.watch.err, width-2) {
			out = append(out, "  "+stErr.Render(l))
		}
		out = append(out, stDim.Render("  The view shows the last successful render until the chart renders again."), "")
	}
	out = append(out, stTitle.Render("Findings"), "")
	if len(rep.Warnings) == 0 {
		return append(out, stReq.Render("  ✔ every container sets cpu and memory requests and limits"))
	}
	for _, w := range rep.Warnings {
		out = append(out, "  "+warningLine(w, true))
	}
	return out
}

func helpLines() []string {
	key := func(k, desc string) string { return "  " + stFKey.Render(fitL(k, 14)) + desc }
	return []string{
		stTitle.Render("rvis · Helm chart resource viewer"),
		"",
		stLabelBold.Render("Navigation"),
		key("↑ ↓  k j", "move selection"),
		key("PgUp PgDn", "page up / down"),
		key("Home End  g G", "first / last row"),
		key("Enter", "details for the selected workload"),
		"",
		stLabelBold.Render("View"),
		key("F3  /", "filter by workload, container or image"),
		key("F5  t", "tree: show / hide containers"),
		key("F6  s  > <", "next / previous sort column"),
		key("I", "invert sort order"),
		key("c C m M n p", "sort by cpu req/lim, mem req/lim, name, pods"),
		key("F2  r", "toggle total (× replicas) / per pod"),
		key("F4  w", "list all findings"),
		key("F7  e", "edit requests and limits; writes to the chart's values or template files"),
		key("F8  E", "quick edit: the whole table becomes editable, totals update as you type"),
		key("F10  q  Esc", "close panel / quit"),
		"",
		stLabelBold.Render("Legend"),
		"  " + stReq.Render("||||") + " request     " + stLim.Render("::::") + " limit headroom above the request",
		"  " + stUnbounded.Render("»") + "    no limit on at least one container (unbounded)",
		"  " + stUnbounded.Render("∞") + "    no limit set     " + stMissing.Render("—") + " no request set",
		"  " + stPartial.Render("1.5+") + " only part of the pod sets this value",
		"  " + stPods.Render("3↑") + "   autoscaled by an HPA     " + stPods.Render("1ds") + " DaemonSet pods (--nodes)",
		"  " + stQoSGuaranteed.Render("Gua") + " / " + stQoSBurstable.Render("Bur") + " / " + stQoSBestEffort.Render("BE") + "  pod QoS class: Guaranteed, Burstable, BestEffort",
		"",
		stDim.Render("  Bars in the table share one scale per column, so rows compare directly."),
		stDim.Render("  Header meters show chart totals; set --cpu/--memory to size them to a node."),
	}
}
