"""The two deployment commands must not depend on the other worker's settings."""
import os
import signal
from unittest.mock import patch

import pytest

from matcher.__main__ import main
from matcher.contract import ContractError
from matcher.runtime import kafka_config
import test_main


def matching_environment():
    return test_main.MainTests().environment()


def analytics_environment():
    values = matching_environment()
    env = {key.replace("MATCHER_", "ANALYTICS_", 1): value
           for key, value in values.items() if key.startswith("MATCHER_")}
    for suffix in ("TOKEN_FILE", "DLQ_TOPIC", "DELIVERY_TIMEOUT_SECONDS", "MAX_REFRESHES"):
        env.pop("ANALYTICS_" + suffix)
    env.update(ANALYTICS_GROUP_ID="analytics-processing-v1", ANALYTICS_SERVICE_TOKEN="a" * 32,
               ANALYTICS_KAFKA_BOOTSTRAP_SERVERS="analytics-kafka:9092",
               ANALYTICS_HEALTH_PORT="8001", ANALYTICS_WORKERS="3", ANALYTICS_ENABLED="false")
    return env


def test_matching_command_never_starts_analytics_even_if_legacy_flag_is_enabled():
    env = {**matching_environment(), "ANALYTICS_ENABLED": "true", "ANALYTICS_SERVICE_TOKEN": "invalid"}
    with patch.dict(os.environ, env, clear=True), patch("sys.argv", ["matcher", "run-matching"]), \
            patch("matcher.facade.FacadeClient"), patch("matcher.kafka.QuarantinePublisher"), \
            patch("matcher.kafka.KafkaRunner") as runner, patch("signal.signal"), \
            patch("matcher.analytics_runtime.analytics_runner") as analytics:
        assert main() == 0
        runner.return_value.run.assert_called_once()
        assert runner.call_args.kwargs["group_id"] == "matching-worker-v1"
        analytics.assert_not_called()


@pytest.mark.parametrize("azure", [False, True])
def test_analytics_command_needs_no_matching_settings_or_credentials(azure):
    env = analytics_environment()
    if azure:
        env.update(ANALYTICS_LOCAL_TEST="0", ANALYTICS_FACADE_URL="https://workflow.example.test",
                   ANALYTICS_KAFKA_BOOTSTRAP_SERVERS="test.servicebus.windows.net:9093",
                   ANALYTICS_KAFKA_CONNECTION_STRING="test-connection")
    with patch.dict(os.environ, env, clear=True), patch("sys.argv", ["matcher", "run-analytics"]), \
            patch("matcher.analytics_runtime.KafkaRunner") as runner, \
            patch("matcher.analytics_runtime.Health") as health, patch("signal.signal") as signals, \
            patch("matcher.__main__.matching_runner") as matching:
        def shutdown(stop):
            assert not stop.is_set()
            assert [c.args[0] for c in signals.call_args_list] == [signal.SIGINT, signal.SIGTERM]
            signals.call_args_list[0].args[1]()
            assert stop.is_set()
        runner.return_value.run.side_effect = shutdown
        assert main() == 0
        matching.assert_not_called()
        settings = runner.call_args.kwargs
        assert (settings["group_id"], settings["workers"], settings["topic"]) == (
            "analytics-processing-v1", 3, "ewaste.batch.events")
        assert runner.call_args.args[0]["security.protocol"] == ("SASL_SSL" if azure else "PLAINTEXT")
        assert settings["processor"].facade.token_provider() == "a" * 32
        health.return_value.serve.assert_called_once_with(8001)
        health.return_value.serve.return_value.shutdown.assert_called_once()
        health.return_value.serve.return_value.server_close.assert_called_once()


@pytest.mark.parametrize("command,env,update", [
    ("run-matching", matching_environment(), {"MATCHER_GROUP_ID": "analytics-processing-v1"}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_GROUP_ID": "matching-worker-v1"}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_GROUP_ID": "shared", "KAFKA_CONSUMER_GROUP": "shared"}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_GROUP_ID": ""}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_SERVICE_TOKEN": ""}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_WORKERS": "0"}),
    ("run-analytics", analytics_environment(), {"ANALYTICS_RETRY_BASE_SECONDS": "31"}),
])
def test_invalid_service_settings_fail_before_consuming(command, env, update):
    with patch.dict(os.environ, {**env, **update}, clear=True), patch("sys.argv", ["matcher", command]), \
            patch("matcher.kafka.KafkaRunner") as matching, \
            patch("matcher.analytics_runtime.KafkaRunner") as analytics, pytest.raises(ContractError):
        try:
            main()
        finally:
            matching.assert_not_called()
            analytics.assert_not_called()


def test_analytics_cannot_inherit_matching_transport_or_local_security_switch():
    env = {**matching_environment(), "KAFKA_BOOTSTRAP_SERVERS": "kafka:9092"}
    with pytest.raises(ContractError):
        kafka_config(env, prefix="ANALYTICS")
    config = kafka_config({**env, **analytics_environment()}, prefix="ANALYTICS")
    assert config["bootstrap.servers"] == "analytics-kafka:9092"


def test_analytics_failure_closes_only_its_probe():
    with patch.dict(os.environ, analytics_environment(), clear=True), \
            patch("sys.argv", ["matcher", "run-analytics"]), patch("signal.signal"), \
            patch("matcher.analytics_runtime.KafkaRunner") as runner, \
            patch("matcher.analytics_runtime.Health") as health:
        runner.return_value.run.side_effect = RuntimeError("consumer stopped")
        with pytest.raises(RuntimeError):
            main()
        health.return_value.serve.return_value.shutdown.assert_called_once()
        health.return_value.serve.return_value.server_close.assert_called_once()
