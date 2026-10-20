import unittest
import base64
import json
from unittest.mock import patch
from matcher.contract import ContractError
from matcher.runtime import kafka_config, token_provider
from matcher.health import Health


class RuntimeTests(unittest.TestCase):
    def test_azure_configuration(self):
        config = kafka_config({"KAFKA_BOOTSTRAP_SERVERS": "example.servicebus.windows.net:9093",
                               "KAFKA_CONNECTION_STRING": "test-secret"})
        self.assertEqual(config["security.protocol"], "SASL_SSL")
        self.assertEqual(config["sasl.username"], "$ConnectionString")
        self.assertEqual(config["request.timeout.ms"], 60000)
        self.assertEqual(config["metadata.max.age.ms"], 180000)

    def test_missing_azure_credentials_fail_closed(self):
        for local in ("0", "1"):
            with self.assertRaises(ContractError):
                kafka_config({"KAFKA_BOOTSTRAP_SERVERS": "example.servicebus.windows.net:9093",
                              "MATCHER_LOCAL_TEST": local})

    def test_plaintext_requires_local_switch(self):
        with self.assertRaises(ContractError):
            kafka_config({"KAFKA_BOOTSTRAP_SERVERS": "kafka:9092"})
        self.assertEqual(kafka_config({"KAFKA_BOOTSTRAP_SERVERS": "kafka:9092", "MATCHER_LOCAL_TEST": "1"})["security.protocol"], "PLAINTEXT")

    def test_short_lived_tokens_refresh_without_operator_scope(self):
        env = {"MATCHER_SIGNING_SECRET": "x" * 32, "MATCHER_TOKEN_ISSUER": "test", "MATCHER_TOKEN_AUDIENCE": "api"}
        provider = token_provider(env)
        with patch("time.time", return_value=1000):
            first = provider()
        with patch("time.time", return_value=1300):
            second = provider()
        decode = lambda token: json.loads(base64.urlsafe_b64decode(token.split(".")[1] + "=="))
        self.assertEqual(decode(first)["exp"], 1300)
        self.assertEqual(decode(second)["exp"], 1600)
        self.assertNotIn("matching.rerun", decode(second)["scope"])

    def test_readiness_requires_polling_and_live_broker_statistics(self):
        health = Health()
        self.assertFalse(health.state(ready=True))
        health.polled()
        self.assertTrue(health.state())
        self.assertFalse(health.state(ready=True))
        health.stats(json.dumps({"brokers": {"one": {"state": "UP"}}, "cgrp": {"state": "up"}}))
        self.assertTrue(health.state(ready=True))
        health.blocked.add(("topic", 0))
        self.assertFalse(health.state(ready=True))
        health.revoked([("topic", 0)])
        self.assertTrue(health.state(ready=True))
        health.stats(json.dumps({"brokers": {"one": {"state": "DOWN"}}, "cgrp": {"state": "up"}}))
        self.assertFalse(health.state(ready=True))
