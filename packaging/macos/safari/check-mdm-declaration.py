#!/usr/bin/env python3
"""Validate the Safari extension-settings declaration shipped for MDM.

Checks the declaration against Apple's published schema for
com.apple.configuration.safari.extensions.settings
(https://github.com/apple/device-management/blob/release/declarative/declarations/configurations/safari.extensions.settings.yaml)
and against Beacon's own contract: the declaration names the collector's
extension and grants every host the extension's manifest asks for.

Usage:
  check-mdm-declaration.py DECLARATION.json --manifest MANIFEST.json \
      --extension-bundle-id ID

Standard library only, so it runs on a stock macOS or Linux Python 3.
"""

import argparse
import json
import re
import sys
from urllib.parse import urlsplit

DECLARATION_TYPE = "com.apple.configuration.safari.extensions.settings"
STATE_VALUES = {"Allowed", "AlwaysOn", "AlwaysOff"}
EXTENSION_KEYS = {"State", "PrivateBrowsing", "AllowedDomains", "DeniedDomains"}
# DDM declarations carry these top-level keys; ServerToken is required.
DECLARATION_KEYS = {"Type", "Identifier", "ServerToken", "Payload"}
# "Identifier (TeamIdentifier)"; Apple team IDs are 10 upper-case alphanumerics.
COMPOSED_ID = re.compile(r"^(?P<bundle>[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+) \((?P<team>[A-Z0-9]{10})\)$")
# Domain entries are bare hosts, optionally with a leading "*" wildcard, or "*".
DOMAIN = re.compile(r"^(\*|\*?[A-Za-z0-9.-]+)$")


def manifest_hosts(manifest):
    hosts = set()
    for pattern in manifest.get("host_permissions", []):
        host = urlsplit(pattern.replace("*://", "https://", 1)).hostname
        if host:
            hosts.add(host)
    return hosts


def check(declaration, manifest, extension_bundle_id):
    errors = []

    if not isinstance(declaration, dict):
        return ["declaration must be a JSON object"]
    missing = DECLARATION_KEYS - declaration.keys()
    if missing:
        errors.append(f"missing top-level keys: {sorted(missing)}")
    extra = declaration.keys() - DECLARATION_KEYS
    if extra:
        errors.append(f"unknown top-level keys: {sorted(extra)}")
    if declaration.get("Type") != DECLARATION_TYPE:
        errors.append(f"Type must be {DECLARATION_TYPE!r}, got {declaration.get('Type')!r}")
    for key in ("Identifier", "ServerToken"):
        value = declaration.get(key)
        if not isinstance(value, str) or not value.strip():
            errors.append(f"{key} must be a non-empty string")

    payload = declaration.get("Payload")
    if not isinstance(payload, dict):
        return errors + ["Payload must be an object"]
    if payload.keys() - {"ManagedExtensions"}:
        errors.append(f"unknown Payload keys: {sorted(payload.keys() - {'ManagedExtensions'})}")
    managed = payload.get("ManagedExtensions")
    if not isinstance(managed, dict) or not managed:
        return errors + ["Payload.ManagedExtensions must be a non-empty object"]

    beacon_entry = None
    for composed, settings in managed.items():
        where = f"ManagedExtensions[{composed!r}]"
        match = COMPOSED_ID.match(composed)
        if composed != "*" and not match:
            errors.append(f"{where}: key must be '*' or 'bundle.id (TEAMID)' with a 10-character team ID")
        if not isinstance(settings, dict):
            errors.append(f"{where}: settings must be an object")
            continue
        unknown = settings.keys() - EXTENSION_KEYS
        if unknown:
            errors.append(f"{where}: unknown keys {sorted(unknown)}")
        for key in ("State", "PrivateBrowsing"):
            if key in settings and settings[key] not in STATE_VALUES:
                errors.append(f"{where}.{key}: must be one of {sorted(STATE_VALUES)}, got {settings[key]!r}")
        for key in ("AllowedDomains", "DeniedDomains"):
            if key not in settings:
                continue
            domains = settings[key]
            if not isinstance(domains, list) or not all(isinstance(d, str) for d in domains):
                errors.append(f"{where}.{key}: must be an array of strings")
                continue
            for d in domains:
                if not DOMAIN.match(d):
                    errors.append(f"{where}.{key}: {d!r} is not a bare domain (no scheme, port, or path)")
        if composed == "*" and "AllowedDomains" in settings:
            errors.append(f"{where}: AllowedDomains is ignored for the '*' identifier")
        if match and match.group("bundle") == extension_bundle_id:
            beacon_entry = settings

    if beacon_entry is None:
        errors.append(f"no ManagedExtensions entry for the collector extension {extension_bundle_id!r}")
    else:
        def domain_set(key):
            value = beacon_entry.get(key, [])
            # Shape errors were reported above; don't crash on them here.
            return {d for d in value if isinstance(d, str)} if isinstance(value, list) else set()

        allowed = domain_set("AllowedDomains")
        denied = domain_set("DeniedDomains")
        wanted = manifest_hosts(manifest)
        if not wanted:
            errors.append("manifest declares no host_permissions to compare against")
        missing_hosts = wanted - allowed
        if missing_hosts:
            errors.append(f"collector entry does not allow manifest hosts: {sorted(missing_hosts)}")
        blocked = wanted & denied
        if blocked:
            errors.append(f"collector entry denies manifest hosts: {sorted(blocked)}")
        if beacon_entry.get("State") == "AlwaysOff":
            errors.append("collector entry turns the extension off (State AlwaysOff)")

    return errors


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("declaration")
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--extension-bundle-id", required=True)
    args = parser.parse_args(argv)

    try:
        with open(args.declaration, encoding="utf-8") as f:
            declaration = json.load(f)
        with open(args.manifest, encoding="utf-8") as f:
            manifest = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    errors = check(declaration, manifest, args.extension_bundle_id)
    for e in errors:
        print(f"error: {args.declaration}: {e}", file=sys.stderr)
    if errors:
        return 1
    print(f"ok: {args.declaration}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
