import copy
import hashlib
import unittest

from analytics_helpers import completion, fixtures, preparation, source
from matcher.analytics import acknowledgement, check_completion, evaluate, source_event
from matcher.contract import ContractError, canonical


class AnalyticsRulesTests(unittest.TestCase):
    def test_all_26_approved_golden_cases_and_repeatability(self):
        cases = fixtures()
        self.assertEqual(len(cases), 26)
        for case in cases:
            with self.subTest(case=case["id"]):
                original = copy.deepcopy(case["input"])
                if "error" in case:
                    with self.assertRaises(ContractError) as error:
                        evaluate(original)
                    self.assertEqual(error.exception.code, case["error"])
                else:
                    self.assertEqual(evaluate(original), case["expected"])
                    self.assertEqual(canonical(evaluate(dict(reversed(list(original.items()))))), canonical(case["expected"]))
                self.assertEqual(original, case["input"])

    def test_boundaries_and_invalid_inputs(self):
        base = fixtures()[0]["input"]
        for changes, code in [({"rule_version": "future"}, "UNSUPPORTED_RULE_VERSION"),
                              ({"actual_item_count": True}, "INVALID_ITEM_COUNT"),
                              ({"actual_item_count": 100001}, "INVALID_ITEM_COUNT"),
                              ({"actual_weight_kg": "0.09"}, "INVALID_WEIGHT_RANGE"),
                              ({"actual_weight_kg": "50000.01"}, "INVALID_WEIGHT_RANGE"),
                              ({"actual_weight_kg": "NaN"}, "INVALID_WEIGHT_FORMAT")]:
            with self.subTest(changes=changes), self.assertRaisesRegex(ContractError, code):
                evaluate({**base, **changes})
        for weight in ("0.10", "50000.00"):
            result = evaluate({**base, "actual_weight_kg": weight, "actual_item_count": 100000,
                               "reused_kg": None, "recycled_kg": None, "disposed_kg": None})
            self.assertEqual(result["metrics"]["unknown_kg"], weight)
            self.assertEqual(result["data_quality"], "MISSING")

    def test_envelope_key_identity_version_and_type_checks(self):
        event = source()
        self.assertEqual(source_event(canonical(event), event["batch_id"].encode(), 10000), event)
        self.assertIsNone(source_event(b'{"event_type":"ReceiptVerified"}', b'x', 10000))
        bad = [(canonical(event), b"wrong", 10000), (canonical(event), b"x", 1), (b'{', b'x', 10000),
               (b'[]', b'x', 10000)]
        for change in ({"schema_version": 2}, {"aggregate_id": "another"}, {"aggregate_version": 8.0},
                       {"claim_epoch": "2"}, {"aggregate_version": 2**32}, {"occurred_at": "not-a-time"}):
            bad.append((canonical({**event, **change}), event["batch_id"].encode(), 10000))
        for args in bad:
            with self.subTest(args=args), self.assertRaises(ContractError):
                source_event(*args)

    def test_preparation_exact_bytes_hash_and_frozen_identity(self):
        event = source()
        ready = preparation(event)
        output = acknowledgement(ready, event)
        self.assertEqual(output, acknowledgement(copy.deepcopy(ready), copy.deepcopy(event)))
        self.assertEqual(output["anomaly_codes"], [])
        self.assertEqual(output["metrics"], fixtures()[0]["expected"]["metrics"])
        # Go may escape characters differently from Python; hash the supplied bytes.
        event["data"]["actor_user_id"] = "user<a>"
        ready = preparation(event)
        ready["input_canonical_json"] = ready["input_canonical_json"].replace("<", "\\u003c")
        ready["input_hash"] = hashlib.sha256(ready["input_canonical_json"].encode()).hexdigest()
        acknowledgement(ready, event)
        for key, value in (("source_event_id", "other"), ("input_hash", "f" * 64), ("input_canonical_json", None)):
            with self.subTest(key=key), self.assertRaises(ContractError):
                acknowledgement({**ready, key: value}, event)
        other = copy.deepcopy(event); other["data"]["actual_item_count"] = 3
        with self.assertRaisesRegex(ContractError, "ANALYTICS_FROZEN_INPUT_MISMATCH"):
            acknowledgement(ready, other)
        other = copy.deepcopy(event); other["data"]["diverted_kg"] = "2.00"
        with self.assertRaisesRegex(ContractError, "ANALYTICS_FROZEN_TOTAL_MISMATCH"):
            acknowledgement(preparation(other), other)

    def test_completion_must_confirm_same_result_before_offset_can_advance(self):
        event = source(); output = acknowledgement(preparation(event), event)
        response = completion(event, output)
        check_completion(response, event, output)
        for mutate in (lambda r: r.update(correlation_id="wrong"), lambda r: r.update(event_id="invalid"),
                       lambda r: r["data"].update(version=8), lambda r: r["data"].update(anomaly_codes=["MISSING_OUTCOME"])):
            bad = copy.deepcopy(response); mutate(bad)
            with self.assertRaisesRegex(ContractError, "INVALID_ANALYTICS_ACK"):
                check_completion(bad, event, output)
