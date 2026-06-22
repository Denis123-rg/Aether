#!/usr/bin/env python3
"""Architecture rule validation for Aether.

Reads the architecture report (JSON) and validates against rules YAML.
Outputs a validation report with pass/fail for each rule.
"""

import argparse
import json
import os
import sys
from pathlib import Path
import yaml


def load_rules(rules_path):
    with open(rules_path) as f:
        return yaml.safe_load(f)


def load_report(report_path):
    with open(report_path) as f:
        return json.load(f)


def check_layer_violations(report, rules):
    """Check that packages in each layer only import allowed packages."""
    violations = []
    layers = rules.get("layers", [])

    pkg_imports = {}
    for pkg in report.get("packages", []):
        pkg_path = pkg["path"]
        imports = set()
        for f in pkg.get("files", []):
            for imp in (f.get("imports") or []):
                imports.add(imp)
        pkg_imports[pkg_path] = imports

    for layer in layers:
        layer_pkgs = layer.get("packages", [])
        cannot_import = layer.get("cannot_import", [])

        for lpkg in layer_pkgs:
            for pkg_path, imports in pkg_imports.items():
                is_in_layer = False
                if lpkg == pkg_path or pkg_path.startswith(lpkg + "/"):
                    is_in_layer = True

                if not is_in_layer:
                    continue

                for imp in imports:
                    for forbidden in cannot_import:
                        if forbidden in imp or imp in forbidden:
                            violations.append({
                                "type": "forbidden_import",
                                "layer": layer["name"],
                                "source": pkg_path,
                                "target": imp,
                                "message": f"Layer '{layer['name']}': {pkg_path} must not import {imp}",
                                "severity": "error",
                            })

    return violations


def check_circular_deps(report):
    """Check for circular dependencies."""
    violations = []
    circular = report.get("import_graph", {}).get("circular_imports") or []
    for cycle in circular:
        violations.append({
            "type": "circular_dependency",
            "cycle": cycle,
            "message": f"Circular dependency detected: {' -> '.join(cycle)}",
            "severity": "error",
        })
    return violations


def check_stats_thresholds(report, rules):
    """Check statistics against thresholds."""
    violations = []
    thresholds = rules.get("thresholds", {})
    stats = report.get("stats", {})

    max_circular = thresholds.get("max_circular_deps", 0)
    if stats.get("circular_deps", 0) > max_circular:
        violations.append({
            "type": "threshold_exceeded",
            "metric": "circular_deps",
            "value": stats["circular_deps"],
            "threshold": max_circular,
            "message": f"Circular dependencies ({stats['circular_deps']}) exceed threshold ({max_circular})",
            "severity": "error",
        })

    max_forbidden = thresholds.get("max_forbidden_deps", 0)
    if stats.get("forbidden_deps", 0) > max_forbidden:
        violations.append({
            "type": "threshold_exceeded",
            "metric": "forbidden_deps",
            "value": stats["forbidden_deps"],
            "threshold": max_forbidden,
            "message": f"Forbidden dependencies ({stats['forbidden_deps']}) exceed threshold ({max_forbidden})",
            "severity": "error",
        })

    return violations


def generate_validation_report(report, rules):
    """Generate complete validation report."""
    results = {
        "summary": {
            "total_rules_checked": 0,
            "passed": 0,
            "failed": 0,
            "warnings": 0,
            "has_errors": False,
        },
        "violations": [],
    }

    violations = []
    violations.extend(check_layer_violations(report, rules))
    violations.extend(check_circular_deps(report))
    violations.extend(check_stats_thresholds(report, rules))

    results["violations"] = violations
    results["summary"]["total_rules_checked"] = len(violations) + 3
    results["summary"]["passed"] = sum(1 for v in violations if v.get("severity") != "error")
    results["summary"]["failed"] = sum(1 for v in violations if v.get("severity") == "error")
    results["summary"]["warnings"] = sum(1 for v in violations if v.get("severity") == "warning")
    results["summary"]["has_errors"] = any(v.get("severity") == "error" for v in violations)

    return results


def main():
    parser = argparse.ArgumentParser(description="Validate architecture rules")
    parser.add_argument("--rules", required=True, help="Path to architecture rules YAML")
    parser.add_argument("--report", required=True, help="Path to architecture report JSON")
    parser.add_argument("--output", required=True, help="Path to output validation report")
    args = parser.parse_args()

    rules = load_rules(args.rules)
    report = load_report(args.report)

    results = generate_validation_report(report, rules)

    with open(args.output, "w") as f:
        json.dump(results, f, indent=2)

    summary = results["summary"]
    print(f"Architecture Validation Results:")
    print(f"  Rules checked: {summary['total_rules_checked']}")
    print(f"  Passed:        {summary['passed']}")
    print(f"  Failed:        {summary['failed']}")
    print(f"  Warnings:      {summary['warnings']}")

    for v in results["violations"]:
        level = "ERROR" if v.get("severity") == "error" else "WARN"
        print(f"  [{level}] {v['message']}")

    if summary["has_errors"]:
        print("\n❌ Architecture validation FAILED")
        sys.exit(1)
    else:
        print("\n✅ Architecture validation PASSED")


if __name__ == "__main__":
    main()
