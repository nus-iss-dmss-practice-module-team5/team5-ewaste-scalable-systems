#!/usr/bin/env python3
"""Decide which services the CD pipeline builds, tests, scans and deploys (EWSS-19).

Each service in .github/service-catalog.json ships on its own. A service is
selected when one of its paths changed, when it is named explicitly, or when a
pipeline file changed (then every available service ships).

A service is available only when its code exists in this checkout (entrypoint
files, Dockerfile build target) and, when an environment is given, its
container app exists there. Members of a `group` (the four Go services) are
available only together, because each serves part of one API. A legacy entry
(`replaced_by`) is used only while its replacement is unavailable, so the
pipeline keeps working before and after the split into separate services lands.

Prints a JSON plan: {"include": [...], "skipped": [...], "groups": {...}}.
Exits 2 when a service that was asked for by name cannot ship.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

APP_PREFIX = "aca-ewaste"
REQUIRED_FIELDS = ("name", "stack", "image", "dockerfile", "context", "app", "paths")
STACKS = ("go", "python", "node")
DEPLOY_MODES = ("image", "matcher-script")
NAME_PATTERN = re.compile(r"^[a-z][a-z0-9-]{1,40}$")


class PlanError(Exception):
    """The catalogue or the request is invalid; nothing should ship."""


def load_catalog(path: Path) -> dict:
    catalog = json.loads(path.read_text(encoding="utf-8"))
    services = catalog.get("services")
    if not isinstance(services, list) or not services:
        raise PlanError("service catalogue has no services")
    names = set()
    for service in services:
        missing = [field for field in REQUIRED_FIELDS if not service.get(field)]
        if missing:
            raise PlanError(f"service {service.get('name', '?')} is missing {missing}")
        name = service["name"]
        if not NAME_PATTERN.match(name) or not NAME_PATTERN.match(service["app"]):
            raise PlanError(f"service {name}: name and app must be lowercase kebab-case")
        if name in names:
            raise PlanError(f"service {name} is listed twice")
        if service["stack"] not in STACKS:
            raise PlanError(f"service {name}: unknown stack {service['stack']}")
        if service.get("deploy_mode", "image") not in DEPLOY_MODES:
            raise PlanError(f"service {name}: unknown deploy_mode {service['deploy_mode']}")
        if "group" in service and not NAME_PATTERN.match(service["group"]):
            raise PlanError(f"service {name}: group must be lowercase kebab-case")
        names.add(name)
    for service in services:
        for ref in [service.get("replaced_by")] + list(service.get("requires_services", [])):
            if ref and ref not in names:
                raise PlanError(f"service {service['name']} refers to unknown service {ref}")
    catalog.setdefault("global_paths", [])
    return catalog


def app_name(app: str, environment: str) -> str:
    return f"{APP_PREFIX}-{environment}-{app}" if environment else ""


def dockerfile_has_target(dockerfile: Path, target: str) -> bool:
    pattern = re.compile(rf"^\s*FROM\s+\S+\s+AS\s+{re.escape(target)}\s*$", re.IGNORECASE)
    return any(pattern.match(line) for line in dockerfile.read_text(encoding="utf-8").splitlines())


def code_problem(service: dict, repo_root: Path) -> str | None:
    """Why this checkout cannot build the service, or None when it can."""
    dockerfile = repo_root / service["dockerfile"]
    if not dockerfile.is_file():
        return f"{service['dockerfile']} does not exist"
    for required in service.get("requires_files", []):
        if not (repo_root / required).is_file():
            return f"{required} does not exist yet"
    target = service.get("target")
    if target and not dockerfile_has_target(dockerfile, target):
        return f"{service['dockerfile']} has no '{target}' build target yet"
    return None


def group_members(catalog: dict) -> dict[str, list[str]]:
    groups: dict[str, list[str]] = {}
    for service in catalog["services"]:
        if service.get("group"):
            groups.setdefault(service["group"], []).append(service["name"])
    return groups


def availability(catalog: dict, repo_root: Path, environment: str,
                 existing_apps: set[str] | None) -> dict[str, str | None]:
    """Maps each service to None (available) or the reason it cannot ship."""
    by_name = {service["name"]: service for service in catalog["services"]}
    result: dict[str, str | None] = {}

    def resolve(name: str, trail: tuple[str, ...] = ()) -> str | None:
        if name in result:
            return result[name]
        if name in trail:
            raise PlanError(f"requires_services cycle: {' -> '.join(trail + (name,))}")
        service = by_name[name]
        reason = code_problem(service, repo_root)
        if reason is None and existing_apps is not None:
            full = app_name(service["app"], environment)
            if full not in existing_apps:
                reason = f"container app {full} does not exist in {environment}"
        if reason is None:
            for dependency in service.get("requires_services", []):
                if resolve(dependency, trail + (name,)) is not None:
                    reason = f"needs {dependency}, which cannot ship yet"
                    break
        result[name] = reason
        return reason

    for name in by_name:
        resolve(name)
    for group, members in group_members(catalog).items():
        blocked = [f"{m} ({result[m]})" for m in members if result[m] is not None]
        if blocked:
            for member in members:
                if result[member] is None:
                    result[member] = f"group {group} cannot ship together: {'; '.join(blocked)}"
    for service in catalog["services"]:
        replacement = service.get("replaced_by")
        if replacement and result[service["name"]] is None and result[replacement] is None:
            result[service["name"]] = f"replaced by {replacement}"
    return result


def _matches(path: str, pattern: str) -> bool:
    if pattern.endswith("/"):
        return path.startswith(pattern)
    return path == pattern


def touches(service: dict, changed_file: str) -> bool:
    """The most specific matching pattern decides; exclusions narrow shared folders."""
    rules = [(pattern, True) for pattern in service["paths"]]
    rules += [(pattern, False) for pattern in service.get("exclude_paths", [])]
    matching = [(len(pattern), include) for pattern, include in rules if _matches(changed_file, pattern)]
    if not matching:
        return False
    return max(matching)[1]


def plan(catalog: dict, repo_root: Path, environment: str, existing_apps: set[str] | None,
         changed_files: list[str] | None, requested: list[str] | None) -> dict:
    status = availability(catalog, repo_root, environment, existing_apps)
    services = catalog["services"]
    by_name = {service["name"]: service for service in services}
    skipped = []

    if requested:
        unknown = [name for name in requested if name not in by_name and name != "all"]
        if unknown:
            raise PlanError(f"unknown service(s): {', '.join(unknown)}")
        if "all" in requested:
            selected = [s for s in services if status[s["name"]] is None]
            reasons = {s["name"]: "requested: all" for s in selected}
        else:
            blocked = [f"{name} ({status[name]})" for name in requested if status[name] is not None]
            if blocked:
                raise PlanError(f"cannot ship {', '.join(blocked)}")
            selected = [by_name[name] for name in dict.fromkeys(requested)]
            reasons = {s["name"]: "requested by name" for s in selected}
    else:
        files = [f.strip() for f in (changed_files or []) if f.strip()]
        global_hit = next((f for f in files for g in catalog["global_paths"] if _matches(f, g)), None)
        selected, reasons = [], {}
        for service in services:
            if status[service["name"]] is not None:
                continue
            if global_hit:
                selected.append(service)
                reasons[service["name"]] = f"pipeline file changed: {global_hit}"
                continue
            hit = next((f for f in files if touches(service, f)), None)
            if hit:
                selected.append(service)
                reasons[service["name"]] = f"changed: {hit}"

    selected_names = {s["name"] for s in selected}
    for service in services:
        if service["name"] in selected_names:
            continue
        reason = status[service["name"]] or "no change for this service"
        skipped.append({"name": service["name"], "reason": reason})

    apps: dict[str, str] = {}
    for service in selected:
        other = apps.setdefault(service["app"], service["name"])
        if other != service["name"]:
            raise PlanError(f"{other} and {service['name']} would both deploy to app {service['app']}")

    include = [
        {
            "name": s["name"],
            "stack": s["stack"],
            "image": s["image"],
            "dockerfile": s["dockerfile"],
            "context": s["context"],
            "target": s.get("target", ""),
            "build_args": "\n".join(f"{k}={v}" for k, v in s.get("build_args", {}).items()),
            "app": s["app"],
            "app_name": app_name(s["app"], environment),
            "deploy_mode": s.get("deploy_mode", "image"),
            "reason": reasons[s["name"]],
        }
        for s in selected
    ]
    groups = {group: all(status[m] is None for m in members)
              for group, members in group_members(catalog).items()}
    return {"include": include, "skipped": skipped, "groups": groups}


def read_lines(path: str | None) -> list[str] | None:
    if path is None:
        return None
    return [line.strip() for line in Path(path).read_text(encoding="utf-8").splitlines() if line.strip()]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--catalog", default=".github/service-catalog.json")
    parser.add_argument("--repo-root", default=".")
    parser.add_argument("--environment", default="", help="dev, stg or prod; empty for build-only runs")
    parser.add_argument("--existing-apps", help="file listing container app names in the target resource group")
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument("--changed-files", help="file listing changed paths, one per line")
    selection.add_argument("--services", help="comma-separated service names, or 'all'")
    args = parser.parse_args(argv)

    if args.environment and args.environment not in ("dev", "stg", "prod"):
        print(f"plan_services: invalid environment {args.environment!r}", file=sys.stderr)
        return 2
    existing = read_lines(args.existing_apps)
    requested = [name.strip() for name in args.services.split(",") if name.strip()] if args.services else None
    if args.services is not None and not requested:
        print("plan_services: --services is empty", file=sys.stderr)
        return 2
    try:
        catalog = load_catalog(Path(args.catalog))
        result = plan(
            catalog,
            Path(args.repo_root),
            args.environment,
            set(existing) if existing is not None else None,
            read_lines(args.changed_files),
            requested,
        )
    except (PlanError, OSError, json.JSONDecodeError) as error:
        print(f"plan_services: {error}", file=sys.stderr)
        return 2
    json.dump(result, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
