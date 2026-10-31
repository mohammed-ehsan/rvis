// Package model holds the resource view of a rendered Helm chart: workloads,
// their containers, and the request/limit arithmetic Kubernetes applies to them.
package model

import (
	"fmt"
	"sort"

	"github.com/mohammed-ehsan/rvis/internal/units"
)

// Quantity is an aggregated amount of one resource. CPU is stored in
// millicores, memory and ephemeral storage in bytes, extended resources in
// milli-units. Missing counts how many contributing containers did not set a
// value, so a total can say "2.5 cores, but 3 containers are unbounded".
type Quantity struct {
	Value   int64 `json:"value"`
	Missing int   `json:"missing,omitempty"`
	// Units is the suffix family the chart used, so values can be shown the
	// way they were written: 3G stays 3G instead of becoming 2.8Gi.
	Units Units `json:"-"`
}

// Units is the suffix family of a quantity.
type Units uint8

const (
	UnitsNone    Units = iota // plain number, or no value
	UnitsDecimal              // k, M, G, T, P, E (powers of 1000)
	UnitsBinary               // Ki, Mi, Gi, Ti, Pi, Ei (powers of 1024)
	UnitsMixed                // a sum of decimal and binary values
)

func (u Units) merge(o Units) Units {
	switch {
	case u == UnitsNone:
		return o
	case o == UnitsNone, o == u:
		return u
	}
	return UnitsMixed
}

// Set reports whether the quantity is fully defined.
func (q Quantity) Set() bool { return q.Missing == 0 }

func (q Quantity) add(o Quantity) Quantity {
	return Quantity{Value: q.Value + o.Value, Missing: q.Missing + o.Missing, Units: q.Units.merge(o.Units)}
}

func (q Quantity) scale(n int64) Quantity {
	return Quantity{Value: q.Value * n, Missing: q.Missing * int(n), Units: q.Units}
}

// Pair is a request/limit pair for one resource.
type Pair struct {
	Req Quantity `json:"request"`
	Lim Quantity `json:"limit"`
	// ReqFromLim is set on containers whose request was omitted and is
	// therefore defaulted by Kubernetes to the limit.
	ReqFromLim bool `json:"requestDefaulted,omitempty"`
}

func (p Pair) add(o Pair) Pair    { return Pair{Req: p.Req.add(o.Req), Lim: p.Lim.add(o.Lim)} }
func (p Pair) scale(n int64) Pair { return Pair{Req: p.Req.scale(n), Lim: p.Lim.scale(n)} }

// max picks the larger request and the larger limit independently, as the
// scheduler does when comparing init containers with the app containers.
func (p Pair) max(o Pair) Pair {
	r := p
	if o.Req.Value > r.Req.Value {
		r.Req = o.Req
	}
	if o.Lim.Value > r.Lim.Value {
		r.Lim = o.Lim
	}
	return r
}

// Resources holds the pairs for every resource rvis tracks.
type Resources struct {
	CPU       Pair            `json:"cpu"`
	Memory    Pair            `json:"memory"`
	Ephemeral Pair            `json:"ephemeralStorage"`
	Extended  map[string]Pair `json:"extended,omitempty"`
}

// Add returns r + o.
func (r Resources) Add(o Resources) Resources {
	out := Resources{
		CPU:       r.CPU.add(o.CPU),
		Memory:    r.Memory.add(o.Memory),
		Ephemeral: r.Ephemeral.add(o.Ephemeral),
	}
	out.Extended = mergeExtended(r.Extended, o.Extended, Pair.add)
	return out
}

// Scale returns r multiplied by n replicas.
func (r Resources) Scale(n int64) Resources {
	out := Resources{CPU: r.CPU.scale(n), Memory: r.Memory.scale(n), Ephemeral: r.Ephemeral.scale(n)}
	if len(r.Extended) > 0 {
		out.Extended = make(map[string]Pair, len(r.Extended))
		for k, v := range r.Extended {
			out.Extended[k] = v.scale(n)
		}
	}
	return out
}

func (r Resources) max(o Resources) Resources {
	out := Resources{
		CPU:       r.CPU.max(o.CPU),
		Memory:    r.Memory.max(o.Memory),
		Ephemeral: r.Ephemeral.max(o.Ephemeral),
	}
	out.Extended = mergeExtended(r.Extended, o.Extended, Pair.max)
	return out
}

func mergeExtended(a, b map[string]Pair, f func(Pair, Pair) Pair) map[string]Pair {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]Pair, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = f(out[k], v)
	}
	return out
}

// HasEphemeral reports whether any ephemeral-storage value is present.
func (r Resources) HasEphemeral() bool {
	return r.Ephemeral.Req.Value > 0 || r.Ephemeral.Lim.Value > 0
}

// ContainerRole distinguishes regular, init and native sidecar containers.
type ContainerRole string

const (
	RoleApp     ContainerRole = "app"
	RoleInit    ContainerRole = "init"
	RoleSidecar ContainerRole = "sidecar"
)

// Container is a single container of a pod template.
type Container struct {
	Name      string        `json:"name"`
	Image     string        `json:"image,omitempty"`
	Role      ContainerRole `json:"role"`
	Resources Resources     `json:"resources"`
	// Raw holds the rendered strings, keyed "requests.cpu", "limits.memory", ...
	Raw map[string]string `json:"-"`
}

// QoS is the Kubernetes pod quality-of-service class.
type QoS string

const (
	QoSGuaranteed QoS = "Guaranteed"
	QoSBurstable  QoS = "Burstable"
	QoSBestEffort QoS = "BestEffort"
)

// Workload is anything that creates pods: Deployments, StatefulSets, Jobs, ...
type Workload struct {
	Kind       string       `json:"kind"`
	Name       string       `json:"name"`
	Namespace  string       `json:"namespace,omitempty"`
	Source     string       `json:"source,omitempty"`
	Replicas   int64        `json:"replicas"`
	Spec       int64        `json:"specReplicas"` // spec.replicas or parallelism, before autoscaling
	SpecSet    bool         `json:"-"`            // the rendered manifest sets Spec explicitly
	PerNode    bool         `json:"perNode,omitempty"`
	HPA        *Autoscaling `json:"hpa,omitempty"`
	Containers []*Container `json:"containers"`
	// Pod is the effective per-pod footprint as the scheduler sees it.
	Pod      Resources `json:"pod"`
	QoS      QoS       `json:"qos"`
	Warnings []Warning `json:"warnings,omitempty"`
}

// Autoscaling captures a HorizontalPodAutoscaler targeting a workload.
type Autoscaling struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

// ReplicaSource names the value that decides a workload's pod count, which
// is the one to edit to change it.
type ReplicaSource int

const (
	ReplicasNotEditable ReplicaSource = iota
	ReplicasSpec                      // spec.replicas, or parallelism for Jobs
	ReplicasHPAMin                    // an autoscaler's minReplicas
)

// ReplicaSource reports which value decides the pod count, or why there is
// none to edit.
func (w *Workload) ReplicaSource() (ReplicaSource, string) {
	switch {
	case w.PerNode:
		return ReplicasNotEditable, "a DaemonSet runs one pod per node; set --nodes to model the cluster size"
	case w.Kind == "Pod":
		return ReplicasNotEditable, "a bare Pod always runs exactly one replica"
	case w.HPA != nil && w.HPA.Min >= w.Spec:
		return ReplicasHPAMin, ""
	}
	return ReplicasSpec, ""
}

// ReplicaField is the name of the replica count in the source:
// "replicas", "parallelism" or "minReplicas".
func (w *Workload) ReplicaField() string {
	src, _ := w.ReplicaSource()
	switch {
	case src == ReplicasHPAMin:
		return "minReplicas"
	case w.Kind == "Job" || w.Kind == "CronJob":
		return "parallelism"
	}
	return "replicas"
}

// SetReplicas changes the value ReplicaSource names and recomputes the pod
// count, keeping the autoscaler's floor.
func (w *Workload) SetReplicas(n int64) {
	src, _ := w.ReplicaSource()
	switch src {
	case ReplicasHPAMin:
		h := *w.HPA
		h.Min = n
		w.HPA = &h
	case ReplicasSpec:
		w.Spec, w.SpecSet = n, true
	default:
		return
	}
	w.Replicas = w.Spec
	if w.HPA != nil && w.Replicas < w.HPA.Min {
		w.Replicas = w.HPA.Min
	}
}

// Total is the per-pod footprint multiplied by the replica count.
func (w *Workload) Total() Resources { return w.Pod.Scale(w.Replicas) }

// MaxReplicas is the replica count at full autoscaling.
func (w *Workload) MaxReplicas() int64 {
	if w.HPA != nil && w.HPA.Max > w.Replicas {
		return w.HPA.Max
	}
	return w.Replicas
}

// Severity of a warning.
type Severity int

const (
	SevInfo Severity = iota
	SevWarn
	SevError
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return "error"
	case SevWarn:
		return "warn"
	}
	return "info"
}

// MarshalText makes severities readable in JSON output.
func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Warning is a finding about a container's resource configuration.
type Warning struct {
	Severity  Severity `json:"severity"`
	Workload  string   `json:"workload"`
	Container string   `json:"container,omitempty"`
	Message   string   `json:"message"`
}

// Report is everything rvis knows about a chart.
type Report struct {
	Title      string      `json:"title"`
	Chart      string      `json:"chart,omitempty"`
	Workloads  []*Workload `json:"workloads"`
	Total      Resources   `json:"total"`
	TotalMax   Resources   `json:"totalAtMaxScale"`
	HasHPA     bool        `json:"hasHPA"`
	Pods       int64       `json:"pods"`
	Containers int         `json:"containers"`
	Warnings   []Warning   `json:"warnings,omitempty"`
}

// Finalize computes per-pod effective resources, QoS classes, warnings and
// the chart totals. It must be called after all workloads are added.
func (r *Report) Finalize() {
	r.Total, r.TotalMax = Resources{}, Resources{}
	r.Pods, r.Containers, r.Warnings, r.HasHPA = 0, 0, nil, false
	for _, w := range r.Workloads {
		w.Pod = EffectivePod(w.Containers)
		w.QoS = qosClass(w.Containers)
		w.Warnings = lint(w)
		r.Total = r.Total.Add(w.Total())
		r.TotalMax = r.TotalMax.Add(w.Pod.Scale(w.MaxReplicas()))
		r.Pods += w.Replicas
		r.Containers += len(w.Containers)
		r.Warnings = append(r.Warnings, w.Warnings...)
		if w.HPA != nil {
			r.HasHPA = true
		}
	}
	sort.SliceStable(r.Warnings, func(i, j int) bool { return r.Warnings[i].Severity > r.Warnings[j].Severity })
}

// EffectivePod applies the Kubernetes rule for pod-level resources:
// the larger of (sum of app containers + sidecars) and, for each init
// container, (that init container + the sidecars started before it).
func EffectivePod(cs []*Container) Resources {
	var sidecars, apps, peak Resources
	for _, c := range cs {
		switch c.Role {
		case RoleSidecar:
			sidecars = sidecars.Add(c.Resources)
		case RoleInit:
			peak = peak.max(c.Resources.Add(sidecars))
		}
	}
	for _, c := range cs {
		if c.Role == RoleApp {
			apps = apps.Add(c.Resources)
		}
	}
	steady := apps.Add(sidecars)
	eff := steady.max(peak)
	// An unbounded app or sidecar container makes the pod limit unbounded
	// regardless of which side won the max above.
	eff.CPU.Lim.Missing = max(eff.CPU.Lim.Missing, steady.CPU.Lim.Missing)
	eff.Memory.Lim.Missing = max(eff.Memory.Lim.Missing, steady.Memory.Lim.Missing)
	eff.CPU.Req.Missing = max(eff.CPU.Req.Missing, steady.CPU.Req.Missing)
	eff.Memory.Req.Missing = max(eff.Memory.Req.Missing, steady.Memory.Req.Missing)
	return eff
}

func qosClass(cs []*Container) QoS {
	anySet, guaranteed := false, true
	for _, c := range cs {
		for _, p := range []Pair{c.Resources.CPU, c.Resources.Memory} {
			if p.Req.Set() || p.Lim.Set() {
				anySet = true
			}
			if !p.Req.Set() || !p.Lim.Set() || p.Req.Value != p.Lim.Value {
				guaranteed = false
			}
		}
	}
	switch {
	case !anySet:
		return QoSBestEffort
	case guaranteed:
		return QoSGuaranteed
	}
	return QoSBurstable
}

func lint(w *Workload) []Warning {
	var out []Warning
	add := func(sev Severity, c *Container, msg string) {
		out = append(out, Warning{Severity: sev, Workload: w.Kind + "/" + w.Name, Container: c.Name, Message: msg})
	}
	for _, c := range w.Containers {
		res := c.Resources
		if res.CPU.Req.Set() && res.CPU.Lim.Set() && res.CPU.Lim.Value < res.CPU.Req.Value {
			add(SevError, c, "cpu limit is lower than its request")
		}
		if res.Memory.Req.Set() && res.Memory.Lim.Set() && res.Memory.Lim.Value < res.Memory.Req.Value {
			add(SevError, c, "memory limit is lower than its request")
		}
		if !res.Memory.Lim.Set() {
			add(SevWarn, c, "no memory limit (can use all node memory)")
		}
		if !res.CPU.Req.Set() {
			add(SevWarn, c, "no cpu request (scheduled as 0)")
		}
		if !res.Memory.Req.Set() {
			add(SevWarn, c, "no memory request (scheduled as 0)")
		}
		if q := decimalMemory(res.Memory); q != nil {
			add(SevInfo, c, fmt.Sprintf("memory uses decimal units: %s = %s bytes = %s (use Mi/Gi for powers of 1024)",
				units.DecimalBytes(q.Value), units.Grouped(q.Value), units.Bytes(q.Value)))
		}
		if !res.CPU.Lim.Set() && res.CPU.Req.Set() {
			add(SevInfo, c, "no cpu limit (may burst to node capacity)")
		}
	}
	return out
}

// decimalMemory returns the memory value written with a decimal suffix
// (limit first), or nil. "3G" is 3,000,000,000 bytes, about 7% less than the
// 3Gi most people mean.
func decimalMemory(p Pair) *Quantity {
	for _, q := range []Quantity{p.Lim, p.Req} {
		if q.Set() && q.Units == UnitsDecimal && q.Value >= 1000*1000 {
			return &q
		}
	}
	return nil
}
