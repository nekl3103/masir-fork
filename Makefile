.PHONY: proto proto-lint build snapshot test test-race lima-test gui gui-local gui-dev android screenshots

ANALYTICS_PKG := github.com/Gozargah/Masir/libs/masir-daemon/internal/analytics
-include .env
export MASIR_POSTHOG_KEY MASIR_POSTHOG_HOST MASIR_UPDATE_PUBKEY MYGO_UPDATER_PRIVATE_KEY MINISIGN_SECRET_KEY MASIR_SIGN_COMPONENTS
UPDATE_PKG := github.com/Gozargah/Masir/libs/masir-daemon/internal/update
LDFLAGS := -X $(ANALYTICS_PKG).apiKey=$(MASIR_POSTHOG_KEY) -X $(ANALYTICS_PKG).host=$(MASIR_POSTHOG_HOST) -X $(UPDATE_PKG).publicKeys=$(MASIR_UPDATE_PUBKEY)

proto:
	cd libs/masir-proto && buf generate

proto-lint:
	cd libs/masir-proto && buf lint && buf breaking --against '../../.git#subdir=libs/masir-proto,branch=master'

# masir is a single executable: `masir daemon run` is the daemon.
build:
	sh scripts/with-xray.sh go build -trimpath -ldflags "$(LDFLAGS)" -o ./dist/masir ./apps/masir

# All release archives, as CI builds them, into build/release (needs network for geo data).
snapshot:
	sh scripts/with-xray.sh go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean

# The module checks CI runs, one module at a time. `test-race` adds the race detector.
test:
	sh scripts/test-modules.sh

# The same with the race detector, for the final check before committing.
test-race:
	sh scripts/test-modules.sh -race

# Linux checks in Lima VMs: Ubuntu (the Debian family), Arch and Fedora with SELinux
# enforcing. Installs the service in each and verifies the unit's capabilities, the
# sandbox, the TUN, the takeover guard and SELinux. Boots VMs and downloads cloud
# images the first time, so it is not part of `make test`.
#   make lima-test
#   make lima-test LIMA_FLAGS="--geo --only arch,fedora"
lima-test:
	@bash scripts/lima-test.sh $(LIMA_FLAGS)

# Pictures of the desktop app's pages, for looking at them without launching it. This is
# one headless test on this machine, not the all-platform release build `make snapshot`.
SNAPSHOT_DIR ?= $(CURDIR)/dist/snapshots
screenshots:
	@mkdir -p "$(SNAPSHOT_DIR)"
	cd $(GUI_DIR) && MASIR_SNAPSHOTS="$(SNAPSHOT_DIR)" go test -count=1 -run TestSnapshots -v
	@echo "Screenshots in $(SNAPSHOT_DIR)"

# The desktop app, with the masir it ships, for the systems and architectures chosen.
# By default it builds all three systems and both architectures. Narrow it with either
# filter, or both for one build:
#
#   make gui
#   make gui GUI_OS=darwin
#   make gui GUI_ARCHES=arm64
#   make gui GUI_OS=windows GUI_ARCHES=arm64
#
# Outputs go to apps/masir-gui/dist/<os>-<arch>.
GUI_DIR := apps/masir-gui
# Resolve the default version in build-gui.sh while holding its build lock.
GUI_VERSION ?=
GUI_OS ?= darwin linux windows
GUI_ARCHES ?= amd64 arm64

gui:
	@GUI_OS="$(GUI_OS)" GUI_ARCHES="$(GUI_ARCHES)" GUI_VERSION="$(GUI_VERSION)" sh scripts/build-gui.sh

# The desktop app for this machine only: the fast build while developing.
GUI_LOCAL_OS := $(shell go env GOOS)
GUI_LOCAL_ARCH := $(shell go env GOARCH)
gui-local:
	@GUI_OS="$(GUI_LOCAL_OS)" GUI_ARCHES="$(GUI_LOCAL_ARCH)" GUI_VERSION="$(GUI_VERSION)" sh scripts/build-gui.sh

# Build an Android APK for ARM64 and x86_64. The SDK and NDK must be installed.
android:
	@sh scripts/build-android.sh

# Runs the desktop app from source, rebuilding as files change.
gui-dev: build
	@sh scripts/dev-gui.sh
