package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

type rawContainer struct {
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	RestartPolicy *string `json:"restartPolicy"`
	Resources     struct {
		Requests map[string]any `json:"requests"`
		Limits   map[string]any `json:"limits"`
	} `json:"resources"`
}

type rawPodSpec struct {
	Containers     []rawContainer `json:"containers"`
	InitContainers []rawContainer `json:"initContainers"`
}

type rawTemplate struct {
	Spec rawPodSpec `json:"spec"`
}

type rawObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		rawPodSpec
		Replicas    *int64       `json:"replicas"`
		Parallelism *int64       `json:"parallelism"`
		Template    *rawTemplate `json:"template"`
		JobTemplate *struct {
			Spec struct {
				Parallelism *int64       `json:"parallelism"`
				Template    *rawTemplate `json:"template"`
			} `json:"spec"`
		} `json:"jobTemplate"`
		ScaleTargetRef *struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"scaleTargetRef"`
		MinReplicas *int64 `json:"minReplicas"`
		MaxReplicas int64  `json:"maxReplicas"`
	} `json:"spec"`
	Items []json.RawMessage `json:"items"`
}

// ParseOptions tunes how manifests are turned into a report.
type ParseOptions struct {
	// Nodes multiplies DaemonSet pods; defaults to 1.
	Nodes int64
}

var sourceRe = regexp.MustCompile(`(?m)^#\s*Source:\s*(\S+)`)

// Parse reads a multi-document YAML stream (the output of `helm template`
// or `helm get manifest`) and builds a finalized report.
func Parse(data []byte, opts ParseOptions) (*Report, error) {
	if opts.Nodes < 1 {
		opts.Nodes = 1
	}
	rep := &Report{}
	var hpas []rawObject
	for i, doc := range splitDocuments(data) {
		src := ""
		if m := sourceRe.FindSubmatch(doc); m != nil {
			src = string(m[1])
			if rep.Title == "" {
				rep.Title, _, _ = strings.Cut(src, "/")
			}
		}
		js, err := yaml.YAMLToJSON(doc)
		if err != nil {
			return nil, fmt.Errorf("document %d (%s): %w", i+1, orUnknown(src), err)
		}
		if err := collect(js, src, opts, rep, &hpas); err != nil {
			return nil, fmt.Errorf("document %d (%s): %w", i+1, orUnknown(src), err)
		}
	}
	applyHPAs(rep, hpas)
	rep.Finalize()
	return rep, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown source"
	}
	return s
}

func collect(js []byte, src string, opts ParseOptions, rep *Report, hpas *[]rawObject) error {
	if len(bytes.TrimSpace(js)) == 0 || string(js) == "null" {
		return nil
	}
	var obj rawObject
	if err := json.Unmarshal(js, &obj); err != nil {
		// Not an object we understand (e.g. a bare string); ignore it.
		return nil
	}
	// The conventional helm.sh/chart label carries "name-version".
	if c := obj.Metadata.Labels["helm.sh/chart"]; c != "" && rep.Chart == "" {
		rep.Chart = c
	}
	if strings.HasSuffix(obj.Kind, "List") {
		for _, it := range obj.Items {
			if err := collect(it, src, opts, rep, hpas); err != nil {
				return err
			}
		}
		return nil
	}

	var (
		spec      *rawPodSpec
		replicas  int64  = 1
		specCount *int64 // spec.replicas or parallelism, as rendered
		perNode   bool
	)
	s := &obj.Spec
	switch obj.Kind {
	case "HorizontalPodAutoscaler":
		*hpas = append(*hpas, obj)
		return nil
	case "Pod":
		spec = &s.rawPodSpec
	case "CronJob":
		if s.JobTemplate != nil && s.JobTemplate.Spec.Template != nil {
			spec = &s.JobTemplate.Spec.Template.Spec
			specCount = s.JobTemplate.Spec.Parallelism
		}
	case "Job":
		if s.Template != nil {
			spec = &s.Template.Spec
			specCount = s.Parallelism
		}
	case "DaemonSet":
		if s.Template != nil {
			spec = &s.Template.Spec
			replicas, perNode = opts.Nodes, true
		}
	default:
		// Deployments, StatefulSets, ReplicaSets, ReplicationControllers and
		// any CRD with the same shape (Argo Rollouts, OpenKruise, ...).
		if s.Template != nil && len(s.Template.Spec.Containers) > 0 {
			spec = &s.Template.Spec
			specCount = s.Replicas
		}
	}
	if !perNode {
		replicas = deref(specCount, 1)
	}
	if spec == nil || len(spec.Containers) == 0 {
		return nil
	}

	w := &Workload{
		Kind:      obj.Kind,
		Name:      obj.Metadata.Name,
		Namespace: obj.Metadata.Namespace,
		Source:    src,
		Replicas:  replicas,
		Spec:      replicas,
		SpecSet:   specCount != nil,
		PerNode:   perNode,
	}
	for _, rc := range spec.InitContainers {
		role := RoleInit
		if rc.RestartPolicy != nil && *rc.RestartPolicy == "Always" {
			role = RoleSidecar
		}
		c, err := convert(rc, role)
		if err != nil {
			return err
		}
		w.Containers = append(w.Containers, c)
	}
	for _, rc := range spec.Containers {
		c, err := convert(rc, RoleApp)
		if err != nil {
			return err
		}
		w.Containers = append(w.Containers, c)
	}
	rep.Workloads = append(rep.Workloads, w)
	return nil
}

func deref(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}

func applyHPAs(rep *Report, hpas []rawObject) {
	for _, h := range hpas {
		ref := h.Spec.ScaleTargetRef
		if ref == nil {
			continue
		}
		for _, w := range rep.Workloads {
			if w.Kind != ref.Kind || w.Name != ref.Name {
				continue
			}
			a := &Autoscaling{Min: deref(h.Spec.MinReplicas, 1), Max: h.Spec.MaxReplicas}
			w.HPA = a
			// The HPA immediately raises a workload below its minimum.
			if w.Replicas < a.Min {
				w.Replicas = a.Min
			}
		}
	}
}

func convert(rc rawContainer, role ContainerRole) (*Container, error) {
	c := &Container{Name: rc.Name, Image: rc.Image, Role: role, Raw: map[string]string{}}
	reqs, err := quantities(rc.Resources.Requests)
	if err != nil {
		return nil, fmt.Errorf("container %q requests: %w", rc.Name, err)
	}
	lims, err := quantities(rc.Resources.Limits)
	if err != nil {
		return nil, fmt.Errorf("container %q limits: %w", rc.Name, err)
	}
	pair := func(name string, conv func(resource.Quantity) int64) Pair {
		var p Pair
		rq, hasReq := reqs[name]
		lq, hasLim := lims[name]
		if hasLim {
			p.Lim.Value, p.Lim.Units = conv(lq.q), lq.units
		} else {
			p.Lim.Missing = 1
		}
		switch {
		case hasReq:
			p.Req.Value, p.Req.Units = conv(rq.q), rq.units
		case hasLim:
			// Kubernetes defaults an omitted request to the limit.
			p.Req, p.ReqFromLim = p.Lim, true
		default:
			p.Req.Missing = 1
		}
		return p
	}
	for k, v := range reqs {
		c.Raw["requests."+k] = v.raw
	}
	for k, v := range lims {
		c.Raw["limits."+k] = v.raw
	}
	milli := func(q resource.Quantity) int64 { return q.MilliValue() }
	bytesOf := func(q resource.Quantity) int64 { return q.Value() }
	c.Resources.CPU = pair("cpu", milli)
	c.Resources.Memory = pair("memory", bytesOf)
	c.Resources.Ephemeral = pair("ephemeral-storage", bytesOf)
	if c.Resources.Ephemeral.Req.Missing > 0 && c.Resources.Ephemeral.Lim.Missing > 0 {
		// Ephemeral storage is rarely set; don't count it as "missing".
		c.Resources.Ephemeral = Pair{}
	}
	for name := range mergeKeys(reqs, lims) {
		if name == "cpu" || name == "memory" || name == "ephemeral-storage" {
			continue
		}
		if c.Resources.Extended == nil {
			c.Resources.Extended = map[string]Pair{}
		}
		p := pair(name, milli)
		p.Req.Missing, p.Lim.Missing = 0, 0
		c.Resources.Extended[name] = p
	}
	return c, nil
}

func mergeKeys(a, b map[string]parsedQuantity) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

type parsedQuantity struct {
	q     resource.Quantity
	units Units
	raw   string
}

// unitsOf classifies the suffix of a quantity string such as "3G" or "512Mi".
func unitsOf(s string) Units {
	switch {
	case strings.HasSuffix(s, "i"):
		return UnitsBinary
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "M"), strings.HasSuffix(s, "G"),
		strings.HasSuffix(s, "T"), strings.HasSuffix(s, "P"), strings.HasSuffix(s, "E"):
		return UnitsDecimal
	}
	return UnitsNone
}

func quantities(in map[string]any) (map[string]parsedQuantity, error) {
	out := make(map[string]parsedQuantity, len(in))
	for k, v := range in {
		var s string
		switch t := v.(type) {
		case nil:
			continue
		case string:
			s = strings.TrimSpace(t)
			if s == "" {
				continue
			}
		case float64:
			s = strconv.FormatFloat(t, 'f', -1, 64)
		default:
			return nil, fmt.Errorf("%s: unsupported value %v", k, v)
		}
		q, err := resource.ParseQuantity(s)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid quantity %q", k, s)
		}
		out[k] = parsedQuantity{q: q, units: unitsOf(s), raw: s}
	}
	return out, nil
}

// splitDocuments splits a YAML stream on "---" separator lines.
func splitDocuments(data []byte) [][]byte {
	var docs [][]byte
	var cur bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	flush := func() {
		if len(bytes.TrimSpace(cur.Bytes())) > 0 {
			docs = append(docs, append([]byte(nil), cur.Bytes()...))
		}
		cur.Reset()
	}
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.HasPrefix(line, []byte("---")) && len(bytes.TrimSpace(line[3:])) == 0 {
			flush()
			continue
		}
		cur.Write(line)
		cur.WriteByte('\n')
	}
	flush()
	return docs
}
