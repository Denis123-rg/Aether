#!/usr/bin/env python3
"""Compare architecture snapshots for drift detection."""

import json
import os
import sys
from pathlib import Path


def generate_snapshot(report_path, snapshot_path, timestamp):
    with open(report_path) as f:
        data = json.load(f)

    import hashlib
    snapshot = {
        "timestamp": timestamp,
        "hash": hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest(),
        "stats": data.get("stats", {}),
        "packages": [
            {"path": p["path"], "name": p["name"], "file_count": len(p.get("files", []))}
            for p in data.get("packages", [])
        ],
        "circular_cycles": data.get("import_graph", {}).get("circular_imports", []),
    }

    os.makedirs(os.path.dirname(snapshot_path), exist_ok=True)
    with open(snapshot_path, "w") as f:
        json.dump(snapshot, f, indent=2)
    print(f"Snapshot saved: {snapshot_path}")
    print(f"Hash: {snapshot['hash']}")
    print(f"Packages: {len(snapshot['packages'])}")
    return snapshot


def compare_snapshots(previous_path, current_path):
    with open(previous_path) as f:
        prev = json.load(f)
    with open(current_path) as f:
        curr = json.load(f)

    changes = []

    prev_pkgs = {p["path"]: p for p in (prev.get("packages") or [])}
    curr_pkgs = {p["path"]: p for p in (curr.get("packages") or [])}

    for path in sorted(curr_pkgs):
        if path not in prev_pkgs:
            changes.append(f"+ NEW: {path}")

    for path in sorted(prev_pkgs):
        if path not in curr_pkgs:
            changes.append(f"- GONE: {path}")

    for path in sorted(curr_pkgs):
        if path in prev_pkgs:
            pc = prev_pkgs[path]["file_count"]
            cc = curr_pkgs[path]["file_count"]
            if pc != cc:
                changes.append(f"~ CHANGED: {path} ({pc} -> {cc} files)")

    prev_c = set(tuple(c) for c in (prev.get("circular_cycles") or []))
    curr_c = set(tuple(c) for c in (curr.get("circular_cycles") or []))
    if curr_c - prev_c:
        changes.append(f"! NEW CYCLES: {len(curr_c - prev_c)} new cycle(s)")

    prev_deps = prev.get("stats", {}).get("forbidden_deps", 0)
    curr_deps = curr.get("stats", {}).get("forbidden_deps", 0)
    if curr_deps > prev_deps:
        changes.append(f"! FORBIDDEN DEPS: {prev_deps} -> {curr_deps}")

    if changes:
        print("Architecture changes detected:")
        for c in changes:
            print(f"  {c}")
        print(f"\nTotal changes: {len(changes)}")
    else:
        print("No architecture changes detected")

    print(f"Previous hash: {prev.get('hash', 'N/A')}")
    print(f"Current hash:  {curr.get('hash', 'N/A')}")

    return changes


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: arch-snapshot-compare.py <command> [args...]")
        print("Commands: generate, compare")
        sys.exit(0)

    cmd = sys.argv[1]

    if cmd == "generate":
        report_path = sys.argv[2]
        snapshot_path = sys.argv[3]
        timestamp = sys.argv[4] if len(sys.argv) > 4 else os.environ.get("TIMESTAMP", "unknown")
        generate_snapshot(report_path, snapshot_path, timestamp)

    elif cmd == "compare":
        previous_path = sys.argv[2]
        current_path = sys.argv[3]
        changes = compare_snapshots(previous_path, current_path)

        has_errors = any(c.startswith("!") for c in changes)
        if has_errors:
            sys.exit(1)
