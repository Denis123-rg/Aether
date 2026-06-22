#!/usr/bin/env python3
"""Generate architecture diagrams from analysis report."""

import argparse
import json
import os


def generate_mermaid_import_graph(report):
    lines = [
        "---",
        "title: Aether Import Dependency Graph",
        "---",
        "graph TD",
    ]
    edges = report.get("import_graph", {}).get("edges", [])
    seen = set()
    for e in edges:
        src = e["source"].replace("/", "_").replace("-", "_")
        tgt = e["target"].replace("/", "_").replace("-", "_")
        key = (src, tgt)
        if key not in seen:
            seen.add(key)
            lines.append(f"    {src}[{e['source']}] --> {tgt}[{e['target']}]")
    return "\n".join(lines)


def generate_mermaid_layer_graph():
    return """---
title: Aether Architecture Layer Diagram
---
graph TD
    subgraph Handler["Handler Layer"]
        H1["cmd/executor"]
        H2["cmd/monitor"]
        H3["cmd/telebot"]
        H4["cmd/signer"]
        H5["cmd/reconciler"]
    end
    subgraph Service["Service Layer"]
        S1["internal/risk"]
        S2["internal/strategy"]
        S3["internal/events"]
        S4["internal/grpc"]
        S5["internal/signer"]
        S6["internal/tracing"]
    end
    subgraph Domain["Domain Layer"]
        D1["internal/pb"]
        D2["internal/metrics"]
        D3["internal/testutil"]
    end
    subgraph DB["Database Layer"]
        DB1["internal/db"]
    end
    subgraph Config["Config Layer"]
        C1["internal/config"]
    end
    subgraph Rust["Rust Core"]
        R1["crates/grpc-server"]
        R2["crates/detector"]
        R3["crates/pools"]
        R4["crates/simulator"]
        R5["crates/state"]
        R6["crates/ingestion"]
        R7["crates/discovery"]
    end
    H1 --> S1
    H1 --> S4
    H1 --> S5
    H2 --> S3
    H3 --> S3
    H4 --> S5
    S1 --> C1
    S2 --> C1
    S4 --> R1
    S3 --> DB1
    DB1 --> C1"""


def generate_mermaid_component_diagram():
    return """---
title: Aether Component Architecture
---
graph TB
    subgraph External["External"]
        ETH["Ethereum RPC"]
        MEMPOOL["Mempool"]
        BUILDERS["Block Builders"]
    end
    subgraph Rust["Rust Core"]
        INGEST["Ingestion"]
        DISCOVERY["Discovery"]
        STATE["State/Graph"]
        DETECTOR["Detector"]
        SIM["Simulator"]
        GRPC["gRPC Server"]
    end
    subgraph Go["Go Coordination"]
        EXEC["Executor"]
        MON["Monitor"]
        TEL["Telebot"]
    end
    subgraph Storage["Storage"]
        DB["PostgreSQL"]
        REDIS["Redis"]
    end
    ETH --> INGEST
    MEMPOOL --> INGEST
    INGEST --> STATE
    DISCOVERY --> STATE
    STATE --> DETECTOR
    DETECTOR --> SIM
    SIM --> GRPC
    GRPC --> EXEC
    EXEC --> BUILDERS
    EXEC --> DB
    EXEC --> REDIS
    MON --> REDIS
    TEL --> REDIS"""


def generate_graphviz_dep_graph(report):
    lines = [
        "digraph AetherDependencies {",
        '    rankdir=LR;',
        '    splines=true;',
        '    node [shape=box, style=filled, fillcolor=lightyellow];',
        '    edge [color=gray];',
    ]
    edges = report.get("import_graph", {}).get("edges", [])
    seen = set()
    for e in edges:
        src = e["source"]
        tgt = e["target"]
        key = (src, tgt)
        if key not in seen:
            seen.add(key)
            safe_src = src.replace("-", "_").replace("/", "_")
            safe_tgt = tgt.replace("-", "_").replace("/", "_")
            lines.append(f'    "{safe_src}" [label="{src}"];')
            lines.append(f'    "{safe_tgt}" [label="{tgt}"];')
            lines.append(f'    "{safe_src}" -> "{safe_tgt}";')
    lines.append("}")
    return "\n".join(lines)


def safe_len(val):
    return len(val) if val else 0

def generate_package_table(report):
    rows = []
    for pkg in report.get("packages", []):
        files = safe_len(pkg.get("files"))
        funcs = sum(safe_len(f.get("functions")) for f in (pkg.get("files") or []))
        types = sum(safe_len(f.get("types")) for f in (pkg.get("files") or []))
        ifaces = sum(safe_len(f.get("interfaces")) for f in (pkg.get("files") or []))
        rows.append({
            "path": pkg["path"],
            "name": pkg["name"],
            "files": files,
            "functions": funcs,
            "types": types,
            "interfaces": ifaces,
        })
    return rows


def main():
    parser = argparse.ArgumentParser(description="Generate architecture diagrams")
    parser.add_argument("--report", required=True, help="Path to architecture report JSON")
    parser.add_argument("--output", required=True, help="Output directory for diagrams")
    args = parser.parse_args()

    os.makedirs(args.output, exist_ok=True)

    with open(args.report) as f:
        report = json.load(f)

    # Generate Mermaid import graph
    with open(os.path.join(args.output, "import-graph.mermaid"), "w") as f:
        f.write(generate_mermaid_import_graph(report))
    print("Generated: import-graph.mermaid")

    # Generate Mermaid layer graph
    with open(os.path.join(args.output, "layer-graph.mermaid"), "w") as f:
        f.write(generate_mermaid_layer_graph())
    print("Generated: layer-graph.mermaid")

    # Generate Mermaid component diagram
    with open(os.path.join(args.output, "component-diagram.mermaid"), "w") as f:
        f.write(generate_mermaid_component_diagram())
    print("Generated: component-diagram.mermaid")

    # Generate Graphviz dependency graph
    with open(os.path.join(args.output, "dep-graph.dot"), "w") as f:
        f.write(generate_graphviz_dep_graph(report))
    print("Generated: dep-graph.dot")

    # Generate package table JSON
    with open(os.path.join(args.output, "packages.json"), "w") as f:
        json.dump(generate_package_table(report), f, indent=2)
    print("Generated: packages.json")

    # Generate layer JSON
    layers = [
        {"name": "Handler", "dirs": ["cmd/executor", "cmd/monitor", "cmd/telebot", "cmd/signer", "cmd/reconciler"]},
        {"name": "Service", "dirs": ["internal/risk", "internal/strategy", "internal/events", "internal/grpc", "internal/signer", "internal/tracing"]},
        {"name": "Domain", "dirs": ["internal/pb", "internal/metrics", "internal/testutil"]},
        {"name": "Database", "dirs": ["internal/db"]},
        {"name": "Config", "dirs": ["internal/config"]},
    ]
    with open(os.path.join(args.output, "layers.json"), "w") as f:
        json.dump(layers, f, indent=2)
    print("Generated: layers.json")

    print("All diagrams generated successfully")


if __name__ == "__main__":
    main()
