"""Unit tests for processing telemetry, structured fields, failure, retry, and readiness signals."""
import json
import unittest
from unittest.mock import MagicMock, patch

from matcher.health import Health
from matcher.kafka import observe
from matcher.worker import Delivery, Record


class TelemetryTests(unittest.TestCase):
    def setUp(self):
        self.log_patch = patch("matcher.kafka.LOG.info")
        self.mock_log = self.log_patch.start()
        self.addCleanup(self.log_patch.stop)

    def test_observe_emits_structured_fields_and_latency(self):
        record = Record("matching.requests", 0, 42, b"key", b"value")
        delivery = Delivery(record, "2026-10-05T08:00:00.000000Z")
        delivery.event = {
            "event_id": "e0000000-0000-4000-8000-000000000001",
            "batch_id": "b0000000-0000-4000-8000-000000000001",
            "correlation_id": "trace-s3-telemetry-001",
        }
        delivery.run_id = "r0000000-0000-4000-8000-000000000001"
        delivery.stage = "EVALUATE"
        delivery.retries = 2
        delivery.failure_streak = 1
        delivery.error_code = "TRANSIENT_TIMEOUT"

        observe("delivery_paused", delivery, code="TRANSIENT_TIMEOUT",
                custom_flag="retry_check", password="not-logged")

        self.mock_log.assert_called_once()
        raw_payload = self.mock_log.call_args[0][0]
        data = json.loads(raw_payload)

        # Standard structured fields
        self.assertEqual(data["component"], "matcher-worker")
        self.assertEqual(data["observation"], "delivery_paused")
        self.assertEqual(data["topic"], "matching.requests")
        self.assertEqual(data["partition"], 0)
        self.assertEqual(data["offset"], "42")
        self.assertEqual(data["stage"], "EVALUATE")
        self.assertEqual(data["retry_count"], 2)
        self.assertEqual(data["failure_streak"], 1)
        self.assertEqual(data["error_code"], "TRANSIENT_TIMEOUT")
        self.assertEqual(data["correlation_id"], "trace-s3-telemetry-001")
        self.assertEqual(data["batch_id"], "b0000000-0000-4000-8000-000000000001")
        self.assertEqual(data["event_id"], "e0000000-0000-4000-8000-000000000001")
        self.assertEqual(data["run_id"], "r0000000-0000-4000-8000-000000000001")
        self.assertEqual(data["code"], "TRANSIENT_TIMEOUT")
        self.assertEqual(data["telemetry_version"], 1)
        self.assertEqual(data["outcome"], "RETRYING")
        self.assertNotIn("custom_flag", data)
        self.assertIn("latency_ms", data)
        self.assertGreaterEqual(data["latency_ms"], 0)

        # Security check: Ensure no raw body, secrets, or tokens leaked
        self.assertNotIn("value", data)
        self.assertNotIn("password", data)
        self.assertNotIn("secret", data)

    def test_health_readiness_signals_on_failures_and_retries(self):
        health = Health()
        health.polled()
        health.stats(json.dumps({"brokers": {"b1": {"state": "UP"}}, "cgrp": {"state": "up"}}))

        # Baseline: Healthy and ready
        self.assertTrue(health.state(ready=True))

        record = Record("matching.requests", 0, 100, b"key", b"value")
        delivery = Delivery(record, "2026-10-05T08:00:00.000000Z")

        # Failure / retry pauses partition -> readiness probe must drop to False
        health.observe("delivery_paused", delivery)
        self.assertFalse(health.state(ready=True), "Readiness must drop when partition delivery is paused")
        self.assertTrue(health.state(ready=False), "Liveness must stay true during pause (no restart needed)")

        # Recovery on successful offset commit
        health.observe("offset_committed", delivery)
        self.assertTrue(health.state(ready=True), "Readiness must recover when offset commit succeeds")


if __name__ == "__main__":
    unittest.main()
