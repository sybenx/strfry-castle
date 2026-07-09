MODULE      := github.com/sybenx/castle-for-strfry-experiment
BIN_DIR     := bin
PLATFORMS   := linux/amd64 linux/arm64
BYTECHECK_MAX := 61440

.PHONY: build test smoke bytecheck clean

build:
	@set -e; \
	for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "==> building gatekeeper $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -o $(BIN_DIR)/$$os-$$arch/gatekeeper ./gatekeeper; \
		echo "==> building steward $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -o $(BIN_DIR)/$$os-$$arch/steward ./steward; \
	done

test:
	go vet ./...
	go test ./...

# Scratch strfry + fixture events via nak. Grows real assertions in Phase 1
# (gatekeeper accept/reject) and Phase 3a (cycle output against a compose
# stack); the harness itself lives in deploy/smoke.sh.
smoke: build
	./deploy/smoke.sh

# Strict from day one: missing towncrier/index.html is a FAILURE, >60KB is a
# FAILURE. Not wired into CI until Phase 6a (see DECISIONS.md) but the
# behavior never changes, so it can't rot into a no-op.
bytecheck:
	@if [ ! -f towncrier/index.html ]; then \
		echo "bytecheck: towncrier/index.html is missing"; exit 1; \
	fi; \
	size=$$(wc -c < towncrier/index.html | tr -d ' '); \
	if [ $$size -gt $(BYTECHECK_MAX) ]; then \
		echo "bytecheck: towncrier/index.html is $$size bytes, over the $(BYTECHECK_MAX)-byte (60KB) budget"; \
		exit 1; \
	fi; \
	echo "bytecheck: towncrier/index.html is $$size bytes, within budget"

clean:
	rm -rf $(BIN_DIR)
