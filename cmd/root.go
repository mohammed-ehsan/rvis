// Package cmd wires the rvis command line.
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mohammed-ehsan/rvis/internal/edit"
	"github.com/mohammed-ehsan/rvis/internal/model"
	"github.com/mohammed-ehsan/rvis/internal/source"
	"github.com/mohammed-ehsan/rvis/internal/ui"
)

// Version is set at build time with -ldflags "-X github.com/mohammed-ehsan/rvis/cmd.Version=...".
var Version = "dev"

// ErrFindings is returned when --fail-on matched; main exits with code 2.
var ErrFindings = errors.New("findings at or above the --fail-on level")

type options struct {
	req      source.Request
	output   string
	sort     string
	flat     bool
	perPod   bool
	cpu      string
	memory   string
	nodes    int64
	width    int
	color    string
	failOn   string
	debug    bool
	watch    bool
	interval time.Duration
	showVers bool
}

// New builds the root command.
func New() *cobra.Command {
	o := &options{}
	c := &cobra.Command{
		Use:   "rvis [NAME] [CHART] [flags]",
		Short: "Visualize the CPU and memory requests and limits of a Helm chart",
		Long: `rvis renders a Helm chart (or reads a deployed release or a manifest file) and
shows every workload and container with its CPU and memory requests and limits,
the effective per-pod footprint, replica-weighted totals, QoS classes and
configuration findings, in an htop-style terminal view.`,
		Example: `  # Inspect a local chart with custom values
  helm rvis ./mychart -f values-prod.yaml --set replicaCount=3

  # A chart from a repository, sized against an 8 core / 32Gi node
  helm rvis bitnami/redis --cpu 8 --memory 32Gi

  # A release that is already installed
  helm rvis --release my-app -n production

  # Pre-rendered manifests, printed as a table (e.g. in CI)
  helm template my-app ./mychart | helm rvis -o table --fail-on error`,
		Args:          cobra.MaximumNArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.showVers {
				fmt.Println("rvis", Version)
				return nil
			}
			return run(o, args)
		},
	}

	f := c.Flags()
	f.StringVar(&o.req.Release, "release", "", "inspect an installed release (uses `helm get manifest`)")
	f.StringVar(&o.req.ManifestFile, "manifest", "", "read rendered manifests from a file, or - for stdin")
	f.StringSliceVarP(&o.req.ValueFiles, "values", "f", nil, "values file(s) passed to helm template")
	f.StringArrayVar(&o.req.Set, "set", nil, "set values on the command line (key=value)")
	f.StringArrayVar(&o.req.SetString, "set-string", nil, "set STRING values on the command line")
	f.StringArrayVar(&o.req.SetFile, "set-file", nil, "set values from files")
	f.StringArrayVar(&o.req.SetJSON, "set-json", nil, "set JSON values on the command line")
	f.StringVar(&o.req.Version, "version", "", "chart version constraint")
	f.StringVar(&o.req.Repo, "repo", "", "chart repository url")
	f.BoolVar(&o.req.Devel, "devel", false, "use development chart versions")
	f.StringVar(&o.req.KubeVersion, "kube-version", "", "Kubernetes version used for Capabilities.KubeVersion")
	f.StringSliceVarP(&o.req.APIVersions, "api-versions", "a", nil, "Kubernetes api versions used for Capabilities.APIVersions")
	f.StringVarP(&o.req.Namespace, "namespace", "n", os.Getenv("HELM_NAMESPACE"), "namespace")
	f.StringVar(&o.req.KubeContext, "kube-context", os.Getenv("HELM_KUBECONTEXT"), "kubeconfig context")
	f.StringVar(&o.req.Kubeconfig, "kubeconfig", "", "path to the kubeconfig file")

	f.StringVarP(&o.output, "output", "o", "auto", "output: auto, tui, table or json")
	f.StringVar(&o.sort, "sort", "cpu-req", "sort by: cpu-req, cpu-lim, mem-req, mem-lim, pods, name, kind, manifest")
	f.BoolVar(&o.flat, "flat", false, "show workloads only, without their containers")
	f.BoolVar(&o.perPod, "per-pod", false, "show a single pod per workload instead of pod × replicas")
	f.StringVar(&o.cpu, "cpu", "", "CPU capacity to size the header meter against (e.g. 8 or 7500m)")
	f.StringVar(&o.memory, "memory", "", "memory capacity to size the header meter against (e.g. 32Gi)")
	f.Int64Var(&o.nodes, "nodes", 1, "number of nodes running each DaemonSet")
	f.IntVar(&o.width, "width", 0, "output width for -o table (default: terminal width or 120)")
	f.StringVar(&o.color, "color", "auto", "color: auto, always or never")
	f.StringVar(&o.failOn, "fail-on", "none", "exit with code 2 on findings: none, warn or error")
	f.BoolVar(&o.watch, "watch", true, "in the interactive view, reload when the chart, values or manifest files change")
	f.DurationVar(&o.interval, "watch-interval", 500*time.Millisecond, "how often watched files are checked")
	f.BoolVar(&o.debug, "debug", false, "accepted for Helm compatibility")
	f.BoolVar(&o.showVers, "rvis-version", false, "print the rvis version")
	_ = f.MarkHidden("debug")
	return c
}

func run(o *options, args []string) error {
	switch len(args) {
	case 1:
		o.req.Chart = args[0]
	case 2:
		o.req.ReleaseName, o.req.Chart = args[0], args[1]
	}
	if o.req.Chart == "" && o.req.Release == "" && o.req.ManifestFile == "" {
		if term.IsTerminal(os.Stdin.Fd()) {
			return errors.New("nothing to inspect: pass a CHART, --release NAME or --manifest FILE (see --help)")
		}
		o.req.ManifestFile = "-"
	}

	sortKey, err := ui.ParseSortKey(o.sort)
	if err != nil {
		return err
	}
	var capacity ui.Capacity
	if o.cpu != "" {
		q, err := resource.ParseQuantity(o.cpu)
		if err != nil {
			return fmt.Errorf("--cpu: %w", err)
		}
		capacity.CPU = q.MilliValue()
	}
	if o.memory != "" {
		q, err := resource.ParseQuantity(o.memory)
		if err != nil {
			return fmt.Errorf("--memory: %w", err)
		}
		capacity.Memory = q.Value()
	}
	failLevel, err := parseFailOn(o.failOn)
	if err != nil {
		return err
	}
	if err := setColor(o.color); err != nil {
		return err
	}

	data, err := source.Load(o.req)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%s: no manifests to inspect", o.req.Describe())
	}
	parseOpts := model.ParseOptions{Nodes: o.nodes}
	rep, err := model.Parse(data, parseOpts)
	if err != nil {
		return err
	}

	st := &ui.State{
		Report:   rep,
		Source:   o.req.Describe(),
		PerPod:   o.perPod,
		Flat:     o.flat,
		Sort:     sortKey,
		Capacity: capacity,
		Nodes:    o.nodes,
	}

	out := o.output
	if out == "auto" {
		out = "table"
		if term.IsTerminal(os.Stdout.Fd()) {
			out = "tui"
		}
	}
	switch out {
	case "tui":
		opts := ui.TUIOptions{Watcher: watcher(o, parseOpts), Reload: reloader(o, parseOpts)}
		if ed, editErr := edit.New(o.req); editErr == nil {
			opts.Editor = ed
		} else {
			opts.EditUnavailable = editErr.Error()
		}
		err = ui.RunTUI(st, opts)
	case "table":
		err = ui.RenderStatic(os.Stdout, st, tableWidth(o.width))
	case "json":
		err = ui.RenderJSON(os.Stdout, rep)
	default:
		return fmt.Errorf("unknown output %q (auto, tui, table, json)", o.output)
	}
	if err != nil {
		return err
	}
	for _, w := range rep.Warnings {
		if failLevel >= 0 && w.Severity >= model.Severity(failLevel) {
			return ErrFindings
		}
	}
	return nil
}

// watcher returns nil when there is nothing local to watch (a release,
// stdin or a remote chart) or watching is disabled.
func watcher(o *options, parseOpts model.ParseOptions) *ui.Watcher {
	paths := source.WatchPaths(o.req)
	if !o.watch || len(paths) == 0 {
		return nil
	}
	return &ui.Watcher{
		Fingerprint: func() (uint64, int) { return source.Fingerprint(paths) },
		Reload:      reloader(o, parseOpts),
		Interval:    o.interval,
	}
}

// reloader re-renders the source and parses it into a fresh report.
func reloader(o *options, parseOpts model.ParseOptions) func() (*model.Report, error) {
	return func() (*model.Report, error) {
		data, err := source.Load(o.req)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, fmt.Errorf("%s: no manifests to inspect", o.req.Describe())
		}
		return model.Parse(data, parseOpts)
	}
}

func parseFailOn(s string) (int, error) {
	switch s {
	case "none", "":
		return -1, nil
	case "warn":
		return int(model.SevWarn), nil
	case "error":
		return int(model.SevError), nil
	}
	return 0, fmt.Errorf("--fail-on: unknown level %q (none, warn, error)", s)
}

func setColor(mode string) error {
	switch mode {
	case "always":
		lipgloss.SetColorProfile(termenv.ANSI256)
	case "never":
		lipgloss.SetColorProfile(termenv.Ascii)
	case "auto":
		if os.Getenv("NO_COLOR") != "" {
			lipgloss.SetColorProfile(termenv.Ascii)
		}
	default:
		return fmt.Errorf("--color: unknown mode %q (auto, always, never)", mode)
	}
	return nil
}

func tableWidth(flag int) int {
	if flag > 0 {
		return flag
	}
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	if w, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && w > 0 {
		return w
	}
	return 120
}
