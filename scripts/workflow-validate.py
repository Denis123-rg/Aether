#!/usr/bin/env python3
"""Validate workflow YAML files and architecture rules."""

import glob
import sys
import yaml


def validate_workflow_yamls():
    errors = []
    workflows = glob.glob(".github/workflows/*.yml") + glob.glob(".github/workflows/*.yaml")
    
    for wf in sorted(workflows):
        try:
            with open(wf) as f:
                data = yaml.safe_load(f)
            if data is None:
                errors.append(f"{wf}: empty or invalid")
            else:
                print(f"  ✓ {wf}: valid")
        except yaml.YAMLError as e:
            errors.append(f"{wf}: {e}")
    
    return errors


def validate_arch_rules():
    errors = []
    try:
        with open("arch-rules.yaml") as f:
            data = yaml.safe_load(f)
        for k in ["layers", "rules", "thresholds"]:
            if k not in data:
                errors.append(f"arch-rules.yaml: missing key: {k}")
        print("  ✓ arch-rules.yaml: valid")
    except yaml.YAMLError as e:
        errors.append(f"arch-rules.yaml: {e}")
    
    return errors


if __name__ == "__main__":
    all_errors = []
    all_errors.extend(validate_workflow_yamls())
    all_errors.extend(validate_arch_rules())
    
    if all_errors:
        print("\nERRORS:")
        for e in all_errors:
            print(f"  ✗ {e}", file=sys.stderr)
        sys.exit(1)
    else:
        print("\n✅ All YAML files valid")
