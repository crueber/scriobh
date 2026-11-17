# Scríobh developer and CI entry points. `make help` lists the targets.
#
# `make ci` runs every gate the ci.yml workflow runs, and more: the race detector on the concurrent packages
# and the timing tests, which the push gate leaves to the nightly workflow (nightly-bench.yml), and the Chromium
# steps when a browser is found. The tables in docs/OPERATIONS.md ("Continuous integration") say which workflow
# runs which step. If a step is added here, add it there too.

GO       ?= go
NODE     ?= node
NPM      ?= npm
DOCKER   ?= docker
VERSION  ?= $(shell git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
BIN      := scriobh
CLI_BIN  := scriobh-cli
IMAGE    ?= scriobh:$(VERSION)

# -race needs cgo and a C compiler; use it only when both are present.
RACE := $(shell [ "$$($(GO) env CGO_ENABLED)" = 1 ] && command -v "$$($(GO) env CC)" >/dev/null 2>&1 && echo -race)

# Packages tested at once. Several tests measure time (the budgets in docs/ARCHITECTURE.md, scaled for -race);
# at the default of one package per CPU the race detector saturates the machine and those tests measure the
# machine instead of the code. The default is two, and the tests are written to need no more than a couple of
# cores (pair it with GOMAXPROCS=2, as the nightly race stage does); raise it only on a machine that is otherwise
# idle. The push gate (ci.yml) sets four itself and runs them
# with SCRIOBH_TIMING=off, which makes every test that calls testutil.Timing skip itself (see docs/OPERATIONS.md,
# "Continuous integration"); the nightly workflow runs the plain pass with SCRIOBH_TIMING unset and TEST_PARALLEL=1.
TEST_PARALLEL ?= 2

# The test pass's cache flag. -count=1 makes every test really run; the push gate sets TEST_COUNT to the empty string so
# Go reuses cached results of packages whose inputs did not change (the runner keeps a persistent Go cache). The
# nightly run and a plain local run keep -count=1.
TEST_COUNT ?= -count=1

# The race detector slows the server package's tests by about ten times: its run no longer fits in 30 minutes.
RACE_TIMEOUT ?= 90m

# The packages where goroutines share state: background workers, queues, streams, supervisors and the shared
# authorization layer. The race detector runs on these in `make test` and in CI; a full-tree race pass
# (`make test-race`) takes over two hours on the CI runner and mostly measures timing tests the detector slows down.
RACE_CORE_PKGS := ./internal/mirror ./internal/gitmirror ./internal/events ./internal/webhook ./internal/mail \
	./internal/domain/semantic ./internal/domain/presence ./internal/backup ./internal/domain/projectgate \
	./internal/ops ./internal/idempotency ./internal/pandoc ./internal/domain/aiwork

# Derived files that must match the registry and the editor sources.
SKILL_FILES  := internal/cli/snapshot.json skills internal/mcp/tools.snapshot.json docs/API.md
EDITOR_FILES := web/static/js/editor.js

.DEFAULT_GOAL := help
.PHONY: release-dist actionlint test-plain test-race test-race-core test-race-core-if-available scripts-deps e2e-extra workflows-check editor-deps help build build-dev cli run test vet fmt-check contrast csp-smoke desktop-smoke \
        skill skill-check editor editor-check editor-test ui-check e2e e2e-fast a11y image bench bench-smoke soak \
        restore-rehearsal upgrade-rehearsal clean-cache ci \
        desktop-deps desktop-sidecar desktop-test desktop-dev desktop-shell-smoke desktop-build

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: cli ## Production server (prod tag, no development issuer) at ./scriobh, and the agent CLI at ./scriobh-cli
	CGO_ENABLED=0 $(GO) build -trimpath -tags prod -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/scriobh

build-dev: cli ## Development server (includes the stub issuer) at ./scriobh, and the agent CLI at ./scriobh-cli
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/scriobh

cli: ## The slim agent CLI at ./scriobh-cli (no server or SQLite linked in)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS) -X github.com/crueber/scriobh/internal/cli.Version=$(VERSION)" -o $(CLI_BIN) ./cmd/scriobh-cli

run: build-dev ## Run for development: stub issuer, ephemeral loopback port, data in ./data
	SCRIOBH_OIDC_STUB=1 SCRIOBH_LISTEN=127.0.0.1:0 SCRIOBH_DATA_DIR=./data ./$(BIN)

test: ## The plain go test pass, then the race detector on the concurrent packages when it is available
	@$(MAKE) --no-print-directory test-plain
	@$(MAKE) --no-print-directory test-race-core-if-available

test-race-core-if-available:
	@if [ -n "$(RACE)" ]; then $(MAKE) --no-print-directory test-race-core; else echo "race detector unavailable (needs cgo and a C compiler); the plain pass only"; fi

# The imports pattern below names the limiter and the PDF child process. A bare `Child` once matched
# TestXMLManyChildrenAndAttributesAreNotBuilt, a single-threaded parser test of nine million elements: 69 s of the race pass.
test-race-core: ## The race detector on the packages where goroutines share state (RACE_CORE_PKGS, the import limiter and the server's streams); about 4 min at two cores on a fast laptop
	@test -n "$(RACE)" || { echo "test-race-core: the race detector needs CGO_ENABLED=1 and a C compiler (cc or gcc) on PATH"; exit 1; }
	CI=true $(GO) test -race -short -p $(TEST_PARALLEL) -timeout 45m -count=1 $(RACE_CORE_PKGS)
	CI=true $(GO) test -race -short -timeout 30m -count=1 ./internal/imports -run 'Limiter|PdfChild|ThroughTheChild|Concurren|Slot|Busy'
	CI=true $(GO) test -race -short -timeout 30m -count=1 ./internal/server -run 'Stream|SSE|Presence|Card|Flash|Hub'

test-race: ## The race detector over the whole tree, by hand only (over two hours on the CI runner; timing tests are scaled for it)
	@test -n "$(RACE)" || { echo "test-race: the race detector needs CGO_ENABLED=1 and a C compiler (cc or gcc) on PATH"; exit 1; }
	CI=true $(GO) test -race -p $(TEST_PARALLEL) -timeout $(RACE_TIMEOUT) -count=1 ./...

# Packages whose tests read inputs the Go test cache cannot see (a child `go build` of the
# production binary and of scriobh-cli, or node over the editor bundle): always run, never cached.
UNCACHED_PKGS := ./cmd/... ./internal/editor

test-plain: ## Only the pass without the race detector (the quick one; SCRIOBH_TIMING=off skips the timing tests)
	@pkgs="$$($(GO) list ./... | grep -v -e '/cmd/' -e '/internal/editor$$')" && [ -n "$$pkgs" ] || { echo "test-plain: go list gave no packages" >&2; exit 1; }; \
	echo "CI=true $(GO) test -p $(TEST_PARALLEL) -timeout 30m $(TEST_COUNT) <$$(echo "$$pkgs" | wc -l | tr -d ' ') packages>"; \
	CI=true $(GO) test -p $(TEST_PARALLEL) -timeout 30m $(TEST_COUNT) $$pkgs
	CI=true $(GO) test -p $(TEST_PARALLEL) -timeout 30m -count=1 $(UNCACHED_PKGS)

vet: ## go vet ./...
	$(GO) vet ./...

fmt-check: ## Fail when any Go file is not gofmt-clean, or when git cannot list the files (an empty list must not pass)
	@files="$$(git ls-files '*.go')" || { echo "fmt-check: git ls-files failed"; exit 1; }; \
	[ -n "$$files" ] || { echo "fmt-check: git lists no Go files"; exit 1; }; \
	out="$$(gofmt -l $$files)" || { echo "fmt-check: gofmt failed"; exit 1; }; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

contrast: ## Contrast check over every theme and accent (scripts/check-contrast.py)
	@test -f scripts/check-contrast.py || { echo "scripts/check-contrast.py does not exist yet; it arrives with the design tokens"; exit 1; }
	python3 scripts/check-contrast.py

csp-smoke: ## Walk the pages in Chromium; fail on CSP, console, or outbound-request problems
	@test -f scripts/csp-smoke.mjs || { echo "scripts/csp-smoke.mjs is missing"; exit 1; }
	cd scripts && $(NPM) ci --no-audit --no-fund && $(NODE) csp-smoke.mjs

scripts-deps: ## Install the browser scripts' dependencies (npm ci in scripts/)
	cd scripts && $(NPM) ci --no-audit --no-fund

e2e-fast: scripts-deps ## Library, settings, palette, themes, goals, outline, the writing view's Phase 2 parts, the offline shell, the HTML export, Git sync, the 10,000-word phone walk and the 20,000-word chapter and the alternatives and ghosts and the AI screens against a fake provider, the static site page and the webhooks and connect pages and the Scrivener, WordPress and Ghost import screens and the EPUB, PDF and tracked-changes import screens and the Editions pages and their diff screens and a review of the import screens' defects (joins, undo, narrow screens) and the print options and proof and desktop mode (a production build started as the shell starts it: the first run, the launch gate, nothing leaving the machine) in Chromium, and the accessibility audit of every page family in both themes at 1280 and 390 px (about 17 min with the audit, about 8 of them its own)
	cd scripts && $(NODE) e2e-library.mjs && $(NODE) e2e-pwa.mjs && $(NODE) e2e-export.mjs && $(NODE) e2e-accept.mjs && $(NODE) e2e-long.mjs && $(NODE) e2e-themes.mjs && $(NODE) e2e-goals.mjs && $(NODE) e2e-outline.mjs && $(NODE) e2e-write2.mjs && $(NODE) e2e-alts.mjs && $(NODE) e2e-alts2.mjs && $(NODE) e2e-phase2.mjs && $(NODE) e2e-ai-admin.mjs && $(NODE) e2e-ai-edit.mjs && $(NODE) e2e-ai-chat.mjs && $(NODE) e2e-ai-lab.mjs && $(NODE) e2e-ai-keys.mjs && $(NODE) e2e-review.mjs && $(NODE) e2e-share.mjs && $(NODE) e2e-i18n.mjs && $(NODE) e2e-admin.mjs && $(NODE) e2e-compile.mjs && $(NODE) e2e-git.mjs && $(NODE) e2e-site.mjs && $(NODE) e2e-connect.mjs && $(NODE) e2e-import-formats.mjs && $(NODE) e2e-import-epub.mjs && $(NODE) e2e-import-pdf.mjs && $(NODE) e2e-editions.mjs && $(NODE) e2e-editions-diff.mjs && $(NODE) e2e-review-ui7.mjs && $(NODE) e2e-compile-print.mjs && $(NODE) e2e-mail.mjs && $(NODE) e2e-pandoc.mjs && $(NODE) e2e-publish.mjs && $(NODE) e2e-gate.mjs && $(NODE) e2e-compmanage.mjs && $(NODE) e2e-semantic.mjs && $(NODE) e2e-desktop.mjs && $(NODE) e2e-studio.mjs && $(NODE) e2e-typography.mjs && $(NODE) e2e-a11y.mjs

a11y: ## The accessibility audit alone: every page family, both themes, 1280 and 390 px, keyboard, zoom, motion and forced colours (about 8 min; `cd scripts && node e2e-a11y.mjs --only NAME --dump DIR` for one page and its accessibility tree)
	cd scripts && $(NPM) ci --no-audit --no-fund && $(NODE) e2e-a11y.mjs

e2e: e2e-fast e2e-extra ## Everything in Chromium: e2e-fast, then e2e-extra

e2e-extra: scripts-deps ## The browser scripts e2e-fast leaves out: the save-loop failure paths, the import page, the Phase 3 and alternatives screens (the signed-out retry waits 20 s)
	cd scripts && $(NODE) e2e-write.mjs && $(NODE) e2e-ai-perf.mjs && $(NODE) e2e-import.mjs && $(NODE) e2e-importgit.mjs && $(NODE) e2e-compendium.mjs && $(NODE) e2e-entities.mjs && $(NODE) e2e-research.mjs && $(NODE) e2e-attachments.mjs && $(NODE) e2e-boards.mjs && $(NODE) e2e-timeline.mjs && $(NODE) e2e-live.mjs && $(NODE) e2e-scene.mjs && $(NODE) e2e-mentions.mjs && $(NODE) e2e-sort.mjs && $(NODE) e2e-phase3.mjs && $(NODE) e2e-review3.mjs && $(NODE) e2e-altperf.mjs

workflows-check: ## Parse the CI workflow files with a real YAML parser (a file that does not parse makes every run fail in 0 s)
	./scripts/check-workflows.sh

release-dist: ## The release files of VERSION in ./dist (server and CLI for the fifteen platforms of scripts/release-dist.sh, archives, SHA256SUMS.txt); `make release-dist VERSION=v2.1.0`
	bash scripts/release-dist.sh --version "$(VERSION)" --out dist

actionlint: ## Lint every workflow with a pinned actionlint, installed into a temporary directory (needs Go and the network; scripts/actionlint.sh)
	bash scripts/actionlint.sh

desktop-smoke: ## Run the app on 127.0.0.1:0 with no proxy and no external IdP, then the production build in desktop mode (launch token, first-run profile, embedded issuer, second instance, SIGTERM)
	./scripts/desktop-smoke.sh

skill: ## Regenerate the agent skill, its command reference, the CLI snapshot and the op table of docs/API.md from the op registry
	@test -d cmd/scriobh-skillgen || { echo "cmd/scriobh-skillgen does not exist yet; the skill generator is not in this tree"; exit 1; }
	$(GO) run ./cmd/scriobh-skillgen

skill-check: ## Fail when the committed skill, CLI snapshot or the op table of docs/API.md differ from the registry
	@test -d cmd/scriobh-skillgen || { echo "cmd/scriobh-skillgen does not exist yet; the skill generator is not in this tree"; exit 1; }
	$(MAKE) --no-print-directory skill
	@st="$$(git status --porcelain -- $(SKILL_FILES))" || { echo "skill-check: git status failed"; exit 1; }; \
	if [ -n "$$st" ]; then \
		echo "skill or CLI snapshot is out of date; run 'make skill' and commit:"; \
		echo "$$st"; exit 1; fi

editor-deps: ## Install the editor package's dependencies (npm ci); the tests need them when CI=true
	@test -f web/editor/package.json || { echo "web/editor/package.json does not exist yet"; exit 1; }
	cd web/editor && $(NPM) ci --no-audit --no-fund

editor: ## Build the ProseMirror bundle into web/static/js/editor.js
	@test -f web/editor/package.json || { echo "web/editor/package.json does not exist yet; the editor package is not in this tree"; exit 1; }
	cd web/editor && $(NPM) ci --no-audit --no-fund && $(NPM) run build

editor-check: ## Rebuild the editor bundle and fail when the committed one differs
	$(MAKE) --no-print-directory editor
	@git diff --exit-code -- $(EDITOR_FILES) || { echo "editor bundle is out of date; run 'make editor' and commit"; exit 1; }
	@st="$$(git status --porcelain -- $(EDITOR_FILES))" || { echo "editor-check: git status failed"; exit 1; }; \
	if [ -n "$$st" ]; then echo "editor bundle is not committed"; exit 1; fi

editor-test: ## Editor unit and Chromium tests (needs web/editor node_modules: make editor)
	cd web/editor && npm test && npm run test:browser

ui-check: ## Drive the UI in Chromium and rewrite docs/screens/phase-0 (manual; changes tracked PNGs)
	cd scripts && npm ci --no-audit --no-fund && node ui-check.mjs

image: ## Build the production container image (scriobh:<version>)
	$(DOCKER) build --build-arg TAGS=prod --build-arg VERSION=$(VERSION) -t $(IMAGE) .

# The sizes `make bench` builds. 1m takes a few minutes to build: `make bench BENCH_SIZES=150k,500k,1m`.
BENCH_SIZES ?= 150k,500k

bench: ## Performance gate: build 150k and 500k-word projects, time every budgeted operation, compare with bench/baseline.json (about a minute)
	$(GO) run ./cmd/scriobh bench --sizes $(BENCH_SIZES)

bench-smoke: ## A tiny bench run that proves the command and its report work (seconds)
	$(GO) run ./cmd/scriobh bench --sizes 5k --runs 5 --baseline tmp/no-baseline.json

# How long `make soak` keeps the mix going for each size.
SOAK_DURATION ?= 20m
SOAK_SIZES    ?= 150k

soak: ## Long-run soak: serve a 150k-word project with every worker on for SOAK_DURATION (default 20m) and fail on growth or a missed budget
	mkdir -p tmp && $(GO) run ./cmd/scriobh bench --sizes $(SOAK_SIZES) --soak $(SOAK_DURATION) --out tmp/soak.json

restore-rehearsal: ## Back up a populated instance and restore it on a clean directory, with the same key and another (no network)
	./scripts/restore-rehearsal.sh

upgrade-rehearsal: ## Write data with every release's own binary, then upgrade it with this build (no network)
	./scripts/upgrade-rehearsal.sh --all

# The Go build and test cache grows without bound (every test binary and every race build is kept) and has filled the
# disk of a development machine before. This deletes the cache files not touched for two hours, and keeps what a build
# in progress or one started a moment ago is using. Do not run it while others build or test on the same machine: a file
# a running build is about to read can be removed under it.
clean-cache: ## Trim the Go build cache: delete cache files older than two hours (not while others build)
	find "$$($(GO) env GOCACHE)" -type f -mmin +120 -delete

# ---- The desktop app (desktop/): a Tauri shell around ./scriobh. Needs Rust and, for desktop-build, Node. ----
# These are not part of `make ci`: the server does not depend on the shell. Cargo writes to
# desktop/src-tauri/target (not committed) unless CARGO_TARGET_DIR says otherwise; a debug build is about 2.5 GB.
CARGO ?= cargo
TAURI := $(NODE) node_modules/@tauri-apps/cli/tauri.js
DESKTOP_TARGET := $(if $(CARGO_TARGET_DIR),$(CARGO_TARGET_DIR),desktop/src-tauri/target)

desktop-deps: ## Install the pinned Tauri command line (build-time only; nothing from npm ships in the app)
	cd desktop && $(NPM) ci --no-audit --no-fund

desktop-sidecar: ## Build the production server into desktop/src-tauri/binaries for this machine
	GO="$(GO)" VERSION="$(VERSION)" ./desktop/scripts/sidecar.sh

desktop-test: desktop-sidecar ## The shell's format check, lints and tests, the real-server tests among them against the binary just built (no window is opened)
	cd desktop/src-tauri && $(CARGO) fmt --check && $(CARGO) clippy --locked --all-targets -- -D warnings
	CARGO="$(CARGO)" ./desktop/scripts/test.sh

desktop-dev: desktop-sidecar ## Run a development build of the app (data in the temporary folder, not the real data folder)
	cd desktop/src-tauri && $(CARGO) run --locked

desktop-shell-smoke: desktop-sidecar ## Drive a development build against the stand-in server and check what the shell does (opens a window)
	cd desktop/src-tauri && $(CARGO) build --locked
	./desktop/scripts/smoke.sh "$(DESKTOP_TARGET)/debug/scriobh-desktop"

desktop-build: desktop-deps desktop-sidecar ## Build the unsigned app and its installers for this machine (on a Mac this is desktop-build-macos for its own architecture)
	@if [ "$$(uname -s)" = Darwin ]; then ./desktop/scripts/build-macos.sh; else cd desktop && $(TAURI) build; fi

.PHONY: desktop-build-macos
desktop-build-macos: ## The macOS packages in ./dist (ARCH=arm64, x86_64, universal or all; default this Mac's own); signed ad hoc unless Apple certificate variables are set
	./desktop/scripts/build-macos.sh $(ARCH)

ci: ## Run the CI steps locally, in CI order
	$(MAKE) --no-print-directory workflows-check
	$(MAKE) --no-print-directory fmt-check
	$(MAKE) --no-print-directory vet
	$(MAKE) --no-print-directory editor-deps
	$(MAKE) --no-print-directory test
	$(MAKE) --no-print-directory contrast
	$(MAKE) --no-print-directory build
	$(MAKE) --no-print-directory skill-check
	$(MAKE) --no-print-directory bench-smoke
	$(MAKE) --no-print-directory editor-check
	$(MAKE) --no-print-directory editor-test
	$(GO) test ./internal/server -run Authz -count=1
	@test -d internal/editor || { echo "internal/editor does not exist yet; the editor round-trip test is not in this tree"; exit 1; }
	$(GO) test ./internal/editor -run RoundTrip -count=1
	$(MAKE) --no-print-directory desktop-smoke
	@if $(NODE) scripts/csp-smoke.mjs --probe >/dev/null 2>&1; then \
		$(MAKE) --no-print-directory csp-smoke; \
	else \
		echo "csp-smoke: SKIPPED (no Chromium found; set CSP_SMOKE_CHROMIUM or run 'cd scripts && npx playwright install chromium')"; \
	fi
	@if $(NODE) scripts/csp-smoke.mjs --probe >/dev/null 2>&1; then \
		$(MAKE) --no-print-directory e2e-fast; \
	else \
		echo "e2e-fast: SKIPPED (no Chromium found)"; \
	fi
