package model

import (
	"os"
	"testing"
)

func mustParse(t *testing.T, y string) *Report {
	t.Helper()
	rep, err := Parse([]byte(y), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func find(t *testing.T, rep *Report, name string) *Workload {
	t.Helper()
	for _, w := range rep.Workloads {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("workload %q not found", name)
	return nil
}

func TestEffectivePodWithInitAndSidecar(t *testing.T) {
	rep := mustParse(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  replicas: 3
  template:
    spec:
      initContainers:
        - name: big-init
          resources: {requests: {cpu: "2", memory: 64Mi}, limits: {cpu: "2", memory: 64Mi}}
        - name: proxy
          restartPolicy: Always
          resources: {requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: 200m, memory: 256Mi}}
        - name: late-init
          resources: {requests: {cpu: 500m, memory: 1Gi}, limits: {cpu: 500m, memory: 1Gi}}
      containers:
        - name: app
          resources: {requests: {cpu: 250m, memory: 256Mi}, limits: {cpu: "1", memory: 512Mi}}
`)
	w := find(t, rep, "web")
	// cpu: max(app 250m + proxy 100m, big-init 2, late-init 500m + proxy 100m) = 2
	if got := w.Pod.CPU.Req.Value; got != 2000 {
		t.Errorf("pod cpu request = %dm, want 2000m", got)
	}
	// memory: max(256Mi+128Mi, 64Mi, 1Gi+128Mi) = 1152Mi
	if got := w.Pod.Memory.Req.Value; got != 1152<<20 {
		t.Errorf("pod memory request = %d, want %d", got, 1152<<20)
	}
	if got := rep.Total.CPU.Req.Value; got != 6000 {
		t.Errorf("total cpu request = %dm, want 6000m", got)
	}
	if w.QoS != QoSBurstable {
		t.Errorf("qos = %s, want Burstable", w.QoS)
	}
	roles := map[string]ContainerRole{}
	for _, c := range w.Containers {
		roles[c.Name] = c.Role
	}
	if roles["proxy"] != RoleSidecar || roles["big-init"] != RoleInit || roles["app"] != RoleApp {
		t.Errorf("roles = %v", roles)
	}
}

func TestRequestDefaultsToLimitAndGuaranteed(t *testing.T) {
	rep := mustParse(t, `
kind: StatefulSet
metadata: {name: db}
spec:
  template:
    spec:
      containers:
        - name: db
          resources: {limits: {cpu: 1.5, memory: 2Gi}}
`)
	w := find(t, rep, "db")
	c := w.Containers[0].Resources
	if c.CPU.Req.Value != 1500 || !c.CPU.ReqFromLim {
		t.Errorf("cpu request = %+v, want 1500m defaulted from limit", c.CPU)
	}
	if w.QoS != QoSGuaranteed {
		t.Errorf("qos = %s, want Guaranteed", w.QoS)
	}
	if w.Replicas != 1 {
		t.Errorf("replicas = %d, want default 1", w.Replicas)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", rep.Warnings)
	}
}

func TestMissingValuesAndWarnings(t *testing.T) {
	rep := mustParse(t, `
kind: Deployment
metadata: {name: a}
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: bounded
          resources: {requests: {cpu: 100m, memory: 100Mi}, limits: {cpu: 50m, memory: 200Mi}}
        - name: open
          resources: {requests: {cpu: 100m}}
`)
	w := find(t, rep, "a")
	if w.Pod.Memory.Lim.Missing != 1 || w.Pod.Memory.Lim.Value != 200<<20 {
		t.Errorf("pod memory limit = %+v, want 200Mi with 1 missing", w.Pod.Memory.Lim)
	}
	if rep.Total.Memory.Lim.Missing != 2 {
		t.Errorf("total missing = %d, want 2 (one per replica)", rep.Total.Memory.Lim.Missing)
	}
	var errs, warns int
	for _, wr := range rep.Warnings {
		switch wr.Severity {
		case SevError:
			errs++
		case SevWarn:
			warns++
		}
	}
	// error: bounded cpu limit < request; warns: open has no memory limit, no memory request
	if errs != 1 || warns != 2 {
		t.Errorf("errors=%d warns=%d, want 1 and 2: %+v", errs, warns, rep.Warnings)
	}
	if rep.Warnings[0].Severity != SevError {
		t.Errorf("warnings not sorted by severity")
	}
}

func TestKindsHPAAndDaemonSets(t *testing.T) {
	y := `
kind: Deployment
metadata: {name: api}
spec:
  template: {spec: {containers: [{name: c, resources: {requests: {cpu: 1}}}]}}
---
kind: HorizontalPodAutoscaler
metadata: {name: api}
spec: {scaleTargetRef: {kind: Deployment, name: api}, minReplicas: 3, maxReplicas: 10}
---
kind: DaemonSet
metadata: {name: agent}
spec:
  template: {spec: {containers: [{name: c, resources: {requests: {cpu: 100m}}}]}}
---
kind: CronJob
metadata: {name: cron}
spec:
  jobTemplate: {spec: {parallelism: 4, template: {spec: {containers: [{name: c, resources: {requests: {cpu: 10m}}}]}}}}
---
kind: Pod
metadata: {name: pod}
spec: {containers: [{name: c}]}
---
kind: List
items:
  - kind: Job
    metadata: {name: job}
    spec: {template: {spec: {containers: [{name: c}]}}}
---
kind: ConfigMap
metadata: {name: ignored}
data: {a: b}
`
	rep, err := Parse([]byte(y), ParseOptions{Nodes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Workloads) != 5 {
		t.Fatalf("got %d workloads, want 5", len(rep.Workloads))
	}
	api := find(t, rep, "api")
	if api.Replicas != 3 || api.HPA == nil || api.MaxReplicas() != 10 {
		t.Errorf("api replicas=%d hpa=%+v", api.Replicas, api.HPA)
	}
	if ds := find(t, rep, "agent"); ds.Replicas != 5 || !ds.PerNode {
		t.Errorf("daemonset replicas=%d perNode=%v", ds.Replicas, ds.PerNode)
	}
	if cj := find(t, rep, "cron"); cj.Replicas != 4 {
		t.Errorf("cronjob parallelism = %d, want 4", cj.Replicas)
	}
	if find(t, rep, "pod").QoS != QoSBestEffort {
		t.Errorf("pod without resources should be BestEffort")
	}
	if rep.TotalMax.CPU.Req.Value != 10000+500+40 {
		t.Errorf("total at max = %d", rep.TotalMax.CPU.Req.Value)
	}
}

func TestInvalidQuantity(t *testing.T) {
	_, err := Parse([]byte(`
kind: Deployment
metadata: {name: bad}
spec: {template: {spec: {containers: [{name: c, resources: {requests: {cpu: lots}}}]}}}
`), ParseOptions{})
	if err == nil {
		t.Fatal("expected an error for an invalid quantity")
	}
}

func TestDemoChart(t *testing.T) {
	data, err := os.ReadFile("../../testdata/demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Parse(data, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chart != "shop-1.4.0" || rep.Title != "shop" {
		t.Errorf("chart=%q title=%q", rep.Chart, rep.Title)
	}
	if len(rep.Workloads) != 6 || rep.Pods != 13 {
		t.Errorf("workloads=%d pods=%d", len(rep.Workloads), rep.Pods)
	}
	if got := rep.Total.CPU.Req.Value; got != 9580 {
		t.Errorf("total cpu request = %dm, want 9580m", got)
	}
}

func TestDecimalMemoryUnits(t *testing.T) {
	rep := mustParse(t, `
kind: Deployment
metadata: {name: svc}
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: app
          resources: {requests: {memory: 2G}, limits: {memory: 3G}}
        - name: log
          resources: {requests: {memory: 500M}, limits: {memory: 1G}}
---
kind: Deployment
metadata: {name: other}
spec:
  template:
    spec:
      containers:
        - name: app
          resources: {requests: {memory: 1Gi}, limits: {memory: 1Gi}}
`)
	svc := find(t, rep, "svc")
	lim := svc.Containers[0].Resources.Memory.Lim
	if lim.Value != 3_000_000_000 || lim.Units != UnitsDecimal {
		t.Errorf("3G parsed as %+v, want 3e9 bytes decimal", lim)
	}
	if svc.Pod.Memory.Lim.Units != UnitsDecimal || svc.Total().Memory.Lim.Units != UnitsDecimal {
		t.Errorf("sums of decimal values should stay decimal")
	}
	if rep.Total.Memory.Lim.Units != UnitsMixed {
		t.Errorf("decimal + binary total = %v, want mixed", rep.Total.Memory.Lim.Units)
	}
	if u := find(t, rep, "other").Pod.Memory.Lim.Units; u != UnitsBinary {
		t.Errorf("1Gi units = %v, want binary", u)
	}
	var found bool
	for _, w := range rep.Warnings {
		if w.Container == "app" && w.Workload == "Deployment/svc" {
			found = w.Message == "memory uses decimal units: 3G = 3,000,000,000 bytes = 2.8Gi (use Mi/Gi for powers of 1024)"
		}
	}
	if !found {
		t.Errorf("missing decimal-units finding: %+v", rep.Warnings)
	}
}

func TestPreview(t *testing.T) {
	rep := mustParse(t, `
kind: Deployment
metadata: {name: web}
spec:
  replicas: 3
  template:
    spec:
      initContainers:
        - name: init
          resources: {requests: {cpu: 1, memory: 64Mi}}
      containers:
        - name: app
          resources: {requests: {cpu: 250m, memory: 256Mi}, limits: {memory: 512Mi}}
        - name: side
          resources: {limits: {cpu: 100m, memory: 32Mi}}
`)
	key := WorkloadKey(rep.Workloads[0])
	prev, errs := rep.Preview(map[Override]string{
		{key, "init", "requests.cpu"}:   "100m",
		{key, "app", "requests.memory"}: "1G",
		{key, "side", "limits.cpu"}:     "200m",
		{key, "app", "limits.cpu"}:      "fast",
	})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one invalid quantity", errs)
	}
	w := prev.Workloads[0]
	// The init container no longer dominates: 250m + side's request, which
	// follows its new limit (request omitted) = 450m per pod.
	if w.Pod.CPU.Req.Value != 450 || prev.Total.CPU.Req.Value != 1350 {
		t.Errorf("pod cpu = %dm total = %dm, want 450m and 1350m", w.Pod.CPU.Req.Value, prev.Total.CPU.Req.Value)
	}
	if got := w.Containers[1].Resources.Memory.Req; got.Value != 1_000_000_000 || got.Units != UnitsDecimal {
		t.Errorf("app memory request = %+v", got)
	}
	// The original report is untouched.
	if rep.Total.CPU.Req.Value != 3000 || rep.Workloads[0].Containers[0].Resources.CPU.Req.Value != 1000 {
		t.Errorf("original modified: total %dm", rep.Total.CPU.Req.Value)
	}
}
