import copy
import os
import subprocess
import sys
import unittest

from matcher.contract import ContractError, canonical, loads, normalize
from matcher.core import evaluate
from helpers import fixture, frozen


class CoreTests(unittest.TestCase):
    def setUp(self):
        self.base = fixture()["input"]

    def candidate(self, mutation):
        data = copy.deepcopy(self.base)
        mutation(data, data["organisations"][0])
        return evaluate(frozen(data))["candidates"][0]

    def test_approved_goldens_and_no_mutation(self):
        for name in ("M-F01", "C2-EX01"):
            with self.subTest(fixture=name):
                gold = fixture(name)
                before = canonical(gold["input"])
                self.assertEqual(evaluate(gold["input"]), gold["expected_output"])
                self.assertEqual(evaluate(gold["input"]), evaluate(gold["input"]))
                self.assertEqual(before, canonical(gold["input"]))

    def test_capacity_boundary_and_no_reservation(self):
        for weight, matched in [("99.99", False), ("100.00", True), ("100.01", True)]:
            with self.subTest(weight=weight):
                row = self.candidate(lambda _, o: o["capacity_pools"][0].update(total_kg=weight))
                self.assertEqual(row["is_matched"], matched)
                self.assertEqual(row["reason_code"], "ELIGIBLE" if matched else "INSUFFICIENT_CAPACITY")

    def test_rules_and_failure_precedence(self):
        cases = [
            (lambda _, o: o["category_capabilities"][0].update(category="BATTERIES"), ["M1", "M2", "M3"]),
            (lambda _, o: o["category_capabilities"][0].update(supports_data_bearing=False), ["M2"]),
            (lambda _, o: o["category_capabilities"][0].update(accepted_conditions=["FUNCTIONAL"]), ["M2"]),
            (lambda _, o: o["service_zones"][0].update(zone="CENTRAL"), ["M4", "M5"]),
            (lambda _, o: o["service_zones"][0].update(minimum_lead_minutes=2881), ["M5"]),
            (lambda _, o: o.update(profile=None), ["M2"]),
            (lambda _, o: o["profile"].update(is_active=False), ["M2"]),
            (lambda _, o: o["category_capabilities"][0].update(is_active=False), ["M1", "M2"]),
            (lambda _, o: o["capacity_pools"][0].update(is_active=False), ["M3"]),
            (lambda _, o: o.update(capacity_pools=[]), ["M3"]),
            (lambda _, o: o["service_zones"][0].update(is_active=False), ["M4", "M5"]),
            (lambda _, o: o.update(category_capabilities=[], service_zones=[]), ["M1", "M2", "M3", "M4", "M5"]),
        ]
        for mutate, failures in cases:
            with self.subTest(failures=failures, mutation=cases.index((mutate, failures))):
                row = self.candidate(mutate)
                self.assertEqual([r["rule_id"] for r in row["failed_rules_json"]], failures)
                self.assertEqual(row["reason_code"], row["failed_rules_json"][0]["reason_code"])

    def test_non_data_bearing_and_missing_evidence(self):
        row = self.candidate(lambda b, o: (b["batch"].update(is_data_bearing=False),
                                          o["category_capabilities"][0].update(supports_data_bearing=False)))
        self.assertTrue(row["is_matched"])
        row = self.candidate(lambda _, o: o.update(profile=None, category_capabilities=[], capacity_pools=[], service_zones=[]))
        self.assertEqual(row["evidence_json"]["missing_configuration"],
                         ["PROFILE", "CATEGORY_CAPABILITY", "CAPACITY_POOL", "SERVICE_ZONE"])
        row = self.candidate(lambda _, o: (o["capacity_pools"][0].update(is_active=False), o["service_zones"][0].update(is_active=False)))
        self.assertIsNone(row["available_capacity_kg"])
        self.assertIsNone(row["minimum_lead_minutes"])
        self.assertIsNone(row["feasible_at"])

    def test_deadline_microseconds(self):
        for instant, expected in [("2026-09-17T23:59:59.999999Z", True),
                                  ("2026-09-18T00:00:00.000000Z", False),
                                  ("2026-09-18T00:00:00.000001Z", False)]:
            row = self.candidate(lambda b, o: (b.update(evaluation_at=instant), o["service_zones"][0].update(minimum_lead_minutes=0)))
            self.assertEqual(row["deadline_viable"], expected)

    def test_submission_window(self):
        for deadline, valid in [("2026-09-17T00:00:00.000000Z", True), ("2026-12-14T00:00:00.000000Z", True),
                                ("2026-09-16T23:59:59.999999Z", False), ("2026-12-14T00:00:00.000001Z", False)]:
            data = copy.deepcopy(self.base)
            data["batch"]["collection_deadline"] = deadline
            if valid:
                evaluate(frozen(data))
            else:
                with self.assertRaises(ContractError): evaluate(frozen(data))

    def test_no_match_empty_and_false_candidate(self):
        empty = copy.deepcopy(self.base)
        empty["organisations"] = []
        out = evaluate(frozen(empty))
        self.assertEqual((out["outcome"], out["primary_reason"], out["candidates"]), ("NO_MATCH", "NO_APPROVED_ORGANISATION", []))
        row = self.candidate(lambda _, o: o.update(category_capabilities=[]))
        self.assertFalse(row["is_matched"])

    def test_sorting_timezone_and_process_invariance(self):
        data = fixture("C2-EX01")["input"]
        data["organisations"][1]["capacity_pools"][0]["total_kg"] = "100.00"
        data = frozen(data)
        expected = canonical(evaluate(data))
        data["organisations"].reverse()
        self.assertEqual(expected, canonical(evaluate(data)))
        self.assertEqual(2, evaluate(data)["eligible_count"])
        for zone in ["UTC", "Pacific/Honolulu", "Asia/Singapore"]:
            env = {**os.environ, "TZ": zone}
            run = subprocess.run([sys.executable, "-m", "matcher", "evaluate"], input=canonical(data), capture_output=True, env=env, check=True)
            self.assertEqual(run.stdout.strip(), expected)

    def test_invalid_values_and_types(self):
        mutations = [("category", "MIXED"), ("zone", "UNKNOWN"), ("estimated_weight_kg", "100.001"),
                     ("estimated_weight_kg", 100.0), ("quantity", 1.0), ("quantity", True),
                     ("estimated_weight_kg", "0.09"), ("estimated_weight_kg", "50000.01"),
                     ("claim_epoch", "18446744073709551616"), ("claim_epoch", "01"), ("claim_epoch", "1\n"),
                     ("estimated_weight_kg", "100.00\n"),
                     ("collection_deadline", "2026-02-30T00:00:00.000000Z"),
                     ("collection_deadline", "2026-09-18T00:00:00Z")]
        for field, value in mutations:
            with self.subTest(field=field, value=value), self.assertRaises(ContractError):
                data = copy.deepcopy(self.base); data["batch"][field] = value
                evaluate(frozen(data))
        with self.assertRaises(ContractError): evaluate(fixture("M-F10-invalid-weight")["input"])
        for raw in [b'{"x":1,"x":2}', b'{"x":NaN}', b'{"x":Infinity}', b'\xff', b'{"x":"\\ud800"}']:
            with self.assertRaises(ContractError): loads(raw)

    def test_exact_integer_limits_and_integrity(self):
        data = copy.deepcopy(self.base)
        data["batch"]["claim_epoch"] = str(2**64 - 1)
        data["organisations"][0]["profile"]["version"] = str(2**63 - 1)
        self.assertEqual(evaluate(frozen(data))["claim_epoch"], str(2**64 - 1))
        data["organisations"][0]["profile"]["version"] = str(2**63)
        with self.assertRaises(ContractError): evaluate(frozen(data))
        for mutate in [lambda d: d["organisations"].append(copy.deepcopy(d["organisations"][0])),
                       lambda d: d["organisations"][0]["capacity_pools"][0].update(reserved_kg="101.00"),
                       lambda d: d["rule_set"].update(rule_set_version="binary-v2"),
                       lambda d: d.update(trigger_type="RECOVERY")]:
            data = copy.deepcopy(self.base); mutate(data)
            with self.assertRaises(ContractError): evaluate(frozen(data))
        data = fixture("C2-EX01")["input"]
        data["organisations"][0]["category_capabilities"][0]["capacity_pool_id"] = data["organisations"][1]["capacity_pools"][0]["id"]
        with self.assertRaises(ContractError): evaluate(frozen(data))

    def test_hash_tampering_and_normalization(self):
        data = copy.deepcopy(self.base); data["batch"]["quantity"] = 11
        with self.assertRaises(ContractError): evaluate(data)
        self.assertEqual(normalize(self.base), self.base)
