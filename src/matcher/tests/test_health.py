import http.client
import json
import unittest
from unittest.mock import patch

from matcher.health import Health
from matcher.worker import Delivery, Record


class HealthTests(unittest.TestCase):
    def setUp(self):
        self.health = Health()
        self.clock = patch("matcher.health.time.monotonic", return_value=1000)
        self.now = self.clock.start()
        self.addCleanup(self.clock.stop)

    def make_ready(self):
        self.health.polled()
        self.health.stats(json.dumps({"brokers": {"one": {"state": "UP"}}, "cgrp": {"state": "up"}}))

    def test_stale_broker_statistics_and_polling_expire_independently(self):
        self.make_ready()
        self.now.return_value = 1015
        self.assertTrue(self.health.state())
        self.assertFalse(self.health.state(ready=True))
        self.now.return_value = 1120
        self.assertFalse(self.health.state())
        self.health.polled()
        self.assertTrue(self.health.state())
        self.assertFalse(self.health.state(ready=True))

    def test_partition_failures_block_readiness_until_committed_or_revoked(self):
        self.make_ready()
        first = Delivery(Record("topic", 0, 1, b"key", b"private-payload"), "2026-09-19T00:00:00.000000Z")
        second = Delivery(Record("topic", 1, 2, b"key", b"private-payload"), first.first_seen)
        self.health.observe("consumer_error")
        self.assertTrue(self.health.state(ready=True))
        self.health.observe("delivery_paused", first)
        self.health.observe("offset_commit_failed", second)
        self.assertTrue(self.health.state())
        self.assertFalse(self.health.state(ready=True))
        self.health.observe("offset_committed", first)
        self.assertFalse(self.health.state(ready=True))
        self.health.revoked([("topic", 1)])
        self.assertTrue(self.health.state(ready=True))

    def test_http_probes_return_aggregate_status_and_unknown_routes_are_404(self):
        server = self.health.serve(0)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=2)
        self.addCleanup(connection.close)

        def get(path, status, body=None):
            connection.request("GET", path)
            response = connection.getresponse()
            raw = response.read()
            self.assertEqual(response.status, status)
            if body is not None:
                self.assertEqual(response.getheader("Content-Type"), "application/json")
                self.assertEqual(int(response.getheader("Content-Length")), len(raw))
                self.assertEqual(json.loads(raw), {"status": body})

        get("/healthz", 503, "unavailable")
        get("/readyz", 503, "unavailable")
        self.health.polled()
        get("/healthz", 200, "ok")
        get("/readyz", 503, "unavailable")
        self.make_ready()
        get("/readyz", 200, "ok")
        get("/unknown", 404)
