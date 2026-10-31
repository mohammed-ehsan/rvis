package source

import (
	"reflect"
	"testing"
)

func TestTemplateArgs(t *testing.T) {
	r := Request{
		Chart:      "./chart",
		Namespace:  "prod",
		ValueFiles: []string{"a.yaml", "b.yaml"},
		Set:        []string{"x=1"},
		Version:    "1.2.3",
		ExtraArgs:  []string{"--skip-tests"},
	}
	want := []string{"template", "rvis", "./chart", "--namespace", "prod",
		"--values", "a.yaml", "--values", "b.yaml", "--set", "x=1", "--version", "1.2.3", "--skip-tests"}
	if got := r.templateArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("templateArgs()\n got %q\nwant %q", got, want)
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		r    Request
		want string
	}{
		{Request{ManifestFile: "-"}, "stdin"},
		{Request{ManifestFile: "m.yaml"}, "m.yaml"},
		{Request{Release: "app"}, "release app"},
		{Request{Chart: "bitnami/redis"}, "bitnami/redis"},
	} {
		if got := c.r.Describe(); got != c.want {
			t.Errorf("Describe() = %q, want %q", got, c.want)
		}
	}
}
