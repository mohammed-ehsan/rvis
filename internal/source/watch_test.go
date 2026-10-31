package source

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchPaths(t *testing.T) {
	dir := t.TempDir()
	vals := filepath.Join(dir, "values.yaml")
	os.WriteFile(vals, nil, 0o644)

	got := WatchPaths(Request{Chart: dir, ValueFiles: []string{vals, "missing.yaml"}, SetFile: []string{"k=" + vals}})
	if len(got) != 3 || got[0] != dir {
		t.Errorf("chart paths = %q", got)
	}
	if got := WatchPaths(Request{Chart: "bitnami/redis"}); len(got) != 0 {
		t.Errorf("remote chart should not be watched: %q", got)
	}
	if got := WatchPaths(Request{Release: "app", Chart: dir}); len(got) != 0 {
		t.Errorf("release should not be watched: %q", got)
	}
	if got := WatchPaths(Request{ManifestFile: "-"}); len(got) != 0 {
		t.Errorf("stdin should not be watched: %q", got)
	}
}

func TestFingerprintChanges(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "templates", "a.yaml")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("a"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)

	base, n := Fingerprint([]string{dir})
	if n != 1 {
		t.Errorf("files = %d, want 1", n)
	}
	if again, _ := Fingerprint([]string{dir}); again != base {
		t.Error("fingerprint is not stable")
	}

	os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("x"), 0o644)
	if fp, _ := Fingerprint([]string{dir}); fp != base {
		t.Error(".git changes should be ignored")
	}

	os.WriteFile(f, []byte("ab"), 0o644)
	edited, _ := Fingerprint([]string{dir})
	if edited == base {
		t.Error("edit not detected")
	}

	later := time.Now().Add(time.Minute)
	os.Chtimes(f, later, later)
	if touched, _ := Fingerprint([]string{dir}); touched == edited {
		t.Error("same-size save not detected")
	}

	os.WriteFile(filepath.Join(dir, "templates", "b.yaml"), nil, 0o644)
	if added, n := Fingerprint([]string{dir}); n != 2 || added == edited {
		t.Error("new file not detected")
	}
}
