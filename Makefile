# Aether off-chain test suite (Go executor + Rust core).
# On-chain (Foundry) tests are intentionally excluded.

SHELL := /bin/bash
PROJECT_ROOT := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
COVERAGE_DIR := $(PROJECT_ROOT)coverage
GO_COVER := $(COVERAGE_DIR)/go.out
RUST_COVER_DIR := $(COVERAGE_DIR)/rust

.PHONY: test test-offchain test-offchain-go test-offchain-rust test-offchain-integration \
	test-offchain-fuzz test-offchain-replay test-offchain-e2e test-offchain-coverage \
	test-offchain-report clean-coverage build e2e issue1-check

build:
	cd "$(PROJECT_ROOT)" && cargo build --release -p aether-grpc-server
	cd "$(PROJECT_ROOT)" && go build -o bin/aether-executor ./cmd/executor
	cd "$(PROJECT_ROOT)" && go build -o bin/aether-monitor ./cmd/monitor
	cd "$(PROJECT_ROOT)" && go build -o bin/aether-telebot ./cmd/telebot
	cd "$(PROJECT_ROOT)" && go build -o bin/aether-signer ./cmd/signer

e2e: test-offchain-e2e

test: test-offchain

issue1-check:
	bash "$(PROJECT_ROOT)scripts/test_issue1_references.sh"

test-offchain: test-offchain-go test-offchain-rust test-offchain-integration
	@echo "=== Off-chain suite complete (unit + integration) ==="

test-offchain-go:
	@mkdir -p "$(COVERAGE_DIR)"
	cd "$(PROJECT_ROOT)" && go test ./... -count=1 -timeout 300s -coverprofile="$(GO_COVER)"
	@echo "Go coverage: $$(cd "$(PROJECT_ROOT)" && go tool cover -func="$(GO_COVER)" | awk '/total:/ {print $$3}')"

test-offchain-rust:
	cd "$(PROJECT_ROOT)" && cargo test --workspace --exclude aether-integration-tests -- --test-threads=4
	cd "$(PROJECT_ROOT)" && cargo test -p aether-integration-tests -- --test-threads=2

test-offchain-integration:
	cd "$(PROJECT_ROOT)" && go test ./tests/integration/... -count=1 -timeout 120s
	cd "$(PROJECT_ROOT)" && bash "$(PROJECT_ROOT)scripts/test_integration.sh"

test-offchain-fuzz:
	@command -v cargo-fuzz >/dev/null 2>&1 || { echo "cargo-fuzz not installed; run: cargo install cargo-fuzz"; exit 1; }
	cd "$(PROJECT_ROOT)fuzz" && cargo fuzz run bellman_ford -- -max_total_time=30 -runs=1000000
	cd "$(PROJECT_ROOT)fuzz" && cargo fuzz run pool_adapter -- -max_total_time=30 -runs=1000000
	cd "$(PROJECT_ROOT)fuzz" && cargo fuzz run cp_math -- -max_total_time=30 -runs=1000000
	cd "$(PROJECT_ROOT)fuzz" && cargo fuzz run swap_calldata -- -max_total_time=30 -runs=1000000
	cd "$(PROJECT_ROOT)fuzz" && cargo fuzz run discovery_validator -- -max_total_time=30 -runs=1000000

test-offchain-replay:
	cd "$(PROJECT_ROOT)" && bash "$(PROJECT_ROOT)scripts/test_replay.sh"

test-offchain-e2e:
	cd "$(PROJECT_ROOT)" && bash "$(PROJECT_ROOT)tests/e2e/run_full_pipeline.sh"

test-offchain-coverage: test-offchain-go test-offchain-rust-coverage
	@echo "Coverage reports in $(COVERAGE_DIR)/"

test-offchain-rust-coverage:
	@command -v cargo-tarpaulin >/dev/null 2>&1 || { echo "cargo-tarpaulin not installed; run: cargo install cargo-tarpaulin"; exit 1; }
	@mkdir -p "$(RUST_COVER_DIR)"
	cd "$(PROJECT_ROOT)" && cargo tarpaulin --workspace --exclude-files 'crates/integration-tests/*' \
		--ignore-tests --out Html --output-dir "$(RUST_COVER_DIR)"

test-offchain-report:
	@bash "$(PROJECT_ROOT)scripts/test_offchain_report.sh"

clean-coverage:
	rm -rf "$(COVERAGE_DIR)"
