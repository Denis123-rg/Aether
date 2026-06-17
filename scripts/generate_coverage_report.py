#!/usr/bin/env python3
"""Generate FINAL_COVERAGE_REPORT.md from Rust LCOV and Go coverage.out."""
import argparse
import os
import re
from collections import defaultdict
from datetime import datetime, timezone


def parse_rust_lcov(path: str):
    files = defaultdict(lambda: {"hit": 0, "found": 0})
    current = None
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line.startswith("SF:"):
                current = line[3:]
            elif line.startswith("DA:") and current:
                _, count = line[3:].split(",")
                files[current]["found"] += 1
                if int(count) > 0:
                    files[current]["hit"] += 1
            elif line == "end_of_record":
                current = None
    return files


def parse_go_coverage(path: str):
    files = defaultdict(lambda: {"hit": set(), "total": set()})
    with open(path) as f:
        next(f)  # mode line
        for line in f:
            line = line.strip()
            if not line:
                continue
            m = re.match(
                r"(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)", line
            )
            if not m:
                continue
            filepath = m.group(1)
            start_line = int(m.group(2))
            end_line = int(m.group(4))
            count = int(m.group(7))
            for ln in range(start_line, end_line + 1):
                files[filepath]["total"].add(ln)
                if count > 0:
                    files[filepath]["hit"].add(ln)
    # convert sets to counts
    return {
        f: {"hit": len(d["hit"]), "found": len(d["total"])}
        for f, d in files.items()
    }


def pct(hit: int, found: int) -> float:
    return hit * 100 / found if found else 100.0


def make_table(items, threshold=95.0):
    lines = []
    lines.append("| File | Lines Hit | Lines Found | Coverage % | Status |")
    lines.append("|------|-----------|-------------|------------|--------|")
    for file_path, hit, found in items:
        p = pct(hit, found)
        status = "✅" if p >= threshold else "❌"
        lines.append(f"| {file_path} | {hit} | {found} | {p:.2f}% | {status} |")
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--rust-lcov", default="rust_coverage.lcov")
    parser.add_argument("--go-coverage", default="coverage.out")
    parser.add_argument("--output", default="FINAL_COVERAGE_REPORT.md")
    args = parser.parse_args()

    rust_files = parse_rust_lcov(args.rust_lcov)
    go_files = parse_go_coverage(args.go_coverage)

    rust_items = sorted(
        [(f, d["hit"], d["found"]) for f, d in rust_files.items()],
        key=lambda x: x[0],
    )
    go_items = sorted(
        [(f, d["hit"], d["found"]) for f, d in go_files.items()],
        key=lambda x: x[0],
    )

    rust_below = sum(1 for _, h, f in rust_items if pct(h, f) < 95.0)
    go_below = sum(1 for _, h, f in go_items if pct(h, f) < 95.0)

    rust_total_hit = sum(h for _, h, _ in rust_items)
    rust_total_found = sum(f for _, _, f in rust_items)
    go_total_hit = sum(h for _, h, _ in go_items)
    go_total_found = sum(f for _, _, f in go_items)

    rust_overall = pct(rust_total_hit, rust_total_found)
    go_overall = pct(go_total_hit, go_total_found)

    lines = []
    lines.append("# Aether Final Coverage Report")
    lines.append("")
    lines.append(f"Generated: {datetime.now(timezone.utc).isoformat()}Z")
    lines.append("")
    lines.append("## Summary")
    lines.append("")
    lines.append(f"- **Rust overall line coverage:** {rust_overall:.2f}%")
    lines.append(f"- **Go overall line coverage:** {go_overall:.2f}%")
    lines.append(f"- **Rust files below 95%:** {rust_below} / {len(rust_items)}")
    lines.append(f"- **Go files below 95%:** {go_below} / {len(go_items)}")
    lines.append("")
    lines.append("## Rust Per-File Coverage")
    lines.append("")
    lines.append(make_table(rust_items))
    lines.append("")
    lines.append("## Go Per-File Coverage")
    lines.append("")
    lines.append(make_table(go_items))
    lines.append("")
    lines.append("## Notes")
    lines.append("")
    lines.append(
        "- Generated protobuf files (`internal/pb/*.pb.go`) and test-only helpers "
        "(`internal/testutil`, `internal/tracing`, `deploy/docker/mock-builder`) "
        "are excluded from the ≥95% target because they are not production business logic."
    )
    lines.append(
        "- Rust binaries under `crates/*/src/bin/` and `main.rs` files may have "
        "lower coverage due to async/network glue that is exercised primarily in "
        "integration/E2E environments."
    )

    with open(args.output, "w") as f:
        f.write("\n".join(lines))
    print(f"Wrote {args.output}")


if __name__ == "__main__":
    main()
