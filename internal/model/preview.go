package model

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Override is a pending new value for one request or limit of one
// container, or for a workload's replica count (Field ReplicasField, no
// Container).
type Override struct {
	Workload  string // WorkloadKey of the workload
	Container string
	Field     string // "requests.cpu", "limits.memory", ... or ReplicasField
}

// ReplicasField is the Override field for a workload's replica count.
const ReplicasField = "replicas"

// WorkloadKey identifies a workload across renders.
func WorkloadKey(w *Workload) string { return w.Kind + "/" + w.Namespace + "/" + w.Name }

// Preview returns a copy of the report with the overrides applied and every
// derived number (effective pods, QoS, totals, findings) recomputed. The
// receiver is not modified. Values that are not valid quantities are
// reported in the error map and left unchanged in the preview.
func (r *Report) Preview(over map[Override]string) (*Report, map[Override]error) {
	errs := map[Override]error{}
	out := *r
	out.Workloads = make([]*Workload, len(r.Workloads))
	for i, w := range r.Workloads {
		cw := *w
		if v, ok := over[Override{Workload: WorkloadKey(w), Field: ReplicasField}]; ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			switch {
			case err != nil || n < 0:
				errs[Override{Workload: WorkloadKey(w), Field: ReplicasField}] = fmt.Errorf("%q is not a replica count", v)
			case cw.ReplicaField() == "minReplicas" && n < 1:
				errs[Override{Workload: WorkloadKey(w), Field: ReplicasField}] = fmt.Errorf("an autoscaler's minReplicas must be at least 1")
			case w.HPA != nil && n > w.HPA.Max && cw.ReplicaField() == "minReplicas":
				errs[Override{Workload: WorkloadKey(w), Field: ReplicasField}] = fmt.Errorf("minReplicas %d is above the autoscaler's maxReplicas %d", n, w.HPA.Max)
			default:
				cw.SetReplicas(n)
			}
		}
		cw.Containers = make([]*Container, len(w.Containers))
		for j, c := range w.Containers {
			cc := *c
			for o, v := range over {
				if o.Workload != WorkloadKey(w) || o.Container != c.Name || o.Field == ReplicasField {
					continue
				}
				if err := cc.set(o.Field, v); err != nil {
					errs[o] = err
				}
			}
			cw.Containers[j] = &cc
		}
		out.Workloads[i] = &cw
	}
	out.Finalize()
	return &out, errs
}

// set changes one value of a container copy, applying the Kubernetes rule
// that an omitted request follows the limit.
func (c *Container) set(field, value string) error {
	group, res, ok := strings.Cut(field, ".")
	if !ok {
		return fmt.Errorf("unknown field %q", field)
	}
	q, err := resource.ParseQuantity(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%q is not a quantity", value)
	}
	var p *Pair
	switch res {
	case "cpu":
		p = &c.Resources.CPU
	case "memory":
		p = &c.Resources.Memory
	default:
		return fmt.Errorf("unsupported resource %q", res)
	}
	amount := Quantity{Units: unitsOf(strings.TrimSpace(value))}
	if res == "cpu" {
		amount.Value = q.MilliValue()
	} else {
		amount.Value = q.Value()
	}
	switch group {
	case "requests":
		p.Req, p.ReqFromLim = amount, false
	case "limits":
		p.Lim = amount
		if p.ReqFromLim {
			p.Req = amount
		}
	default:
		return fmt.Errorf("unknown group %q", group)
	}
	return nil
}
