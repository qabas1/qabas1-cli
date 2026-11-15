MODULE := github.com/qabas1/qabas1-cli
VERSION ?= dev
COMMIT ?= none
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)

.PHONY: build test vet dist dist-linux dist-windows package-deb package-msi windows-assets checksums ci clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/qabas ./cmd/qabas

test:
	go test -count=1 ./...

vet:
	go vet ./...

ci: test vet build
	@echo "ci ok"

dist-linux:
	VERSION=$(VERSION) COMMIT=$(COMMIT) bash scripts/dist.sh

package-deb:
	VERSION=$(VERSION) bash scripts/package-deb.sh

# Windows targets run on Windows (PowerShell + .NET SDK for WiX).
dist-windows:
	powershell -NoProfile -File packaging/windows/build.ps1 -Version $(VERSION) -Commit $(COMMIT)

package-msi:
	powershell -NoProfile -File packaging/windows/build-msi.ps1 -Version $(VERSION)

windows-assets:
	python packaging/windows/assets/generate.py

checksums:
	VERSION=$(VERSION) bash scripts/checksums.sh

dist: dist-linux

clean:
	rm -rf bin dist .tools cmd/qabas/*.syso
