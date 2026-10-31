package source

import (
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// WatchPaths lists the local files and directories whose changes alter the
// rendered output: a chart directory or archive, values files, --set-file
// files and a manifest file. Remote charts, releases and stdin yield nothing.
func WatchPaths(r Request) []string {
	var out []string
	add := func(p string) {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	switch {
	case r.ManifestFile == "-":
		return nil
	case r.ManifestFile != "":
		add(r.ManifestFile)
		return out
	case r.Release != "":
		return nil
	}
	if r.Chart != "" && r.Repo == "" {
		add(r.Chart)
	}
	for _, f := range r.ValueFiles {
		add(f)
	}
	for _, kv := range r.SetFile {
		if _, f, ok := strings.Cut(kv, "="); ok {
			add(f)
		}
	}
	return out
}

// Fingerprint summarizes the names, sizes and modification times of every
// file under paths. It changes whenever a file is edited, added or removed.
func Fingerprint(paths []string) (sum uint64, files int) {
	h := fnv.New64a()
	for _, root := range paths {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if name := d.Name(); p != root && (name == ".git" || name == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			files++
			h.Write([]byte(p))
			h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
			h.Write([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)))
			return nil
		})
	}
	return h.Sum64(), files
}
