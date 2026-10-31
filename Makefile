VERSION ?= $(shell sed -n 's/^version: *"\{0,1\}\([^"]*\)"\{0,1\}$$/\1/p' plugin.yaml)
LDFLAGS := -s -w -X github.com/mohammed-ehsan/rvis/cmd.Version=$(VERSION)

.PHONY: build test lint demo install install-local package uninstall fixtures clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/rvis .

test:
	go test ./...

lint:
	gofmt -l . | (! grep .)
	go vet ./...

# Opens the interactive view on the bundled demo chart.
demo: build
	HELM_BIN=$${HELM_BIN:-helm} ./bin/rvis testdata/demo-chart

# Installs from this checkout by linking it (the hook builds bin/rvis).
install: build
	helm plugin install .

# Builds and copies a self-contained plugin into Helm's plugin directory:
# no GitHub, no network, keeps working after `make clean`.
install-local:
	sh scripts/install-local.sh

# Writes dist/rvis_<version>_<os>_<arch>.tar.gz to install elsewhere
# offline. Set GOOS/GOARCH to package for another platform.
package:
	sh scripts/install-local.sh --package

uninstall:
	helm plugin uninstall rvis

# Re-renders the test fixture after editing testdata/demo-chart.
fixtures:
	helm template rvis testdata/demo-chart > testdata/demo.yaml

clean:
	rm -rf bin dist
