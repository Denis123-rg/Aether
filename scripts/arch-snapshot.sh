#!/usr/bin/env bash
set -euo pipefail

# Architecture Snapshot Script
# Stores a snapshot of the current architecture state for drift detection.

export PROJECT_ROOT="${PROJECT_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
export SNAPSHOT_DIR="${SNAPSHOT_DIR:-$PROJECT_ROOT/reports/arch-analysis/snapshots}"
export TIMESTAMP=$(date +%Y%m%d_%H%M%S)

mkdir -p "$SNAPSHOT_DIR"

SNAPSHOT_FILE="$SNAPSHOT_DIR/arch-snapshot-$TIMESTAMP.json"
LATEST_LINK="$PROJECT_ROOT/reports/arch-analysis/latest"

info() { echo "[$(date +%H:%M:%S)] INFO: $*"; }
error() { echo "[$(date +%H:%M:%S)] ERROR: $*"; }

generate_snapshot() {
    info "Generating architecture snapshot..."

    python3 -c "
import json, hashlib, os, sys

project_root = os.environ['PROJECT_ROOT']
snapshot_file = os.environ['SNAPSHOT_FILE']

report_path = os.path.join(project_root, 'reports', 'arch-analysis', 'data', 'arch-report.json')
if not os.path.exists(report_path):
    print('No architecture report found at', report_path)
    sys.exit(0)

with open(report_path) as f:
    data = json.load(f)

# Collect all Go module info
import subprocess
go_mods = []
try:
    result = subprocess.run(['go', 'list', '-m', '-json'], capture_output=True, text=True, cwd=project_root)
    for line in result.stdout.strip().split('\n'):
        if line:
            go_mods.append(json.loads(line))
except:
    pass

# Collect dependency info
deps = set()
import_graph = data.get('import_graph', {})
for edge in import_graph.get('edges', []):
    deps.add((edge['source'], edge['target']))

snapshot = {
    'timestamp': os.environ['TIMESTAMP'],
    'hash': hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest(),
    'stats': data.get('stats', {}),
    'packages': [{'path': p['path'], 'name': p['name'], 'file_count': len(p.get('files', []))} for p in data.get('packages', [])],
    'dependency_count': len(deps),
    'circular_cycles': import_graph.get('circular_imports', []),
}

json.dump(snapshot, open(snapshot_file, 'w'), indent=2)
print(f'Snapshot saved: {snapshot_file}')
print(f'Hash: {snapshot[\"hash\"]}')
print(f'Packages: {len(snapshot[\"packages\"])}')
"

    mkdir -p "$LATEST_LINK"
    cp "$SNAPSHOT_FILE" "$LATEST_LINK/arch-snapshot.json"
    info "Latest snapshot updated at $LATEST_LINK/arch-snapshot.json"
}

detect_drift() {
    local previous="$LATEST_LINK/arch-snapshot.json"
    if [ ! -f "$previous" ]; then
        info "No previous snapshot to compare against"
        return 0
    fi

    info "Comparing against previous snapshot..."

    python3 -c "
import json, sys

with open('$previous') as f:
    prev = json.load(f)
with open('$SNAPSHOT_FILE') as f:
    curr = json.load(f)

changes = []

prev_pkgs = {p['path']: p for p in prev.get('packages', [])}
curr_pkgs = {p['path']: p for p in curr.get('packages', [])}

for path in curr_pkgs:
    if path not in prev_pkgs:
        changes.append(f'  + NEW PACKAGE: {path}')
for path in prev_pkgs:
    if path not in curr_pkgs:
        changes.append(f'  - REMOVED PACKAGE: {path}')

for path in curr_pkgs:
    if path in prev_pkgs:
        if curr_pkgs[path]['file_count'] != prev_pkgs[path]['file_count']:
            changes.append(f'  ~ {path}: {prev_pkgs[path][\"file_count\"]} -> {curr_pkgs[path][\"file_count\"]} files')

prev_cycles = set(tuple(c) for c in prev.get('circular_cycles', []))
curr_cycles = set(tuple(c) for c in curr.get('circular_cycles', []))
if curr_cycles - prev_cycles:
    changes.append(f'  ! NEW CIRCULAR DEPENDENCIES: {len(curr_cycles - prev_cycles)} new cycle(s)')

prev_deps = prev.get('dependency_count', 0)
curr_deps = curr.get('dependency_count', 0)
if curr_deps != prev_deps:
    changes.append(f'  ! DEPENDENCY COUNT: {prev_deps} -> {curr_deps}')

if changes:
    print('Architecture drift detected:')
    for c in changes:
        print(c)
    print(f'\nTotal changes: {len(changes)}')
else:
    print('No architecture drift detected')

print(f'Previous hash: {prev.get(\"hash\", \"N/A\")}')
print(f'Current hash:  {curr.get(\"hash\", \"N/A\")}')
" 2>&1
}

main() {
    generate_snapshot
    detect_drift
}

main "$@"
