#!/usr/bin/env python3
"""Generate comprehensive architecture reports."""

import argparse
import json
import os
from datetime import datetime


def safe_len(val):
    return len(val) if val else 0


def generate_html_report(report_dir, data):
    """Generate the main HTML architecture report."""
    stats = data.get("stats", {})
    packages = data.get("packages", [])
    forbidden = data.get("forbidden_imports") or []
    warnings = data.get("warnings") or []
    circular = data.get("import_graph", {}).get("circular_imports") or []

    html = [
        "<!DOCTYPE html>",
        '<html lang="en">',
        "<head>",
        '<meta charset="UTF-8">',
        '<meta name="viewport" content="width=device-width, initial-scale=1.0">',
        "<title>Aether Architecture Report</title>",
        "<style>",
        "body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0d1117;color:#c9d1d9;padding:20px;max-width:1200px;margin:0 auto}",
        "h1{color:#58a6ff;border-bottom:2px solid #30363d;padding-bottom:10px}",
        "h2{color:#8b949e;margin-top:30px}",
        ".stat{display:inline-block;background:#161b22;border:1px solid #30363d;border-radius:8px;padding:15px 25px;margin:5px;text-align:center}",
        ".stat .val{font-size:28px;font-weight:700;color:#58a6ff}",
        ".stat .lbl{font-size:11px;color:#8b949e;text-transform:uppercase}",
        ".error{color:#f85149}",
        ".success{color:#3fb950}",
        "table{width:100%;border-collapse:collapse;margin:15px 0}",
        "th,td{padding:10px 14px;text-align:left;border-bottom:1px solid #21262d;font-size:13px}",
        "th{background:#161b22;color:#8b949e;font-weight:600}",
        "tr:hover{background:#1c2128}",
        ".badge{display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600}",
        ".badge-pass{background:#1b3a2d;color:#3fb950}",
        ".badge-fail{background:#3d1f1e;color:#f85149}",
        ".meta{color:#484f58;font-size:12px;margin-bottom:20px}",
        "</style>",
        "</head>",
        "<body>",
        "<h1>Aether Architecture Report</h1>",
        f'<p class="meta">Generated: {datetime.now().strftime("%Y-%m-%d %H:%M:%S")} | Report version 1.0</p>',
        "<h2>Statistics</h2>",
    ]

    stat_blocks = [
        ("Packages", stats.get("total_packages", 0)),
        ("Files", stats.get("total_files", 0)),
        ("Imports", stats.get("total_imports", 0)),
        ("Functions", stats.get("total_functions", 0)),
        ("Types", stats.get("total_types", 0)),
        ("Interfaces", stats.get("total_interfaces", 0)),
        ("Structs", stats.get("total_structs", 0)),
    ]
    for label, value in stat_blocks:
        html.append(f'<div class="stat"><div class="val">{value}</div><div class="lbl">{label}</div></div>')

    html.append("<h2>Health Checks</h2>")

    circular_count = stats.get("circular_deps", 0)
    if circular_count > 0:
        html.append(f'<div class="stat"><div class="val error">{circular_count}</div><div class="lbl">Circular Dependencies</div></div>')
    else:
        html.append(f'<div class="stat"><div class="val success">0</div><div class="lbl">Circular Dependencies</div></div>')

    forbidden_count = stats.get("forbidden_deps", 0)
    if forbidden_count > 0:
        html.append(f'<div class="stat"><div class="val error">{forbidden_count}</div><div class="lbl">Forbidden Dependencies</div></div>')
    else:
        html.append(f'<div class="stat"><div class="val success">0</div><div class="lbl">Forbidden Dependencies</div></div>')

    if warnings:
        html.append("<h2>Warnings</h2><ul>")
        for w in warnings:
            html.append(f'<li class="error">{w}</li>')
        html.append("</ul>")

    if forbidden:
        html.append("<h2>Forbidden Imports</h2><table><tr><th>Source</th><th>Target</th><th>Message</th></tr>")
        for fi in forbidden:
            html.append(f"<tr><td>{fi['source']}</td><td>{fi['target']}</td><td>{fi['message']}</td></tr>")
        html.append("</table>")

    if circular:
        html.append("<h2>Circular Dependencies</h2><ul>")
        for cycle in circular:
            html.append(f"<li>{' → '.join(cycle)}</li>")
        html.append("</ul>")

    html.append("<h2>Packages</h2><table><tr><th>Package</th><th>Files</th><th>Functions</th><th>Types</th><th>Interfaces</th><th>Structs</th></tr>")

    for pkg in packages:
        files = safe_len(pkg.get("files"))
        funcs = sum(safe_len(f.get("functions")) for f in (pkg.get("files") or []))
        types = sum(safe_len(f.get("types")) for f in (pkg.get("files") or []))
        ifaces = sum(safe_len(f.get("interfaces")) for f in (pkg.get("files") or []))
        structs = sum(safe_len(f.get("structs")) for f in (pkg.get("files") or []))
        html.append(f"<tr><td>{pkg['path']}</td><td>{files}</td><td>{funcs}</td><td>{types}</td><td>{ifaces}</td><td>{structs}</td></tr>")
    html.append("</table>")

    html.append("</body></html>")
    return "\n".join(html)


def generate_json_reports(report_dir, data):
    """Generate JSON reports for further processing."""
    stats = data.get("stats", {})
    packages = data.get("packages", [])
    forbidden = data.get("forbidden_imports") or []
    import_graph = data.get("import_graph", {})

    # Stats report
    with open(os.path.join(report_dir, "stats.json"), "w") as f:
        json.dump({
            "timestamp": datetime.now().isoformat(),
            "stats": stats,
            "summary": {
                "has_circular_deps": stats.get("circular_deps", 0) > 0,
                "has_forbidden_deps": stats.get("forbidden_deps", 0) > 0,
                "total_violations": stats.get("circular_deps", 0) + stats.get("forbidden_deps", 0),
            }
        }, f, indent=2)

    # Packages report
    pkg_report = []
    for pkg in packages:
        files = safe_len(pkg.get("files"))
        funcs = sum(safe_len(f.get("functions")) for f in (pkg.get("files") or []))
        types = sum(safe_len(f.get("types")) for f in (pkg.get("files") or []))
        ifaces = sum(safe_len(f.get("interfaces")) for f in (pkg.get("files") or []))
        structs = sum(safe_len(f.get("structs")) for f in (pkg.get("files") or []))
        pkg_report.append({
            "path": pkg["path"],
            "name": pkg["name"],
            "files": files,
            "functions": funcs,
            "types": types,
            "interfaces": ifaces,
            "structs": structs,
        })
    with open(os.path.join(report_dir, "packages.json"), "w") as f:
        json.dump(pkg_report, f, indent=2)

    # Import graph report
    graph_report = {
        "nodes": import_graph.get("nodes") or [],
        "edge_count": len(import_graph.get("edges") or []),
        "circular_cycles": import_graph.get("circular_imports") or [],
        "circular_count": len(import_graph.get("circular_imports") or []),
    }
    with open(os.path.join(report_dir, "import-graph.json"), "w") as f:
        json.dump(graph_report, f, indent=2)

    # Violations report
    violations = []
    for fi in forbidden:
        violations.append({
            "type": "forbidden_import",
            "severity": "error",
            "source": fi["source"],
            "target": fi["target"],
            "message": fi["message"],
        })
    with open(os.path.join(report_dir, "violations.json"), "w") as f:
        json.dump(violations, f, indent=2)

    print("JSON reports generated successfully")


def generate_markdown_report(report_dir, data):
    """Generate Markdown architecture report."""
    stats = data.get("stats", {})
    packages = data.get("packages", [])
    forbidden = data.get("forbidden_imports") or []
    circular = data.get("import_graph", {}).get("circular_imports") or []

    lines = [
        "# Aether Architecture Report",
        "",
        f"**Generated:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}",
        "",
        "## Summary",
        "",
        f"- **Total Packages:** {stats.get('total_packages', 0)}",
        f"- **Total Files:** {stats.get('total_files', 0)}",
        f"- **Total Imports:** {stats.get('total_imports', 0)}",
        f"- **Total Functions:** {stats.get('total_functions', 0)}",
        f"- **Total Types:** {stats.get('total_types', 0)}",
        f"- **Total Interfaces:** {stats.get('total_interfaces', 0)}",
        f"- **Total Structs:** {stats.get('total_structs', 0)}",
        f"- **Circular Dependencies:** {stats.get('circular_deps', 0)}",
        f"- **Forbidden Dependencies:** {stats.get('forbidden_deps', 0)}",
        "",
        "## Health",
        "",
    ]

    if stats.get("circular_deps", 0) == 0 and stats.get("forbidden_deps", 0) == 0:
        lines.append("✅ **All architecture checks passed.**")
    else:
        lines.append("❌ **Architecture violations detected.**")
        if stats.get("circular_deps", 0) > 0:
            lines.append(f"  - {stats['circular_deps']} circular dependencies")
        if stats.get("forbidden_deps", 0) > 0:
            lines.append(f"  - {stats['forbidden_deps']} forbidden dependencies")

    if forbidden:
        lines.extend(["", "## Forbidden Imports", ""])
        for fi in forbidden:
            lines.append(f"- ❌ `{fi['source']}` → `{fi['target']}`: {fi['message']}")

    if circular:
        lines.extend(["", "## Circular Dependencies", ""])
        for cycle in circular:
            lines.append(f"- 🔄 {' → '.join(cycle)}")

    lines.extend(["", "## Packages", "", "| Package | Files | Functions | Types | Interfaces | Structs |", "|---------|-------|-----------|-------|------------|---------|"])
    for pkg in packages:
        files = safe_len(pkg.get("files"))
        funcs = sum(safe_len(f.get("functions")) for f in (pkg.get("files") or []))
        types = sum(safe_len(f.get("types")) for f in (pkg.get("files") or []))
        ifaces = sum(safe_len(f.get("interfaces")) for f in (pkg.get("files") or []))
        structs = sum(safe_len(f.get("structs")) for f in (pkg.get("files") or []))
        lines.append(f"| {pkg['path']} | {files} | {funcs} | {types} | {ifaces} | {structs} |")

    lines.append("")
    with open(os.path.join(report_dir, "arch-report.md"), "w") as f:
        f.write("\n".join(lines))
    print("Markdown report generated")


def main():
    parser = argparse.ArgumentParser(description="Generate architecture reports")
    parser.add_argument("--report", required=True, help="Path to architecture report JSON")
    parser.add_argument("--output", required=True, help="Output directory for reports")
    args = parser.parse_args()

    os.makedirs(args.output, exist_ok=True)

    with open(args.report) as f:
        data = json.load(f)

    html = generate_html_report(args.output, data)
    with open(os.path.join(args.output, "architecture-report.html"), "w") as f:
        f.write(html)
    print("Generated: architecture-report.html")

    generate_json_reports(args.output, data)
    generate_markdown_report(args.output, data)

    print("All reports generated successfully")


if __name__ == "__main__":
    main()
