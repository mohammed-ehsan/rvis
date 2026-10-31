// Package source obtains rendered Kubernetes manifests from a chart, a
// deployed release, a file, or stdin.
package source

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Request describes where manifests come from.
type Request struct {
	Chart        string // chart path, repo/chart or OCI reference
	ReleaseName  string // name used for `helm template`
	Release      string // deployed release to inspect with `helm get manifest`
	ManifestFile string // pre-rendered manifests; "-" reads stdin
	Namespace    string
	KubeContext  string
	Kubeconfig   string
	ValueFiles   []string
	Set          []string
	SetString    []string
	SetFile      []string
	SetJSON      []string
	Version      string
	Repo         string
	Devel        bool
	KubeVersion  string
	APIVersions  []string
	ExtraArgs    []string // passed verbatim to helm after "--"
	Stdin        io.Reader
}

// Describe returns a short human label for the request, used in headers.
func (r Request) Describe() string {
	switch {
	case r.ManifestFile == "-":
		return "stdin"
	case r.ManifestFile != "":
		return r.ManifestFile
	case r.Release != "":
		return "release " + r.Release
	}
	return r.Chart
}

// Load returns the rendered manifests for the request.
func Load(r Request) ([]byte, error) {
	switch {
	case r.ManifestFile == "-":
		in := r.Stdin
		if in == nil {
			in = os.Stdin
		}
		return io.ReadAll(in)
	case r.ManifestFile != "":
		return os.ReadFile(r.ManifestFile)
	case r.Release != "":
		args := []string{"get", "manifest", r.Release}
		args = append(args, r.connectionArgs()...)
		return runHelm(args)
	case r.Chart != "":
		return runHelm(r.templateArgs())
	}
	return nil, fmt.Errorf("nothing to inspect: pass a chart, --release or --manifest")
}

func (r Request) connectionArgs() []string {
	var a []string
	if r.Namespace != "" {
		a = append(a, "--namespace", r.Namespace)
	}
	if r.KubeContext != "" {
		a = append(a, "--kube-context", r.KubeContext)
	}
	if r.Kubeconfig != "" {
		a = append(a, "--kubeconfig", r.Kubeconfig)
	}
	return a
}

func (r Request) templateArgs() []string {
	name := r.ReleaseName
	if name == "" {
		name = "rvis"
	}
	a := []string{"template", name, r.Chart}
	if r.Namespace != "" {
		a = append(a, "--namespace", r.Namespace)
	}
	if r.KubeContext != "" {
		a = append(a, "--kube-context", r.KubeContext)
	}
	if r.Kubeconfig != "" {
		a = append(a, "--kubeconfig", r.Kubeconfig)
	}
	multi := func(flag string, vals []string) {
		for _, v := range vals {
			a = append(a, flag, v)
		}
	}
	multi("--values", r.ValueFiles)
	multi("--set", r.Set)
	multi("--set-string", r.SetString)
	multi("--set-file", r.SetFile)
	multi("--set-json", r.SetJSON)
	multi("--api-versions", r.APIVersions)
	if r.Version != "" {
		a = append(a, "--version", r.Version)
	}
	if r.Repo != "" {
		a = append(a, "--repo", r.Repo)
	}
	if r.Devel {
		a = append(a, "--devel")
	}
	if r.KubeVersion != "" {
		a = append(a, "--kube-version", r.KubeVersion)
	}
	return append(a, r.ExtraArgs...)
}

// HelmBinary is the helm executable: $HELM_BIN when running as a plugin.
func HelmBinary() string {
	if b := os.Getenv("HELM_BIN"); b != "" {
		return b
	}
	return "helm"
}

func runHelm(args []string) ([]byte, error) {
	bin := HelmBinary()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimPrefix(strings.TrimSpace(stderr.String()), "Error: ")
		if msg == "" {
			msg = err.Error()
		}
		// Helm's message comes first: it is what a reader needs, and the
		// live view's error banner only shows the first line.
		return nil, fmt.Errorf("%s\n\ncommand: %s %s", msg, bin, strings.Join(args, " "))
	}
	return stdout.Bytes(), nil
}
