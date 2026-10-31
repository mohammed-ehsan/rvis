package edit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/source"
)

func needHelm(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(source.HelmBinary()); err != nil {
		t.Skip("helm not installed")
	}
}

// copyChart copies the demo chart into a temp dir so tests can edit it.
func copyChart(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "shop")
	src := "../../testdata/demo-chart"
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func render(t *testing.T, req source.Request) *model.Report {
	t.Helper()
	data, err := source.Load(req)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := model.Parse(data, model.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func raw(t *testing.T, rep *model.Report, workload, container, field string) (string, bool) {
	t.Helper()
	for _, w := range rep.Workloads {
		if w.Name != workload {
			continue
		}
		for _, c := range w.Containers {
			if c.Name == container {
				v, ok := c.Raw[field]
				return v, ok
			}
		}
	}
	t.Fatalf("%s/%s not found", workload, container)
	return "", false
}

func TestLocateDemoChart(t *testing.T) {
	needHelm(t)
	dir := copyChart(t)
	ed, err := New(source.Request{Chart: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ed.Locate(Target{Kind: "Deployment", Name: "rvis-api"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(f Field, file, path string) {
		t.Helper()
		r := res[f]
		if r.Loc == nil {
			t.Errorf("%v: not located: %s", f, r.Reason)
			return
		}
		if !strings.HasSuffix(r.Loc.File, file) || r.Loc.Path != path {
			t.Errorf("%v: got %s %q, want %s %q", f, r.Loc.File, r.Loc.Path, file, path)
		}
	}
	check(Field{"api", "requests", "cpu"}, "values.yaml", "api.resources.requests.cpu")
	check(Field{"api", "limits", "memory"}, "values.yaml", "api.resources.limits.memory")
	check(Field{"istio-proxy", "limits", "cpu"}, "templates/api.yaml", "")
	check(Field{"migrate", "requests", "memory"}, "templates/api.yaml", "")
	if l := res[Field{"istio-proxy", "limits", "cpu"}].Loc; l != nil && l.Line != 25 {
		t.Errorf("istio-proxy limits.cpu line = %d, want 25", l.Line)
	}
}

func TestApplyReplacesOnlyTheValue(t *testing.T) {
	needHelm(t)
	dir := copyChart(t)
	valuesPath := filepath.Join(dir, "values.yaml")
	before, _ := os.ReadFile(valuesPath)
	req := source.Request{Chart: dir}
	ed, _ := New(req)

	_, err := ed.Apply(Target{Kind: "Deployment", Name: "rvis-api"}, map[Field]string{
		{"api", "limits", "cpu"}:           "2",
		{"istio-proxy", "requests", "cpu"}: "150m",
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(valuesPath)
	want := strings.Replace(string(before), `limits: { cpu: "1", memory: 1Gi }`, `limits: { cpu: "2", memory: 1Gi }`, 1)
	if string(after) != want {
		t.Errorf("values.yaml changed beyond the edited value:\n%s", after)
	}
	rep := render(t, req)
	if v, _ := raw(t, rep, "rvis-api", "api", "limits.cpu"); v != "2" {
		t.Errorf("api limits.cpu = %q, want 2", v)
	}
	if v, _ := raw(t, rep, "rvis-api", "istio-proxy", "requests.cpu"); v != "150m" {
		t.Errorf("istio-proxy requests.cpu = %q, want 150m", v)
	}
}

func TestInsertMissingGroupAndKey(t *testing.T) {
	needHelm(t)
	dir := copyChart(t)
	req := source.Request{Chart: dir}
	ed, _ := New(req)
	target := Target{Kind: "Deployment", Name: "rvis-worker"}

	res, err := ed.Locate(target)
	if err != nil {
		t.Fatal(err)
	}
	// worker has requests only, in a flow map; limits are added next to it.
	if l := res[Field{"worker", "limits", "memory"}].Loc; l == nil || !l.Insert || l.Path != "worker.resources.limits" {
		t.Fatalf("worker limits.memory = %+v / %s", l, res[Field{"worker", "limits", "memory"}].Reason)
	}
	// ml-inference is written in a template: inserting is refused with a reason.
	if r := res[Field{"ml-inference", "limits", "cpu"}]; r.Loc != nil || !strings.Contains(r.Reason, "template") {
		t.Errorf("ml-inference limits.cpu = %+v", r)
	}

	if _, err := ed.Apply(target, map[Field]string{
		{"worker", "limits", "cpu"}:    "1",
		{"worker", "limits", "memory"}: "2Gi",
	}); err != nil {
		t.Fatal(err)
	}
	values, _ := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if !strings.Contains(string(values), "  resources:\n    limits:\n      cpu: 1\n      memory: 2Gi\n    requests: { cpu: 500m, memory: 1Gi }") {
		t.Errorf("unexpected values.yaml:\n%s", values)
	}
	rep := render(t, req)
	if v, _ := raw(t, rep, "rvis-worker", "worker", "limits.memory"); v != "2Gi" {
		t.Errorf("worker limits.memory = %q", v)
	}
}

func TestOverrideFileSharedValueAndBlockInsert(t *testing.T) {
	needHelm(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chart", "Chart.yaml"), "apiVersion: v2\nname: c\nversion: 0.1.0\n")
	write(t, filepath.Join(dir, "chart", "values.yaml"), "# shared defaults\nresources:\n  requests:\n    cpu: 100m # keep this comment\n  limits:\n    memory: 256Mi\nreplicas: 1\n")
	write(t, filepath.Join(dir, "chart", "templates", "d.yaml"), `{{- range $n := list "a" "b" }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ $n }}}
spec:
  replicas: {{ $.Values.replicas }}
  template:
    spec:
      containers:
        - name: app
          resources: {{- toYaml $.Values.resources | nindent 12 }}
{{- end }}
`)
	override := filepath.Join(dir, "prod.yaml")
	write(t, override, "resources:\n  requests:\n    cpu: '250m'\n    memory: 128Mi\n  limits:\n    memory: 256Mi\n")
	req := source.Request{Chart: filepath.Join(dir, "chart"), ValueFiles: []string{override}}
	ed, err := New(req)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Kind: "Deployment", Name: "a"}
	res, err := ed.Locate(target)
	if err != nil {
		t.Fatal(err)
	}
	// The effective cpu request comes from the -f file, not the chart's
	// values.yaml, even though both match (replicas: 1 is a decoy as well).
	l := res[Field{"app", "requests", "cpu"}].Loc
	if l == nil || !strings.HasSuffix(l.File, "prod.yaml") || l.Line != 3 {
		t.Fatalf("requests.cpu location = %+v", l)
	}
	if len(l.Shared) != 1 || !strings.Contains(l.Shared[0], "Deployment/b app requests.cpu") {
		t.Errorf("shared = %v, want Deployment/b", l.Shared)
	}
	if r := res[Field{"app", "limits", "cpu"}]; r.Loc == nil || !r.Loc.Insert {
		t.Errorf("limits.cpu should be insertable into prod.yaml: %+v", r)
	}

	if _, err := ed.Apply(target, map[Field]string{
		{"app", "requests", "cpu"}: "300m",
		{"app", "limits", "cpu"}:   "1",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(override)
	want := "resources:\n  requests:\n    cpu: '300m'\n    memory: 128Mi\n  limits:\n    memory: 256Mi\n    cpu: 1\n"
	if string(got) != want {
		t.Errorf("prod.yaml =\n%s\nwant\n%s", got, want)
	}
	if chartValues, _ := os.ReadFile(filepath.Join(dir, "chart", "values.yaml")); !strings.Contains(string(chartValues), "cpu: 100m # keep this comment") {
		t.Errorf("chart values.yaml should be untouched:\n%s", chartValues)
	}

	if _, err := ed.Apply(target, map[Field]string{{"app", "requests", "cpu"}: "lots"}); err == nil {
		t.Error("invalid quantity accepted")
	}
}

func TestNotEditableSources(t *testing.T) {
	for _, req := range []source.Request{
		{Release: "x"}, {ManifestFile: "-"}, {Chart: "bitnami/redis", Repo: "https://x"}, {Chart: "./no-such-dir"},
	} {
		if _, err := New(req); err == nil {
			t.Errorf("New(%+v) should fail", req)
		}
	}
}

// A values leaf equal to a resource value but used elsewhere (replicas: 2
// next to cpu: 2) must not hide the real source.
func TestDecoyValuesAreIsolated(t *testing.T) {
	needHelm(t)
	dir := copyChart(t)
	values := filepath.Join(dir, "values.yaml")
	data, _ := os.ReadFile(values)
	os.WriteFile(values, []byte(strings.Replace(string(data), `limits: { cpu: "1", memory: 1Gi }`, `limits: { cpu: "2", memory: 1Gi }`, 1)), 0o644)

	ed, _ := New(source.Request{Chart: dir})
	res, err := ed.Locate(Target{Kind: "Deployment", Name: "rvis-api"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Field{{"api", "limits", "cpu"}, {"migrate", "requests", "memory"}, {"istio-proxy", "limits", "memory"}} {
		if res[f].Loc == nil {
			t.Errorf("%v not located: %s", f, res[f].Reason)
		}
	}
	if l := res[Field{"api", "limits", "cpu"}].Loc; l != nil && l.Path != "api.resources.limits.cpu" {
		t.Errorf("api limits.cpu traced to %q", l.Path)
	}
}

// decoyChart builds a chart whose values hold many numbers equal to the
// resource values (replica counts, HPA minimums) next to the real ones, with
// an override file and a non-conventional key. Rendering a marker in any
// replica count drops its workload, which used to exhaust the tracer.
func decoyChart(t *testing.T) (dir string, req source.Request) {
	t.Helper()
	dir = t.TempDir()
	chart := filepath.Join(dir, "chart")
	write(t, filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: decoys\nversion: 0.1.0\n")
	var values, override strings.Builder
	values.WriteString("apps:\n")
	override.WriteString("apps:\n")
	for i := range 12 {
		fmt.Fprintf(&values, "  app%d:\n    replicaCount: 1\n    autoscaling: {minReplicas: 1, maxReplicas: 2}\n    resources:\n      requests: {cpu: 10m, memory: 1Gi}\n      limits: {cpu: 1, memory: 1Gi}\n", i)
		fmt.Fprintf(&override, "  app%d:\n    resources:\n      requests: {cpu: 20m, memory: 1Gi}\n      limits: {cpu: 1, memory: 2Gi}\n", i)
	}
	values.WriteString("odd:\n  replicaCount: 1\n  cpuCeiling: 1\n  shared: 2\n")
	write(t, filepath.Join(chart, "values.yaml"), values.String())
	write(t, filepath.Join(dir, "stg.yaml"), override.String())
	write(t, filepath.Join(chart, "templates", "apps.yaml"), `{{- range $name, $a := .Values.apps }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ $name }}}
spec:
  replicas: {{ $a.replicaCount }}
  template:
    spec:
      containers:
        - name: main
          resources: {{- toYaml $a.resources | nindent 12 }}
{{- end }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: odd}
spec:
  replicas: {{ .Values.odd.replicaCount }}
  template:
    spec:
      containers:
        - name: main
          resources:
            limits: {cpu: {{ .Values.odd.cpuCeiling }}, memory: 1Gi}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: tied}
spec:
  replicas: {{ .Values.odd.shared }}
  template:
    spec:
      containers:
        - name: main
          resources:
            requests: {cpu: {{ .Values.odd.shared }}}
---
apiVersion: v1
kind: Pod
metadata: {name: bare}
spec:
  containers: [{name: wget, image: busybox}]
`)
	return dir, source.Request{Chart: chart, ValueFiles: []string{filepath.Join(chart, "values.yaml"), filepath.Join(dir, "stg.yaml")}}
}

func TestDecoysAndOverridePrecedence(t *testing.T) {
	needHelm(t)
	_, req := decoyChart(t)
	ed, err := New(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app0", "app7", "app11"} {
		res, err := ed.Locate(Target{Kind: "Deployment", Name: name})
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range Groups {
			for _, r := range Resources {
				f := Field{"main", g, r}
				l := res[f].Loc
				if l == nil {
					t.Errorf("%s %v: not located: %s", name, f, res[f].Reason)
					continue
				}
				// Every effective value lives in the override file; the
				// same paths in values.yaml are shadowed.
				if !strings.HasSuffix(l.File, "stg.yaml") || l.Path != "apps."+name+".resources."+g+"."+r {
					t.Errorf("%s %v: located at %s %s", name, f, l.File, l.Path)
				}
			}
		}
	}

	// A key that is not shaped like a resource is still found by the
	// fallback pass, among the replicaCount decoys.
	res, err := ed.Locate(Target{Kind: "Deployment", Name: "odd"})
	if err != nil {
		t.Fatal(err)
	}
	if l := res[Field{"main", "limits", "cpu"}].Loc; l == nil || l.Path != "odd.cpuCeiling" {
		t.Errorf("odd limits.cpu = %+v (%s)", l, res[Field{"main", "limits", "cpu"}].Reason)
	}
}

func TestUntraceableValuesSayWhy(t *testing.T) {
	needHelm(t)
	_, req := decoyChart(t)
	ed, _ := New(req)

	// odd.shared feeds both replicas and the cpu request: a marker there
	// always breaks the render, so it cannot be traced, and the reason
	// must say so rather than blame the template.
	res, err := ed.Locate(Target{Kind: "Deployment", Name: "tied"})
	if err != nil {
		t.Fatal(err)
	}
	if r := res[Field{"main", "requests", "cpu"}]; r.Loc != nil || !strings.Contains(r.Reason, "could not be traced") {
		t.Errorf("tied requests.cpu = %+v", r)
	}

	res, err = ed.Locate(Target{Kind: "Pod", Name: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if r := res[Field{"wget", "limits", "memory"}]; r.Loc != nil || !strings.Contains(r.Reason, "sets no resources anywhere") {
		t.Errorf("bare limits.memory = %+v", r)
	}
}

// sharedChart renders two Deployments from one resources block.
func sharedChart(t *testing.T) (string, source.Request) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: c\nversion: 0.1.0\n")
	write(t, filepath.Join(dir, "values.yaml"), "resources:\n  requests:\n    cpu: 100m\n    memory: 64Mi\n  limits:\n    memory: 128Mi\nsolo:\n  requests: {cpu: 5m, memory: 8Mi}\n")
	write(t, filepath.Join(dir, "templates", "d.yaml"), `{{- range $n := list "a" "b" }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: {{ $n }}}
spec:
  template:
    spec:
      containers:
        - name: app
          resources: {{- toYaml $.Values.resources | nindent 12 }}
{{- end }}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: solo}
spec:
  template:
    spec:
      containers:
        - name: app
          resources: {{- toYaml .Values.solo | nindent 12 }}
`)
	return dir, source.Request{Chart: dir}
}

func TestApplyAllAcrossWorkloads(t *testing.T) {
	needHelm(t)
	dir, req := sharedChart(t)
	ed, _ := New(req)
	a, b, solo := Target{Kind: "Deployment", Name: "a"}, Target{Kind: "Deployment", Name: "b"}, Target{Kind: "Deployment", Name: "solo"}
	valuesPath := filepath.Join(dir, "values.yaml")
	before, _ := os.ReadFile(valuesPath)

	// Conflicting values for one shared source: rejected, nothing written.
	_, err := ed.ApplyAll(map[Target]map[Field]string{
		a:    {{"app", "requests", "cpu"}: "200m"},
		b:    {{"app", "requests", "cpu"}: "300m"},
		solo: {{"app", "requests", "cpu"}: "10m"},
	})
	if err == nil || !strings.Contains(err.Error(), "share one value") {
		t.Fatalf("expected a shared-value conflict, got %v", err)
	}
	if now, _ := os.ReadFile(valuesPath); string(now) != string(before) {
		t.Fatal("a rejected save must not write anything")
	}

	// Agreeing values: one write per source, and the shared missing cpu
	// limit is inserted once, not once per workload.
	summary, err := ed.ApplyAll(map[Target]map[Field]string{
		a:    {{"app", "requests", "cpu"}: "200m", {"app", "limits", "cpu"}: "1"},
		b:    {{"app", "requests", "cpu"}: "200m", {"app", "limits", "cpu"}: "1"},
		solo: {{"app", "requests", "memory"}: "16Mi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(valuesPath)
	want := "resources:\n  requests:\n    cpu: 200m\n    memory: 64Mi\n  limits:\n    memory: 128Mi\n    cpu: 1\nsolo:\n  requests: {cpu: 5m, memory: 16Mi}\n"
	if string(got) != want {
		t.Errorf("values.yaml =\n%s\nwant\n%s", got, want)
	}
	if len(summary) != 3 {
		t.Errorf("summary = %q, want 3 writes", summary)
	}
	rep := render(t, req)
	for _, n := range []string{"a", "b"} {
		if v, _ := raw(t, rep, n, "app", "limits.cpu"); v != "1" {
			t.Errorf("%s limits.cpu = %q", n, v)
		}
	}
}

func TestLocateAndApplyReplicas(t *testing.T) {
	needHelm(t)
	dir := copyChart(t)
	req := source.Request{Chart: dir}
	ed, _ := New(req)
	cases := []struct {
		kind, name, file, path string
	}{
		{"Deployment", "rvis-frontend", "values.yaml", "frontend.replicas"},
		{"Deployment", "rvis-worker", "values.yaml", "worker.replicas"},
		// HPA min (4) is above spec.replicas (2): the minimum decides.
		{"Deployment", "rvis-api", "values.yaml", "api.autoscaling.min"},
		{"StatefulSet", "rvis-postgres", "templates/postgres.yaml", ""},
		{"CronJob", "rvis-report", "templates/cron.yaml", ""},
	}
	for _, c := range cases {
		res, err := ed.Locate(Target{Kind: c.kind, Name: c.name})
		if err != nil {
			t.Fatal(err)
		}
		l := res[ReplicasField].Loc
		if l == nil || !strings.HasSuffix(l.File, c.file) || l.Path != c.path {
			t.Errorf("%s replicas: %+v (%s)", c.name, l, res[ReplicasField].Reason)
		}
	}
	res, _ := ed.Locate(Target{Kind: "DaemonSet", Name: "rvis-log-agent"})
	if r := res[ReplicasField]; r.Loc != nil || !strings.Contains(r.Reason, "one pod per node") {
		t.Errorf("daemonset replicas = %+v", r)
	}

	_, err := ed.ApplyAll(map[Target]map[Field]string{
		{Kind: "Deployment", Name: "rvis-frontend"}: {ReplicasField: "5"},
		{Kind: "Deployment", Name: "rvis-api"}:      {ReplicasField: "6"},
		{Kind: "CronJob", Name: "rvis-report"}:      {ReplicasField: "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := render(t, req)
	got := map[string]int64{}
	for _, w := range rep.Workloads {
		got[w.Name] = w.Replicas
	}
	if got["rvis-frontend"] != 5 || got["rvis-api"] != 6 || got["rvis-report"] != 3 {
		t.Errorf("replicas after apply = %v", got)
	}
	if _, err := ed.Apply(Target{Kind: "Deployment", Name: "rvis-frontend"}, map[Field]string{ReplicasField: "two"}); err == nil {
		t.Error("non-integer replicas accepted")
	}
}

// Charts often loop over the replica count; markers must stay small enough
// for that to render quickly, and resource candidates equal to the count
// (cpu: 3 next to replicaCount: 3) must not be mistaken for it.
func TestReplicasWithLoopsAndDecoys(t *testing.T) {
	needHelm(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: loop\nversion: 0.1.0\n")
	write(t, filepath.Join(dir, "values.yaml"), "cluster:\n  replicaCount: 3\n  resources:\n    requests: {cpu: 3, memory: 1Gi}\nother:\n  size: 3\n")
	write(t, filepath.Join(dir, "templates", "sts.yaml"), `apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db}
spec:
  replicas: {{ .Values.cluster.replicaCount }}
  template:
    spec:
      containers:
        - name: db
          resources: {{- toYaml .Values.cluster.resources | nindent 12 }}
{{- range $i := until (int .Values.cluster.replicaCount) }}
---
apiVersion: v1
kind: Service
metadata: {name: db-{{ $i }}}
spec: {ports: [{port: 80}]}
{{- end }}
`)
	ed, _ := New(source.Request{Chart: dir})
	start := time.Now()
	res, err := ed.Locate(Target{Kind: "StatefulSet", Name: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if l := res[ReplicasField].Loc; l == nil || l.Path != "cluster.replicaCount" {
		t.Errorf("replicas = %+v (%s)", l, res[ReplicasField].Reason)
	}
	if l := res[Field{"db", "requests", "cpu"}].Loc; l == nil || l.Path != "cluster.resources.requests.cpu" {
		t.Errorf("cpu request = %+v", l)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("locating took %s", d)
	}
}

func TestPackagedSubchartReason(t *testing.T) {
	needHelm(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	write(t, filepath.Join(sub, "Chart.yaml"), "apiVersion: v2\nname: cache\nversion: 1.0.0\n")
	write(t, filepath.Join(sub, "values.yaml"), "count: 2\nresources: {requests: {cpu: 50m}}\n")
	write(t, filepath.Join(sub, "templates", "sts.yaml"), `apiVersion: apps/v1
kind: StatefulSet
metadata: {name: cache}
spec:
  replicas: {{ .Values.count }}
  template:
    spec:
      containers:
        - name: cache
          resources: {{- toYaml .Values.resources | nindent 12 }}
`)
	parent := filepath.Join(dir, "parent")
	write(t, filepath.Join(parent, "Chart.yaml"), "apiVersion: v2\nname: parent\nversion: 1.0.0\n")
	write(t, filepath.Join(parent, "values.yaml"), "other: 1\n")
	os.MkdirAll(filepath.Join(parent, "charts"), 0o755)
	if out, err := exec.Command(source.HelmBinary(), "package", sub, "-d", filepath.Join(parent, "charts")).CombinedOutput(); err != nil {
		t.Fatalf("helm package: %v %s", err, out)
	}
	ed, _ := New(source.Request{Chart: parent})
	res, err := ed.Locate(Target{Kind: "StatefulSet", Name: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Field{ReplicasField, {"cache", "requests", "cpu"}} {
		if r := res[f]; r.Loc != nil || !strings.Contains(r.Reason, `packaged subchart "cache"`) || !strings.Contains(r.Reason, `"cache:"`) {
			t.Errorf("%v: %+v", f, r)
		}
	}
}
