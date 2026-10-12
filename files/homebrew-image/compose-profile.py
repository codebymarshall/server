#!/usr/bin/python3
"""Compose Home Server with the existing Homelab profile; retain one source of defaults."""
import copy
import sys
from pathlib import Path

import yaml


def compose(home: dict, cluster: dict) -> dict:
    result = copy.deepcopy(cluster)
    # Accounts and keys are always private provisioning, never template examples.
    result.pop("passwd", None)
    for section, groups in home.items():
        if not isinstance(groups, dict):
            result[section] = groups
            continue
        target = result.setdefault(section, {})
        for group, entries in groups.items():
            key = "name" if section == "systemd" else "path"
            merged = {entry[key]: entry for entry in target.get(group, [])}
            merged.update({entry[key]: entry for entry in entries})
            target[group] = list(merged.values())
    return result


if __name__ == "__main__":
    home, cluster, destination = map(Path, sys.argv[1:])
    destination.write_text(yaml.safe_dump(compose(yaml.safe_load(home.read_text()),
                                               yaml.safe_load(cluster.read_text())),
                                          sort_keys=False))
