import copy
import unittest
from unittest.mock import patch

from matcher.contract import canonical, loads
from matcher.facade import FacadeError
from matcher.worker import Delivery, Processor, PublishError, Record
from helpers import committed, event, fixture, frozen


class FacadeDouble:
    """Protocol test double; this is deliberately not database persistence."""
    def __init__(self):
        self.context = fixture()["input"]
        self.saved = None
        self.calls = []
        self.fail_result_once = False
        self.stale_once = False

    def prepare(self, source):
        self.calls.append(("prepare", copy.deepcopy(source)))
        return self.response()

    def response(self):
        if self.saved:
            return {"phase": "COMPLETED", "run_id": self.context["run_id"], "result": copy.deepcopy(self.saved)}
        return {"phase": "PREPARED", "run_id": self.context["run_id"], "prepared_context": copy.deepcopy(self.context)}

    def run(self, run_id, source, action=None, body=None):
        self.calls.append((action or "get", copy.deepcopy(body)))
        if action == "result":
            if self.stale_once:
                self.stale_once = False
                raise FacadeError(409, "STALE_CONTEXT")
            self.saved = committed(self.context, body)
            if self.fail_result_once:
                self.fail_result_once = False
                raise FacadeError()
            return copy.deepcopy(self.saved)
        if action == "refresh":
            assert body == {"expected_input_hash": self.context["input_hash"]}
            self.context["context_generation"] += 1
            self.context["evaluation_at"] = "2026-09-16T00:00:01.000000Z"
            self.context = frozen(self.context)
        return self.response()


class PublisherDouble:
    def __init__(self):
        self.records = []
        self.fail = False

    def publish(self, key, value):
        if self.fail:
            raise PublishError()
        self.records.append((key, value))


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.facade, self.publisher, self.observations = FacadeDouble(), PublisherDouble(), []
        self.processor = Processor(self.facade, self.publisher, max_bytes=100_000, retry_base=1,
                                   retry_max=4, max_refreshes=3,
                                   observe=lambda *a, **k: self.observations.append((a, k)),
                                   clock=lambda: "2026-09-20T00:00:00.000000Z")

    def delivery(self, value=None, key=None):
        source = event()
        return Delivery(Record("ewaste.batch.events", 0, 5, key or source["batch_id"].encode(),
                               canonical(source) if value is None else value), "2026-09-19T00:00:00.000000Z")

    def finish(self, delivery):
        for _ in range(20):
            self.processor.step(delivery)
            if delivery.stage == "DONE": return
        self.fail("delivery did not finish")

    def test_success_replay_and_lost_response_resolves_before_evaluation(self):
        self.facade.fail_result_once = True
        with patch("matcher.worker.evaluate", wraps=__import__("matcher.core", fromlist=["evaluate"]).evaluate) as evaluator:
            first = self.delivery(); self.finish(first)
            self.assertEqual(first.disposition, "COMMITTED")
            second = self.delivery(); self.finish(second)
            self.assertEqual(second.disposition, "COMMITTED")
            self.assertEqual(evaluator.call_count, 1)
        self.assertEqual([c[0] for c in self.facade.calls], ["prepare", "result", "get", "prepare"])
        self.assertFalse(self.publisher.records)

    def test_uncertain_uncommitted_result_resends_identical_output(self):
        original = self.facade.run
        failed = []
        def run(*args):
            if len(args) > 2 and args[2] == "result" and not failed:
                failed.append(canonical(args[3])); raise FacadeError()
            return original(*args)
        self.facade.run = run
        delivery = self.delivery(); self.finish(delivery)
        self.assertEqual(failed[0], canonical([v for k, v in self.facade.calls if k == "result"][0]))
        self.assertEqual([k for k, _ in self.facade.calls], ["prepare", "get", "result"])

    def test_only_explicit_stale_allows_refresh(self):
        self.facade.stale_once = True
        delivery = self.delivery(); self.finish(delivery)
        self.assertEqual([k for k, _ in self.facade.calls], ["prepare", "result", "refresh", "result"])
        self.assertEqual(delivery.refreshes, 1)
        self.assertEqual(delivery.context["context_generation"], 2)

    def test_auth_and_persistence_outages_pause_without_quarantine(self):
        for status in (401, 403, 429, 503):
            delivery = self.delivery(); self.processor.step(delivery)
            with patch.object(self.facade, "prepare", side_effect=FacadeError(status, "UNAVAILABLE", retry_after=7)):
                self.assertGreaterEqual(self.processor.step(delivery), 7)
                self.assertEqual(delivery.stage, "PREPARE")
        self.assertFalse(self.publisher.records)

    def test_bare_state_conflict_never_acknowledges(self):
        delivery = self.delivery(); self.processor.step(delivery)
        with patch.object(self.facade, "prepare", side_effect=FacadeError(409, "STATE_CONFLICT")):
            for _ in range(5): self.processor.step(delivery)
        self.assertIsNone(delivery.disposition)
        self.assertFalse(self.publisher.records)

    def test_durable_skip(self):
        source = event()
        self.facade.saved = {"disposition": "SKIPPED", "code": "STATE_CONFLICT", "run_id": self.facade.context["run_id"],
                             "batch_id": source["batch_id"], "correlation_id": source["correlation_id"], "replay": True}
        delivery = self.delivery(); self.finish(delivery)
        self.assertEqual(delivery.disposition, "SKIPPED")
        self.assertEqual([k for k, _ in self.facade.calls], ["prepare"])

    def test_dlq_failure_has_no_disposition_and_preserves_payload(self):
        delivery = self.delivery(b'{"private_note":"must never leak",')
        self.processor.step(delivery)
        self.publisher.fail = True
        self.assertGreater(self.processor.step(delivery), 0)
        self.assertEqual(delivery.stage, "QUARANTINE")
        raw = delivery.quarantine_record[1]
        self.publisher.fail = False; self.finish(delivery)
        self.assertEqual(self.publisher.records[0][1], raw)
        self.assertNotIn(b"private_note", raw)
        self.assertIsNone(loads(raw)["correlation_id"])

    def test_unknown_optional_fields_pass_to_facade_but_not_dlq(self):
        source = event(); source["private_note"] = "DO_NOT_PUBLISH"; source["data"]["extra"] = "DO_NOT_PUBLISH"
        delivery = self.delivery(canonical(source))
        with patch.object(self.facade, "prepare", side_effect=FacadeError(409, "IDEMPOTENCY_CONFLICT")):
            self.finish(delivery)
        self.assertNotIn(b"DO_NOT_PUBLISH", self.publisher.records[0][1])
        self.assertEqual(loads(self.publisher.records[0][1])["original_event"], event())

    def test_optional_event_fields_do_not_override_core_dto(self):
        source = event(); source["data"]["status"] = "UNKNOWN_OPTIONAL_VALUE"
        delivery = self.delivery(canonical(source)); self.finish(delivery)
        self.assertEqual(delivery.disposition, "COMMITTED")
        self.assertEqual(self.facade.calls[0][1], source)
        self.assertEqual(delivery.context["batch"]["status"], "SUBMITTED")

    def test_unsupported_version_wrong_key_and_unrelated_event(self):
        source = event(); source["schema_version"] = 2
        delivery = self.delivery(canonical(source)); self.finish(delivery)
        self.assertEqual(loads(self.publisher.records[-1][1])["error_code"], "UNSUPPORTED_SCHEMA_VERSION")
        self.assertEqual(loads(self.publisher.records[-1][1])["source_event_id"], source["event_id"])
        self.assertEqual(loads(self.publisher.records[-1][1])["correlation_id"], source["correlation_id"])
        delivery = self.delivery(key=b'"quoted-key"'); self.finish(delivery)
        self.assertEqual(delivery.disposition, "QUARANTINED")
        delivery = self.delivery(b'{"event_type":"Unrelated.v99"}'); self.finish(delivery)
        self.assertEqual(delivery.disposition, "IGNORED_EVENT")
        self.assertFalse(self.facade.calls)

    def test_invalid_result_resolution_before_quarantine(self):
        original = self.facade.run
        def run(*args):
            if len(args) > 2 and args[2] == "result": raise FacadeError(422, "INVALID_RESULT")
            return original(*args)
        self.facade.run = run
        delivery = self.delivery(); self.finish(delivery)
        self.assertEqual(delivery.disposition, "QUARANTINED")
        self.assertIn("get", [k for k, _ in self.facade.calls])

    def test_mismatched_facade_identity_never_acknowledges(self):
        self.facade.context["correlation_id"] = "wrong-trace"
        delivery = self.delivery()
        for _ in range(5): self.processor.step(delivery)
        self.assertIsNone(delivery.disposition)
        self.assertFalse(self.publisher.records)
