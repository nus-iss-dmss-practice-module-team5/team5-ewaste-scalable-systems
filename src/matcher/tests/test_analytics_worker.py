import os
import threading
import unittest
from unittest.mock import Mock, patch

from analytics_helpers import completion, preparation, source
from matcher.analytics import acknowledgement
from matcher.analytics_runtime import CombinedHealth, analytics_runner, run_workers
from matcher.analytics_worker import AnalyticsFacade, AnalyticsProcessor
from matcher.contract import ContractError, canonical
from matcher.facade import FacadeError
from matcher.health import Health
from matcher.worker import Delivery, Record


class AnalyticsWorkerTests(unittest.TestCase):
    def setUp(self):
        self.event = source()
        self.facade = Mock()
        self.facade.prepare.return_value = preparation(self.event)
        self.output = acknowledgement(preparation(self.event), self.event)
        self.facade.commit.return_value = completion(self.event, self.output)
        self.observe = Mock()
        self.processor = AnalyticsProcessor(self.facade, max_bytes=10000, retry_base=1, retry_max=8, observe=self.observe)

    def delivery(self):
        return Delivery(Record("ewaste.batch.events", 0, 42, self.event["batch_id"].encode(), canonical(self.event)),
                        "2026-10-07T00:00:00.000000Z")

    def advance(self, delivery, steps=4):
        for _ in range(steps):
            self.processor.step(delivery)

    def test_success_redelivery_and_restart_submit_identical_results(self):
        first, restart = self.delivery(), self.delivery()
        self.advance(first); self.advance(restart)
        self.assertEqual((first.stage, first.disposition), ("DONE", "COMMITTED"))
        self.assertEqual(first.output, restart.output)
        self.assertEqual(self.facade.commit.call_args_list[0], self.facade.commit.call_args_list[1])

    def test_response_loss_and_invalid_ack_retry_same_request(self):
        for failure in (FacadeError(), {"data": {}}):
            with self.subTest(failure=type(failure)):
                self.facade.commit.side_effect = [failure, completion(self.event, self.output)]
                delivery = self.delivery(); self.advance(delivery)
                self.assertEqual(delivery.stage, "COMMIT")
                self.assertEqual(delivery.retries, 1)
                self.processor.step(delivery)
                self.assertEqual(delivery.stage, "DONE")

    def test_retry_auth_rate_limit_rejection_and_recovery_are_observable(self):
        for status, code in ((401, "ANALYTICS_AUTH_FAILED"), (403, "ANALYTICS_AUTH_FAILED"),
                             (429, "ANALYTICS_FACADE_UNAVAILABLE"), (503, "ANALYTICS_FACADE_UNAVAILABLE"),
                             (409, "ANALYTICS_FACADE_REJECTED")):
            with self.subTest(status=status):
                self.facade.prepare.side_effect = [FacadeError(status, "untrusted-body-secret", retry_after=3), preparation(self.event)]
                delivery = self.delivery(); self.processor.step(delivery)
                self.assertEqual(self.processor.step(delivery), 3)
                self.assertEqual((delivery.stage, delivery.error_code), ("PREPARE", code))
                self.advance(delivery, 3)
                self.assertEqual(delivery.stage, "DONE")
                self.assertNotIn("untrusted-body-secret", str(self.observe.call_args_list))

    def test_bad_preparation_refetched_and_invalid_event_not_acknowledged(self):
        self.facade.prepare.side_effect = [{}, preparation(self.event)]
        delivery = self.delivery(); self.advance(delivery, 3)
        self.assertEqual(delivery.stage, "PREPARE")
        self.advance(delivery, 3); self.assertEqual(delivery.stage, "DONE")
        delivery = Delivery(Record("topic", 0, 43, b"x", b'{'), "2026-10-07T00:00:00.000000Z")
        self.assertEqual(self.processor.step(delivery), 1)
        self.assertEqual(self.processor.step(delivery), 2)
        self.assertEqual(delivery.stage, "VALIDATE_EVENT")
        ignored = Delivery(Record("topic", 0, 44, b"x", b'{"event_type":"RequestCompleted"}'), delivery.first_seen)
        self.processor.step(ignored)
        self.assertEqual((ignored.stage, ignored.disposition), ("DONE", "IGNORED"))

    def test_facade_routes_version_key_and_preserved_correlation(self):
        facade = AnalyticsFacade("https://api.example.test", None, 5, 10000, token_provider=lambda: "x" * 32)
        with patch.object(facade, "request") as request:
            facade.prepare(self.event)
            self.assertTrue(request.call_args.kwargs["preserve_correlation"])
            self.assertIn("source_event_id=" + self.event["event_id"], request.call_args.args[1])
            facade.commit(self.event, self.output)
            self.assertEqual(request.call_args.args[4], "analytics-v1:" + self.event["event_id"])
            self.assertEqual(request.call_args.kwargs["expected_version"], 8)
        response = Mock(status=200, headers={})
        response.read.return_value = b'{}'; response.__enter__ = Mock(return_value=response); response.__exit__ = Mock()
        with patch.object(facade.opener, "open", return_value=response) as send:
            self.event["correlation_id"] = "c" * 120 + "é"
            facade.commit(self.event, self.output)
            headers = dict(send.call_args.args[0].header_items())
            self.assertEqual(headers["If-match-version"], "8")
            self.assertEqual(headers["X-correlation-id"].encode("latin-1").decode("utf-8"), self.event["correlation_id"])
            self.event["correlation_id"] = "bad\nheader"
            with self.assertRaises(ContractError):
                facade.commit(self.event, self.output)

    def test_runtime_separate_group_and_aggregate_readiness(self):
        env = {"ANALYTICS_ENABLED": "true", "ANALYTICS_SERVICE_TOKEN": "t" * 32,
               "ANALYTICS_FACADE_URL": "http://api:8080"}
        with patch.dict(os.environ, env, clear=True), patch("matcher.analytics_runtime.KafkaRunner") as runner:
            worker, health = analytics_runner({}, "ewaste.batch.events", "matching-worker-v1", self.observe, lambda _: 10, True)
            self.assertEqual(runner.call_args.kwargs["group_id"], "analytics-processing-v1")
            self.assertEqual(runner.call_args.kwargs["offset_reset"], "earliest")
            runner.call_args.kwargs["observer"]("delivery_paused", self.delivery(), code="TEST")
            self.assertFalse(health.state(ready=True))
            self.assertEqual(runner.call_args.kwargs["processor"].facade.token_provider(), "t" * 32)
        for update in ({"ANALYTICS_ENABLED": "wrong"}, {"ANALYTICS_SERVICE_TOKEN": ""}, {"ANALYTICS_GROUP_ID": "matching-worker-v1"}):
            with patch.dict(os.environ, {**env, **update}, clear=True), self.assertRaises(ContractError):
                analytics_runner({}, "topic", "matching-worker-v1", self.observe, lambda _: 10, True)
        healthy, unhealthy = Mock(), Mock()
        healthy.state.return_value = True; unhealthy.state.return_value = False
        self.assertFalse(CombinedHealth([healthy, unhealthy], self.observe).state(ready=True))
        self.assertTrue(CombinedHealth([healthy], self.observe).state(ready=True))

    def test_runner_failure_stops_other_consumer(self):
        stop = threading.Event()
        first, second = Mock(), Mock()
        first.run.side_effect = RuntimeError("runner failed")
        second.run.side_effect = lambda event: event.wait(2)
        with self.assertRaisesRegex(RuntimeError, "runner failed"):
            run_workers([first, second], stop)
        self.assertTrue(stop.is_set())

    def test_runner_shutdown_and_unexpected_normal_exit(self):
        runner, stop = Mock(), threading.Event()
        with self.assertRaisesRegex(RuntimeError, "consumer stopped unexpectedly"):
            run_workers([runner], stop)
        self.assertTrue(stop.is_set())
        stop.clear()
        runner.run.side_effect = lambda event: event.set()
        run_workers([runner], stop)
