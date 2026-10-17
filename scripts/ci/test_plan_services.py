"""Tests for plan_services.py. Run: python3 -m unittest discover -s scripts/ci"""

import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import plan_services as ps

REAL_CATALOG = Path(__file__).resolve().parents[2] / ".github" / "service-catalog.json"
GO_SERVICES = ["identity-access", "batch-workflow", "claim-logistics", "custody-verification"]
TODAY_APPS = {f"aca-ewaste-dev-{app}" for app in ("api", "ui", "analytics")}
ALL_APPS = {f"aca-ewaste-dev-{app}" for app in
            ("auth", "api", "claim", "custody", "matching-worker", "analytics", "ui")}


def write(root: Path, relative: str, text: str = "") -> None:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def make_repo(root: Path, worker_targets: bool) -> None:
    """The Go entrypoints are on dev already; the worker build targets arrive with EWSS-23."""
    write(root, "src/backend/Dockerfile", "FROM golang:1.26-alpine AS builder\nFROM alpine:3.20\n")
    write(root, "src/backend/cmd/server/main.go")
    for name in GO_SERVICES:
        write(root, f"src/backend/cmd/{name}/main.go")
    write(root, "src/frontend/Dockerfile", "FROM node:22 AS build\n")
    if worker_targets:
        write(root, "src/matcher/Dockerfile",
              "FROM python:3.12-slim AS runtime\nFROM runtime AS matching-worker\n"
              "FROM runtime AS analytics-worker\nFROM runtime AS legacy\n")
    else:
        write(root, "src/matcher/Dockerfile", "FROM python:3.12-slim\n")


class PlanTestCase(unittest.TestCase):
    worker_targets = True

    def setUp(self):
        self.catalog = ps.load_catalog(REAL_CATALOG)
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        make_repo(self.root, self.worker_targets)

    def tearDown(self):
        self._tmp.cleanup()

    def run_plan(self, changed=None, services=None, environment="", apps=None):
        result = ps.plan(self.catalog, self.root, environment, apps, changed, services)
        skipped = {s["name"]: s["reason"] for s in result["skipped"]}
        return [entry["name"] for entry in result["include"]], skipped, result


class CatalogTests(unittest.TestCase):
    def test_real_catalog_is_valid(self):
        catalog = ps.load_catalog(REAL_CATALOG)
        names = [service["name"] for service in catalog["services"]]
        self.assertEqual(len(names), len(set(names)))

    def test_rejects_unknown_reference(self):
        bad = {"services": [
            {"name": "aa", "stack": "go", "image": "i", "dockerfile": "d", "context": ".",
             "app": "xx", "paths": ["a/"], "replaced_by": "zz"}]}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "bad.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            with self.assertRaisesRegex(ps.PlanError, "unknown service zz"):
                ps.load_catalog(path)


def push_path_filters(workflow: Path) -> list[str]:
    """Read on.push.paths without PyYAML, which the runner may not have."""
    filters, in_push, in_paths = [], False, False
    for line in workflow.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(line) - len(line.lstrip())
        if indent == 2:
            in_push, in_paths = stripped == "push:", False
        elif in_push and indent == 4:
            in_paths = stripped == "paths:"
        elif in_paths and stripped.startswith("- "):
            filters.append(stripped[2:].strip("'\""))
    return filters


def covered(pattern: str, filters: list[str]) -> bool:
    """A catalogue folder ("a/") or file ("a/b") is covered by "x/**" above it or an exact match."""
    for glob in filters:
        if glob.endswith("/**") and pattern.startswith(glob[:-2]):
            return True
        if glob == pattern:
            return True
    return False


class TriggerTests(unittest.TestCase):
    def test_push_trigger_covers_every_planner_input(self):
        """A planner input outside the push filter never reaches the planner on push."""
        catalog = ps.load_catalog(REAL_CATALOG)
        filters = push_path_filters(REAL_CATALOG.parent / "workflows" / "cd-pipeline.yml")
        self.assertIn("src/**", filters, "push path filter was not parsed")
        inputs = set(catalog["global_paths"])
        for service in catalog["services"]:
            inputs.update(service["paths"])
        missing = sorted(p for p in inputs if not covered(p, filters))
        self.assertEqual(missing, [], f"add these to on.push.paths in cd-pipeline.yml: {missing}")

    def test_coverage_check_spots_a_missing_input(self):
        self.assertTrue(covered("src/backend/", ["src/**"]))
        self.assertTrue(covered("scripts/deploy-matcher.sh", ["scripts/deploy-matcher.sh"]))
        self.assertFalse(covered("scripts/ci/", ["src/**", "scripts/deploy-matcher.sh"]))
        self.assertFalse(covered("contracts/matching/", ["contracts/match/**"]))


class TodayOnDevTests(PlanTestCase):
    """Go entrypoints exist, but only the api, ui and analytics apps exist in Azure."""

    worker_targets = False

    def test_backend_change_keeps_shipping_the_combined_api(self):
        names, skipped, result = self.run_plan(
            changed=["src/backend/internal/service/claim.go"], environment="dev", apps=TODAY_APPS)
        self.assertEqual(names, ["workflow-api"])
        self.assertIn("does not exist in dev", skipped["claim-logistics"])
        # batch-workflow's own app exists, but it must not replace the combined
        # API while the other three services have nowhere to run.
        self.assertIn("group go-services cannot ship together", skipped["batch-workflow"])
        self.assertEqual(result["groups"], {"go-services": False})

    def test_matcher_change_ships_combined_worker(self):
        names, _, _ = self.run_plan(changed=["src/matcher/matcher/runtime.py"],
                                    environment="dev", apps=TODAY_APPS)
        self.assertEqual(names, ["workflow-analytics"])

    def test_frontend_change_ships_ui_alone(self):
        names, _, _ = self.run_plan(changed=["src/frontend/src/app/page.tsx"],
                                    environment="dev", apps=TODAY_APPS)
        self.assertEqual(names, ["ui"])

    def test_docs_and_terraform_changes_ship_nothing(self):
        names, _, _ = self.run_plan(changed=["docs/readme.md", "terraform/environments/main.tf"],
                                    environment="dev", apps=TODAY_APPS)
        self.assertEqual(names, [])

    def test_requesting_a_service_without_an_app_fails(self):
        with self.assertRaisesRegex(ps.PlanError, "cannot ship claim-logistics"):
            self.run_plan(services=["claim-logistics"], environment="dev", apps=TODAY_APPS)

    def test_partial_apps_still_ship_the_combined_api(self):
        apps = TODAY_APPS | {"aca-ewaste-dev-auth"}
        names, skipped, _ = self.run_plan(services=["all"], environment="dev", apps=apps)
        self.assertEqual(names, ["workflow-api", "workflow-analytics", "ui"])
        self.assertIn("cannot ship together", skipped["identity-access"])


class AfterSplitTests(PlanTestCase):
    """All seven apps exist and the worker images have their own build targets."""

    def test_entrypoint_change_ships_one_service(self):
        names, _, _ = self.run_plan(changed=["src/backend/cmd/claim-logistics/main.go"],
                                    environment="dev", apps=ALL_APPS)
        self.assertEqual(names, ["claim-logistics"])

    def test_shared_backend_change_ships_all_go_services_not_legacy(self):
        names, skipped, result = self.run_plan(
            changed=["src/backend/internal/repository/claim.go"], environment="dev", apps=ALL_APPS)
        self.assertEqual(names, GO_SERVICES)
        self.assertEqual(skipped["workflow-api"], "replaced by batch-workflow")
        self.assertEqual(result["groups"], {"go-services": True})

    def test_pipeline_change_ships_every_available_service(self):
        names, _, _ = self.run_plan(changed=[".github/workflows/service-pipeline.yml"],
                                    environment="dev", apps=ALL_APPS)
        self.assertEqual(names, GO_SERVICES + ["matching-worker", "analytics-worker", "ui"])

    def test_single_service_by_name(self):
        names, _, result = self.run_plan(services=["ui"], environment="dev", apps=ALL_APPS)
        self.assertEqual(names, ["ui"])
        self.assertEqual(result["include"][0]["app_name"], "aca-ewaste-dev-ui")

    def test_missing_matching_app_keeps_the_combined_worker(self):
        apps = ALL_APPS - {"aca-ewaste-dev-matching-worker"}
        names, skipped, _ = self.run_plan(changed=["src/matcher/matcher/runtime.py"],
                                          environment="dev", apps=apps)
        # The analytics-only image must not replace the combined worker while
        # the matching worker has nowhere to run.
        self.assertEqual(names, ["workflow-analytics"])
        self.assertIn("needs matching-worker", skipped["analytics-worker"])

    def test_build_only_run_without_environment(self):
        names, _, _ = self.run_plan(changed=["src/backend/internal/x.go"])
        self.assertEqual(names, GO_SERVICES)

    def test_build_args_and_target_are_passed_through(self):
        _, _, result = self.run_plan(services=["batch-workflow", "analytics-worker"])
        entries = {e["name"]: e for e in result["include"]}
        self.assertEqual(entries["batch-workflow"]["build_args"], "SERVICE=batch-workflow")
        self.assertEqual(entries["analytics-worker"]["target"], "analytics-worker")
        self.assertEqual(entries["analytics-worker"]["deploy_mode"], "matcher-script")


class PathRuleTests(unittest.TestCase):
    service = {"paths": ["src/backend/", "src/backend/cmd/a/"], "exclude_paths": ["src/backend/cmd/"]}

    def test_most_specific_rule_wins(self):
        self.assertTrue(ps.touches(self.service, "src/backend/internal/x.go"))
        self.assertTrue(ps.touches(self.service, "src/backend/cmd/a/main.go"))
        self.assertFalse(ps.touches(self.service, "src/backend/cmd/b/main.go"))
        self.assertFalse(ps.touches(self.service, "src/backendish/file.go"))


class CliTests(unittest.TestCase):
    def test_unknown_service_exits_2(self):
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            code = ps.main(["--catalog", str(REAL_CATALOG), "--services", "nope"])
        self.assertEqual(code, 2)
        self.assertIn("unknown service", err.getvalue())

    def test_invalid_environment_exits_2(self):
        with redirect_stderr(io.StringIO()):
            self.assertEqual(ps.main(["--environment", "qa", "--services", "all"]), 2)


if __name__ == "__main__":
    unittest.main()
