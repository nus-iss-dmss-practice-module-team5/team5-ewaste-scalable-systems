import logging
import os
import signal
import sys
import threading

from .contract import ContractError, canonical, loads, require
from .core import evaluate


def required(name):
    value = os.environ.get(name)
    require(bool(value))
    return value


def positive(name):
    value = int(required(name))
    require(value > 0)
    return value


def matching_runner():
    from .facade import FacadeClient
    from .kafka import KafkaRunner, QuarantinePublisher, observe
    from .worker import Processor
    from .health import Health

    from .runtime import kafka_config, token_file, token_provider
    common = kafka_config()
    local = os.environ.get("MATCHER_LOCAL_TEST") == "1"
    require(common.get("security.protocol") in (("PLAINTEXT", "SSL", "SASL_SSL") if local else ("SSL", "SASL_SSL")))
    require(bool(common.get("bootstrap.servers")))
    topic = os.environ.get("MATCHER_TOPIC") or required("KAFKA_TOPIC_BATCH_EVENTS")
    dlq = os.environ.get("MATCHER_DLQ_TOPIC") or required("KAFKA_TOPIC_DLQ")
    require(topic != dlq)
    provider = token_provider()
    facade = FacadeClient(required("MATCHER_FACADE_URL"), None if provider else token_file(),
                          positive("MATCHER_HTTP_TIMEOUT_SECONDS"), positive("MATCHER_MAX_RESPONSE_BYTES"),
                          local=local, token_provider=provider)
    publisher = QuarantinePublisher(common, dlq, positive("MATCHER_DELIVERY_TIMEOUT_SECONDS"))
    health = Health(observer=observe)
    def observer(name, delivery=None, **fields):
        fields.setdefault("consumer_group", os.environ.get("MATCHER_GROUP_ID") or os.environ.get("KAFKA_CONSUMER_GROUP"))
        observe(name, delivery, **fields)
        health.observe(name, delivery, **fields)
    processor = Processor(facade, publisher, max_bytes=positive("MATCHER_MAX_RECORD_BYTES"),
                          retry_base=positive("MATCHER_RETRY_BASE_SECONDS"), retry_max=positive("MATCHER_RETRY_MAX_SECONDS"),
                          max_refreshes=positive("MATCHER_MAX_REFRESHES"), observe=observer)
    require(processor.retry_base <= processor.retry_max)
    runner = KafkaRunner(common, topic=topic, group_id=os.environ.get("MATCHER_GROUP_ID") or required("KAFKA_CONSUMER_GROUP"),
                         offset_reset=required("MATCHER_OFFSET_RESET"), max_poll_ms=positive("MATCHER_MAX_POLL_MS"),
                         session_timeout_ms=positive("MATCHER_SESSION_TIMEOUT_MS"), workers=positive("MATCHER_WORKERS"),
                         processor=processor, observer=observer, health=health)
    return runner, health, common, topic


def serve(runner, health, port_name):
    stop = threading.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    server = health.serve(positive(port_name)) if os.environ.get(port_name) else None
    try:
        runner(stop)
    finally:
        if server:
            server.shutdown()
            server.server_close()
    return 0


def main():
    if sys.argv[1:] == ["evaluate"]:
        try:
            result = evaluate(loads(sys.stdin.buffer.read()))
            sys.stdout.buffer.write(canonical(result) + b"\n")
        except ContractError as exc:
            print(exc.code, file=sys.stderr)
            return 2
        return 0
    require(len(sys.argv) == 2 and sys.argv[1] in ("run", "run-matching", "run-analytics"))
    command = sys.argv[1]
    logging.basicConfig(level=logging.INFO, format="%(message)s")
    from .analytics_runtime import CombinedHealth, analytics_runner, run_workers
    if command == "run-analytics":
        from .kafka import observe
        from .runtime import kafka_config
        common = kafka_config(prefix="ANALYTICS")
        topic = os.environ.get("ANALYTICS_TOPIC") or required("KAFKA_TOPIC_BATCH_EVENTS")
        matching_group = os.environ.get("MATCHER_GROUP_ID") or os.environ.get("KAFKA_CONSUMER_GROUP", "matching-worker-v1")
        runner, health = analytics_runner(common, topic, matching_group, observe, positive,
                                          os.environ.get("ANALYTICS_LOCAL_TEST") == "1", standalone=True)
        return serve(runner.run, health, "ANALYTICS_HEALTH_PORT")
    if command == "run-matching":
        matching_group = os.environ.get("MATCHER_GROUP_ID") or required("KAFKA_CONSUMER_GROUP")
        require(matching_group != os.environ.get("ANALYTICS_GROUP_ID", "analytics-processing-v1"),
                "INVALID_MATCHING_CONFIGURATION")
    runner, health, common, topic = matching_runner()
    if command == "run-matching":
        return serve(runner.run, health, "MATCHER_HEALTH_PORT")
    # Compatibility command for deployments that still run both consumers together.
    from .kafka import observe
    local = os.environ.get("MATCHER_LOCAL_TEST") == "1"
    analytics = analytics_runner(common, topic, runner.group_id, observe, positive, local)
    probe_health = CombinedHealth([health, analytics[1]], observe) if analytics else health
    run = (lambda stop: run_workers([runner, analytics[0]], stop)) if analytics else runner.run
    return serve(run, probe_health, "MATCHER_HEALTH_PORT")


if __name__ == "__main__":
    sys.exit(main())
