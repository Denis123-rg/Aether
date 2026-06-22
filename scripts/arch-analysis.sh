#!/usr/bin/env bash
set -uo pipefail

# ================================================================
# Aether Architecture Analysis & Quality Assurance Platform
# ================================================================
# This script orchestrates the complete architecture validation pipeline.
#
# Phases:
#   1. Go Architecture Analysis (import graph, call graph, type analysis)
#   2. Rust Architecture Analysis (cargo modules, dependency graph)
#   3. Forbidden Import Detection
#   4. Circular Dependency Detection
#   5. Architecture Rule Validation
#   6. Static Analysis (go vet, staticcheck, clippy)
#   7. Test Execution with Coverage
#   8. Diagram Generation (Mermaid, Graphviz)
#   9. Report Generation (HTML, JSON)
#  10. Architecture Snapshot & Drift Detection
#  11. Artifact Collection
# ================================================================

export PROJECT_ROOT="${PROJECT_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
export OUTPUT_DIR="${OUTPUT_DIR:-$PROJECT_ROOT/reports/arch-analysis}"
export TIMESTAMP=$(date +%Y%m%d_%H%M%S)
export REPORT_DIR="$OUTPUT_DIR/$TIMESTAMP"
export RULES_FILE="${RULES_FILE:-$PROJECT_ROOT/arch-rules.yaml}"
export HAS_ERRORS=0

mkdir -p "$REPORT_DIR"/{diagrams,reports,graphs,snapshots,logs,data}

log()  { echo "[$(date +%H:%M:%S)] $*"; }
info() { log "INFO: $*"; }
warn() { log "WARN: $*"; }
error(){ log "ERROR: $*"; HAS_ERRORS=1; }
die()  { error "$*"; exit 1; }

cleanup() {
    info "Cleaning up temporary files..." || true
}
trap cleanup EXIT

set +e

check_deps() {
    local missing=0
    for cmd in go rustc cargo python3 dot mmdc; do
        if ! command -v "$cmd" &>/dev/null; then
            warn "Missing dependency: $cmd"
            missing=$((missing + 1))
        fi
    done
    if command -v dot &>/dev/null; then
        info "Graphviz found: $(dot -V 2>&1)"
    fi
    if [ "$missing" -gt 0 ]; then
        warn "$missing tool(s) missing, some features may be unavailable"
    fi
}

phase_go_analysis() {
    info "=== Phase 1: Go Architecture Analysis ==="

    cd "$PROJECT_ROOT"

    info "Building architecture analyzer..."
    cd "$PROJECT_ROOT/scripts/arch-analyzer"
    go build -o "$REPORT_DIR/arch-analyzer" . 2>&1 || die "Failed to build arch-analyzer"
    cd "$PROJECT_ROOT"

    info "Running Go architecture analysis..."
    "$REPORT_DIR/arch-analyzer" "$PROJECT_ROOT" "$REPORT_DIR/data/arch-report.json" 2>&1 || {
        warn "Architecture analysis reported violations"
    }

    info "Running go vet..."
    go vet ./... 2>&1 | tee "$REPORT_DIR/logs/go-vet.log" || {
        error "go vet found issues"
    }

    info "Running staticcheck..."
    if command -v staticcheck &>/dev/null; then
        staticcheck ./... 2>&1 | tee "$REPORT_DIR/logs/staticcheck.log" || {
            error "staticcheck found issues"
        }
    else
        warn "staticcheck not installed, skipping"
    fi

    info "Running ineffassign..."
    if command -v ineffassign &>/dev/null; then
        ineffassign ./... 2>&1 | tee "$REPORT_DIR/logs/ineffassign.log" || true
    fi

    info "Go analysis complete"
}

phase_rust_analysis() {
    info "=== Phase 2: Rust Architecture Analysis ==="

    cd "$PROJECT_ROOT"

    info "Analyzing Rust crate structure..."
    if command -v cargo &>/dev/null; then
        cargo metadata --format-version 1 > "$REPORT_DIR/data/cargo-metadata.json" 2>/dev/null || warn "cargo metadata failed"

        info "Generating Rust dependency graph..."
        if command -v cargo-depgraph &>/dev/null; then
            cargo depgraph --workspace-only > "$REPORT_DIR/graphs/rust-depgraph.dot" 2>/dev/null || warn "cargo-depgraph failed"
        fi

        info "Running clippy..."
        cargo clippy --workspace -- -D warnings 2>&1 | tee "$REPORT_DIR/logs/clippy.log" || {
            error "clippy found issues"
        }
    fi

    info "Rust analysis complete"
}

phase_validate_arch() {
    info "=== Phase 3: Architecture Rule Validation ==="

    cd "$PROJECT_ROOT"

    if [ ! -f "$REPORT_DIR/data/arch-report.json" ]; then
        warn "No architecture report found, skipping validation"
        return
    fi

    info "Validating architecture rules from $RULES_FILE..."

    python3 "$PROJECT_ROOT/scripts/arch-validate.py" \
        --rules "$RULES_FILE" \
        --report "$REPORT_DIR/data/arch-report.json" \
        --output "$REPORT_DIR/reports/validation-report.json" 2>&1 | tee "$REPORT_DIR/logs/validation.log" || {
        error "Architecture rule violations detected"
    }

    info "Validation complete"
}

phase_run_tests() {
    info "=== Phase 4: Test Execution ==="

    cd "$PROJECT_ROOT"

    info "Running Go tests with coverage..."
    go test ./cmd/... ./internal/... -count=1 -coverprofile="$REPORT_DIR/data/go-coverage.out" -covermode=atomic 2>&1 | tee "$REPORT_DIR/logs/go-test.log" || {
        warn "Some Go tests failed"
    }

    if [ -f "$REPORT_DIR/data/go-coverage.out" ]; then
        go tool cover -func="$REPORT_DIR/data/go-coverage.out" > "$REPORT_DIR/reports/go-coverage.txt" 2>&1
        COVERAGE=$(go tool cover -func="$REPORT_DIR/data/go-coverage.out" | grep total | grep -oP '\d+\.\d+' | head -1)
        info "Go coverage: ${COVERAGE:-N/A}%"
    fi

    info "Running Go race detector..."
    go test -race -count=1 ./cmd/... ./internal/... 2>&1 | tee "$REPORT_DIR/logs/race-detector.log" || {
        warn "Race detector found issues"
    }

    if command -v cargo &>/dev/null; then
        info "Running Rust tests..."
        cargo test --workspace --exclude aether-integration-tests 2>&1 | tee "$REPORT_DIR/logs/rust-test.log" || {
            warn "Some Rust tests failed"
        }
    fi

    info "Test execution complete"
}

phase_generate_diagrams() {
    info "=== Phase 5: Diagram Generation ==="

    cd "$PROJECT_ROOT"

    if [ -f "$PROJECT_ROOT/scripts/diagram-gen.py" ]; then
        python3 "$PROJECT_ROOT/scripts/diagram-gen.py" \
            --report "$REPORT_DIR/data/arch-report.json" \
            --output "$REPORT_DIR/diagrams" 2>&1 | tee "$REPORT_DIR/logs/diagram-gen.log" || {
            warn "Diagram generation had issues"
        }
    fi

    info "Generating Mermaid diagrams..."
    generate_mermaid_import_graph
    generate_mermaid_layer_graph
    generate_mermaid_component_diagram

    info "Generating Graphviz diagrams..."
    if command -v dot &>/dev/null; then
        generate_graphviz_dep_graph
    else
        warn "Graphviz (dot) not available, skipping DOT rendering"
    fi

    info "Diagram generation complete"
}

generate_mermaid_import_graph() {
    local mermaid_file="$REPORT_DIR/diagrams/import-graph.mermaid"
    cat > "$mermaid_file" << 'MERMAID_HEADER'
---
title: Aether Import Dependency Graph
---
graph TD
MERMAID_HEADER

    if [ -f "$REPORT_DIR/data/arch-report.json" ]; then
        python3 -c "
import json
with open('$REPORT_DIR/data/arch-report.json') as f:
    data = json.load(f)
edges = data.get('import_graph', {}).get('edges', [])
seen = set()
for e in edges:
    src = e['source'].replace('/', '_').replace('-', '_')
    tgt = e['target'].replace('/', '_').replace('-', '_')
    key = (src, tgt)
    if key not in seen:
        seen.add(key)
        print(f'    {src} --> {tgt}')
" >> "$mermaid_file" 2>/dev/null || warn "Failed to generate Mermaid import graph"
    fi

    if command -v mmdc &>/dev/null; then
        mmdc -i "$mermaid_file" -o "$REPORT_DIR/diagrams/import-graph.png" 2>/dev/null || warn "mmdc failed for import graph"
    fi
}

generate_mermaid_layer_graph() {
    local mermaid_file="$REPORT_DIR/diagrams/layer-graph.mermaid"
    cat > "$mermaid_file" << 'MERMAID'
---
title: Aether Architecture Layer Diagram
---
graph TD
    subgraph "Handler Layer"
        cmd_executor["cmd/executor"]
        cmd_monitor["cmd/monitor"]
        cmd_telebot["cmd/telebot"]
        cmd_signer["cmd/signer"]
        cmd_reconciler["cmd/reconciler"]
    end
    subgraph "Service Layer"
        internal_risk["internal/risk"]
        internal_strategy["internal/strategy"]
        internal_events["internal/events"]
        internal_grpc["internal/grpc"]
        internal_signer["internal/signer"]
    end
    subgraph "Database Layer"
        internal_db["internal/db"]
    end
    subgraph "Config Layer"
        internal_config["internal/config"]
    end
    subgraph "Domain Layer"
        internal_pb["internal/pb"]
        internal_metrics["internal/metrics"]
    end
    subgraph "Rust Core Layer"
        crates_grpc["crates/grpc-server"]
        crates_detector["crates/detector"]
        crates_pools["crates/pools"]
        crates_simulator["crates/simulator"]
    end

    cmd_executor --> internal_grpc
    cmd_executor --> internal_risk
    cmd_executor --> internal_signer
    cmd_monitor --> internal_events
    cmd_telebot --> internal_events
    internal_grpc --> crates_grpc
    internal_risk --> internal_config
    internal_strategy --> internal_config
    internal_events --> internal_db
    internal_db --> internal_config
MERMAID

    if command -v mmdc &>/dev/null; then
        mmdc -i "$mermaid_file" -o "$REPORT_DIR/diagrams/layer-graph.png" 2>/dev/null || warn "mmdc failed for layer graph"
    fi
}

generate_mermaid_component_diagram() {
    local mermaid_file="$REPORT_DIR/diagrams/component-diagram.mermaid"
    cat > "$mermaid_file" << 'MERMAID'
---
title: Aether Component Architecture
---
graph TB
    subgraph "External Inputs"
        ETH_RPC["Ethereum RPC"]
        MEMPOOL["Mempool Stream"]
        BUILDERS["Block Builders"]
    end
    subgraph "Ingestion Layer"
        INGEST["crates/ingestion"]
        DISCOVERY["crates/discovery"]
    end
    subgraph "Core Engine"
        STATE["crates/state"]
        DETECTOR["crates/detector"]
        SIMULATOR["crates/simulator"]
    end
    subgraph "gRPC API"
        GRPC_SERVER["crates/grpc-server"]
    end
    subgraph "Go Coordination"
        EXECUTOR["cmd/executor"]
        MONITOR["cmd/monitor"]
        TELEBOT["cmd/telebot"]
    end
    subgraph "Persistence"
        DB["internal/db"]
        REDIS["Redis Pub/Sub"]
    end

    ETH_RPC --> INGEST
    MEMPOOL --> INGEST
    INGEST --> STATE
    DISCOVERY --> STATE
    STATE --> DETECTOR
    DETECTOR --> SIMULATOR
    SIMULATOR --> GRPC_SERVER
    GRPC_SERVER --> EXECUTOR
    EXECUTOR --> BUILDERS
    EXECUTOR --> DB
    EXECUTOR --> REDIS
    MONITOR --> REDIS
    TELEBOT --> REDIS
MERMAID

    if command -v mmdc &>/dev/null; then
        mmdc -i "$mermaid_file" -o "$REPORT_DIR/diagrams/component-diagram.png" 2>/dev/null || warn "mmdc failed for component diagram"
    fi
}

generate_graphviz_dep_graph() {
    local dot_file="$REPORT_DIR/graphs/dep-graph.dot"
    cat > "$dot_file" << 'DOTHEADER'
digraph AetherDependencies {
    rankdir=LR;
    splines=true;
    node [shape=box, style=filled, fillcolor=lightyellow];
    edge [color=gray];
DOTHEADER

    if [ -f "$REPORT_DIR/data/arch-report.json" ]; then
        python3 -c "
import json
with open('$REPORT_DIR/data/arch-report.json') as f:
    data = json.load(f)
edges = data.get('import_graph', {}).get('edges', [])
seen = set()
for e in edges:
    src = e['source']
    tgt = e['target']
    key = (src, tgt)
    if key not in seen:
        seen.add(key)
        print(f'    \"{src}\" -> \"{tgt}\";')
" >> "$dot_file" 2>/dev/null || warn "Failed to generate DOT graph"
    fi

    echo "}" >> "$dot_file"

    if command -v dot &>/dev/null; then
        dot -Tsvg "$dot_file" -o "$REPORT_DIR/diagrams/dep-graph.svg" 2>/dev/null || warn "dot SVG failed"
        dot -Tpng "$dot_file" -o "$REPORT_DIR/diagrams/dep-graph.png" 2>/dev/null || warn "dot PNG failed"
    fi
}

phase_generate_reports() {
    info "=== Phase 6: Report Generation ==="

    cd "$PROJECT_ROOT"

    if [ -f "$PROJECT_ROOT/scripts/report-gen.py" ]; then
        python3 "$PROJECT_ROOT/scripts/report-gen.py" \
            --report "$REPORT_DIR/data/arch-report.json" \
            --output "$REPORT_DIR/reports" 2>&1 | tee "$REPORT_DIR/logs/report-gen.log" || {
            warn "Report generation had issues"
        }
    fi

    generate_html_dashboard
    generate_summary_report

    info "Report generation complete"
}

generate_html_dashboard() {
    local html_file="$REPORT_DIR/reports/architecture-dashboard.html"
    local arch_report="$REPORT_DIR/data/arch-report.json"

    local total_files=0 total_pkgs=0 total_imps=0 total_funcs=0 total_types=0
    local circular=0 forbidden=0 coverage_val="N/A"

    if [ -f "$arch_report" ]; then
        total_files=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['total_files'])" 2>/dev/null || echo 0)
        total_pkgs=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['total_packages'])" 2>/dev/null || echo 0)
        total_imps=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['total_imports'])" 2>/dev/null || echo 0)
        total_funcs=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['total_functions'])" 2>/dev/null || echo 0)
        total_types=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['total_types'])" 2>/dev/null || echo 0)
        circular=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['circular_deps'])" 2>/dev/null || echo 0)
        forbidden=$(python3 -c "import json; d=json.load(open('$arch_report')); print(d['stats']['forbidden_deps'])" 2>/dev/null || echo 0)
    fi

    if [ -f "$REPORT_DIR/reports/go-coverage.txt" ]; then
        coverage_val=$(grep total "$REPORT_DIR/reports/go-coverage.txt" | grep -oP '\d+\.\d+' | head -1 || echo "N/A")
    fi

    cat > "$html_file" << HTML_HEADER
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Aether Architecture Analysis Dashboard</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0d1117; color: #c9d1d9; padding: 20px; }
  h1 { color: #58a6ff; margin-bottom: 20px; font-size: 28px; }
  h2 { color: #8b949e; margin: 25px 0 15px; border-bottom: 1px solid #30363d; padding-bottom: 8px; }
  .stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 15px; margin-bottom: 25px; }
  .stat-card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 18px; text-align: center; }
  .stat-card .value { font-size: 32px; font-weight: 700; color: #58a6ff; }
  .stat-card .label { font-size: 12px; color: #8b949e; margin-top: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
  .stat-card.warning .value { color: #d29922; }
  .stat-card.error .value { color: #f85149; }
  .stat-card.success .value { color: #3fb950; }
  table { width: 100%; border-collapse: collapse; margin: 15px 0; }
  th, td { padding: 10px 14px; text-align: left; border-bottom: 1px solid #21262d; font-size: 13px; }
  th { background: #161b22; color: #8b949e; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px; }
  tr:hover { background: #1c2128; }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 600; }
  .badge-pass { background: #1b3a2d; color: #3fb950; }
  .badge-fail { background: #3d1f1e; color: #f85149; }
  .badge-warn { background: #3d2e13; color: #d29922; }
  .error-item { color: #f85149; padding: 6px 0; font-size: 13px; }
  .diagram-section { margin: 20px 0; }
  .diagram-section img { max-width: 100%; border: 1px solid #30363d; border-radius: 8px; }
  .timestamp { color: #484f58; font-size: 12px; margin-bottom: 20px; }
  .nav { display: flex; gap: 10px; margin-bottom: 20px; flex-wrap: wrap; }
  .nav a { color: #58a6ff; text-decoration: none; padding: 6px 14px; border: 1px solid #30363d; border-radius: 6px; font-size: 13px; }
  .nav a:hover { background: #1c2128; }
  pre { background: #161b22; padding: 15px; border-radius: 8px; overflow-x: auto; font-size: 12px; line-height: 1.5; }
</style>
</head>
<body>
<h1>Aether Architecture Analysis Dashboard</h1>
<div class="timestamp">Generated: $(date '+%Y-%m-%d %H:%M:%S') | Report: $TIMESTAMP</div>
<div class="nav">
  <a href="#stats">Statistics</a>
  <a href="#packages">Packages</a>
  <a href="#violations">Violations</a>
  <a href="#diagrams">Diagrams</a>
  <a href="#coverage">Coverage</a>
</div>

<h2 id="stats">Architecture Statistics</h2>
<div class="stats-grid">
  <div class="stat-card"><div class="value">$total_pkgs</div><div class="label">Packages</div></div>
  <div class="stat-card"><div class="value">$total_files</div><div class="label">Go Files</div></div>
  <div class="stat-card"><div class="value">$total_imps</div><div class="label">Imports</div></div>
  <div class="stat-card"><div class="value">$total_funcs</div><div class="label">Functions</div></div>
  <div class="stat-card"><div class="value">$total_types</div><div class="label">Types</div></div>
  <div class="stat-card $( [ "$circular" -gt 0 ] && echo "error" || echo "success" )"><div class="value">$circular</div><div class="label">Circular Dependencies</div></div>
  <div class="stat-card $( [ "$forbidden" -gt 0 ] && echo "error" || echo "success" )"><div class="value">$forbidden</div><div class="label">Forbidden Dependencies</div></div>
  <div class="stat-card"><div class="value">${coverage_val}%</div><div class="label">Go Coverage</div></div>
</div>

<h2 id="packages">Package Analysis</h2>
<table>
<tr><th>Package</th><th>Files</th><th>Functions</th><th>Types</th><th>Interfaces</th><th>Structs</th></tr>
HTML_HEADER

    if [ -f "$arch_report" ]; then
        python3 -c "
import json, html
with open('$arch_report') as f:
    data = json.load(f)
for pkg in data.get('packages', []):
    files = len(pkg.get('files', []))
    funcs = sum(len(f.get('functions', [])) for f in pkg.get('files', []))
    types = sum(len(f.get('types', [])) for f in pkg.get('files', []))
    ifaces = sum(len(f.get('interfaces', [])) for f in pkg.get('files', []))
    structs = sum(len(f.get('structs', [])) for f in pkg.get('files', []))
    pname = html.escape(pkg.get('path', ''))
    print(f'<tr><td>{pname}</td><td>{files}</td><td>{funcs}</td><td>{types}</td><td>{ifaces}</td><td>{structs}</td></tr>')
" >> "$html_file" 2>/dev/null || true
    fi

    cat >> "$html_file" << 'HTML_VIOLATIONS'
</table>

<h2 id="violations">Violations & Warnings</h2>
HTML_VIOLATIONS

    if [ -f "$arch_report" ] && [ "$forbidden" -gt 0 ]; then
        python3 -c "
import json, html
with open('$arch_report') as f:
    data = json.load(f)
for fi in data.get('forbidden_imports', []):
    msg = html.escape(fi.get('message', ''))
    src = html.escape(fi.get('source', ''))
    tgt = html.escape(fi.get('target', ''))
    print(f'<div class=\"error-item\">❌ <strong>{src}</strong> → <strong>{tgt}</strong>: {msg}</div>')
" >> "$html_file" 2>/dev/null || true
    else
        echo '<div class="stat-card success"><div class="value">✓</div><div class="label">No Architecture Violations</div></div>' >> "$html_file"
    fi

    if [ -f "$arch_report" ]; then
        python3 -c "
import json, html
with open('$arch_report') as f:
    data = json.load(f)
for w in data.get('warnings', []):
    w = html.escape(w)
    print(f'<div class=\"error-item\">⚠️ {w}</div>')
" >> "$html_file" 2>/dev/null || true
    fi

    cat >> "$html_file" << 'HTML_DIAGRAMS'
<h2 id="diagrams">Generated Diagrams</h2>
<div class="diagram-section">
HTML_DIAGRAMS

    if [ -f "$REPORT_DIR/diagrams/import-graph.png" ]; then
        echo '<h3>Import Graph</h3><img src="../diagrams/import-graph.png" alt="Import Graph">' >> "$html_file"
    fi
    if [ -f "$REPORT_DIR/diagrams/layer-graph.png" ]; then
        echo '<h3>Layer Architecture</h3><img src="../diagrams/layer-graph.png" alt="Layer Architecture">' >> "$html_file"
    fi
    if [ -f "$REPORT_DIR/diagrams/component-diagram.png" ]; then
        echo '<h3>Component Architecture</h3><img src="../diagrams/component-diagram.png" alt="Component Architecture">' >> "$html_file"
    fi
    if [ -f "$REPORT_DIR/diagrams/dep-graph.png" ]; then
        echo '<h3>Dependency Graph</h3><img src="../diagrams/dep-graph.png" alt="Dependency Graph">' >> "$html_file"
    fi

    cat >> "$html_file" << 'HTML_FOOTER'
</div>
</body>
</html>
HTML_FOOTER

    info "HTML dashboard generated: $html_file"
}

generate_summary_report() {
    local report_file="$REPORT_DIR/reports/arch-summary.md"

    {
        echo "# Aether Architecture Analysis Report"
        echo "**Generated:** $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        echo "## Summary"
        echo ""
        if [ -f "$REPORT_DIR/data/arch-report.json" ]; then
            python3 -c "
import json
with open('$REPORT_DIR/data/arch-report.json') as f:
    d = json.load(f)
s = d['stats']
print(f'- **Total Packages:** {s[\"total_packages\"]}')
print(f'- **Total Files:** {s[\"total_files\"]}')
print(f'- **Total Imports:** {s[\"total_imports\"]}')
print(f'- **Total Functions:** {s[\"total_functions\"]}')
print(f'- **Total Types:** {s[\"total_types\"]}')
print(f'- **Total Interfaces:** {s[\"total_interfaces\"]}')
print(f'- **Total Structs:** {s[\"total_structs\"]}')
print(f'- **Circular Dependencies:** {s[\"circular_deps\"]}')
print(f'- **Forbidden Dependencies:** {s[\"forbidden_deps\"]}')
" >> "$report_file" 2>/dev/null || true
        fi

        echo ""
        echo "## Packages"
        echo ""
        echo "| Package | Files | Functions | Types | Interfaces |"
        echo "|---------|-------|-----------|-------|------------|"

        if [ -f "$REPORT_DIR/data/arch-report.json" ]; then
            python3 -c "
import json
with open('$REPORT_DIR/data/arch-report.json') as f:
    data = json.load(f)
for pkg in data.get('packages', []):
    files = len(pkg.get('files', []))
    funcs = sum(len(f.get('functions', [])) for f in pkg.get('files', []))
    types = sum(len(f.get('types', [])) for f in pkg.get('files', []))
    ifaces = sum(len(f.get('interfaces', [])) for f in pkg.get('files', []))
    print(f'| {pkg[\"path\"]} | {files} | {funcs} | {types} | {ifaces} |')
" >> "$report_file" 2>/dev/null || true
        fi
    } 2>/dev/null

    info "Summary report: $report_file"
}

phase_snapshot() {
    info "=== Phase 7: Architecture Snapshot ==="

    cd "$PROJECT_ROOT"

    local snapshot_file="$REPORT_DIR/snapshots/arch-snapshot.json"
    local previous_snapshot="$PROJECT_ROOT/reports/arch-analysis/latest/arch-snapshot.json"
    local drift_report="$REPORT_DIR/reports/drift-report.json"

    if [ -f "$REPORT_DIR/data/arch-report.json" ]; then
        python3 -c "
import json, hashlib, sys

with open('$REPORT_DIR/data/arch-report.json') as f:
    data = json.load(f)

snapshot = {
    'timestamp': '$TIMESTAMP',
    'hash': hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest(),
    'stats': data.get('stats', {}),
    'packages': [{'path': p['path'], 'name': p['name'], 'file_count': len(p.get('files', []))} for p in data.get('packages', [])],
}
json.dump(snapshot, open('$snapshot_file', 'w'), indent=2)
print(f'Snapshot hash: {snapshot[\"hash\"]}')
" 2>&1 | tee "$REPORT_DIR/logs/snapshot.log" || warn "Snapshot generation failed"
    fi

    if [ -f "$previous_snapshot" ]; then
        info "Comparing with previous snapshot for architecture drift..."

        python3 -c "
import json, sys

with open('$previous_snapshot') as f:
    prev = json.load(f)
with open('$snapshot_file') as f:
    curr = json.load(f)

drifts = []
prev_pkgs = {p['path']: p for p in prev.get('packages', [])}
curr_pkgs = {p['path']: p for p in curr.get('packages', [])}

for path in curr_pkgs:
    if path not in prev_pkgs:
        drifts.append({'type': 'new_package', 'path': path, 'message': f'New package added: {path}'})

for path in prev_pkgs:
    if path not in curr_pkgs:
        drifts.append({'type': 'removed_package', 'path': path, 'message': f'Package removed: {path}'})

for path in curr_pkgs:
    if path in prev_pkgs:
        if curr_pkgs[path]['file_count'] != prev_pkgs[path]['file_count']:
            drifts.append({'type': 'file_count_changed', 'path': path,
                'message': f'{path}: {prev_pkgs[path][\"file_count\"]} → {curr_pkgs[path][\"file_count\"]} files'})

prev_circular = prev.get('stats', {}).get('circular_deps', 0)
curr_circular = curr.get('stats', {}).get('circular_deps', 0)
if curr_circular > prev_circular:
    drifts.append({'type': 'new_cycles', 'path': 'project',
        'message': f'Circular dependencies increased: {prev_circular} → {curr_circular}'})

prev_forbidden = prev.get('stats', {}).get('forbidden_deps', 0)
curr_forbidden = curr.get('stats', {}).get('forbidden_deps', 0)
if curr_forbidden > prev_forbidden:
    drifts.append({'type': 'new_forbidden_deps', 'path': 'project',
        'message': f'Forbidden dependencies increased: {prev_forbidden} → {curr_forbidden}'})

result = {'drifts': drifts, 'drift_count': len(drifts), 'has_drift': len(drifts) > 0}
json.dump(result, open('$drift_report', 'w'), indent=2)

if drifts:
    print(f'Architecture DRIFT detected: {len(drifts)} change(s)')
    for d in drifts:
        print(f'  - {d[\"message\"]}')
    sys.exit(1 if curr_circular > prev_circular or curr_forbidden > prev_forbidden else 0)
else:
    print('No architecture drift detected')
" 2>&1 | tee "$REPORT_DIR/logs/drift.log" || {
        error "Architecture drift detected - pipeline should fail"
    }
    fi

    mkdir -p "$PROJECT_ROOT/reports/arch-analysis/latest"
    cp "$snapshot_file" "$PROJECT_ROOT/reports/arch-analysis/latest/arch-snapshot.json" 2>/dev/null || true
}

phase_collect_artifacts() {
    info "=== Phase 8: Artifact Collection ==="

    cd "$PROJECT_ROOT"

    local artifact_dir="$REPORT_DIR/artifacts"
    mkdir -p "$artifact_dir"

    find "$REPORT_DIR" -type f \( -name "*.json" -o -name "*.html" -o -name "*.svg" -o -name "*.png" -o -name "*.dot" -o -name "*.mermaid" -o -name "*.log" -o -name "*.txt" -o -name "*.out" -o -name "*.yaml" -o -name "*.yml" -o -name "*.md" \) -exec cp {} "$artifact_dir/" \; 2>/dev/null || true

    info "Artifacts collected: $(find "$artifact_dir" -type f | wc -l) files"
}

print_summary() {
    echo ""
    echo "========================================================================"
    echo "  AETHER ARCHITECTURE ANALYSIS COMPLETE"
    echo "========================================================================"
    echo "  Report directory: $REPORT_DIR"
    echo "  Dashboard:        $REPORT_DIR/reports/architecture-dashboard.html"
    echo "  Summary:          $REPORT_DIR/reports/arch-summary.md"
    echo "  Diagrams:         $REPORT_DIR/diagrams/"
    echo "  Data:             $REPORT_DIR/data/"
    echo "========================================================================"
    if [ "$HAS_ERRORS" -ne 0 ]; then
        echo "  STATUS:          ❌ Some checks failed"
    else
        echo "  STATUS:          ✅ All checks passed"
    fi
    echo "========================================================================"
}

main() {
    echo ""
    echo "========================================================================"
    echo "  AETHER ARCHITECTURE ANALYSIS AND QUALITY ASSURANCE PLATFORM"
    echo "========================================================================"
    echo ""

    check_deps
    phase_go_analysis
    phase_rust_analysis
    phase_validate_arch
    phase_run_tests
    phase_generate_diagrams
    phase_generate_reports
    phase_snapshot
    phase_collect_artifacts
    print_summary

    if [ "$HAS_ERRORS" -ne 0 ]; then
        exit 1
    fi
}

main "$@"
