import importlib.util
import os
import threading
import time
import unittest
from concurrent.futures import Future
from unittest.mock import Mock, patch

from matcher.contract import ContractError, canonical, loads
from matcher.events import quarantine
from matcher.worker import Delivery, Processor, PublishError, Record
from helpers import event
from test_facade import FixtureServer
from test_worker import FacadeDouble, PublisherDouble

HAS_CLIENT = importlib.util.find_spec("confluent_kafka") is not None


@unittest.skipUnless(HAS_CLIENT, "Kafka client is installed in the pinned Docker test image")
class OffsetTests(unittest.TestCase):
    def make_runner(self):
        from matcher.kafka import KafkaRunner
        with patch("matcher.kafka.Consumer") as consumer:
            runner = KafkaRunner({}, topic="ewaste.batch.events", group_id="unit", offset_reset="earliest",
                                  max_poll_ms=300000, session_timeout_ms=6000, workers=1,
                                  processor=Mock(retry_max=0), observer=Mock())
            return runner, consumer.return_value

    def test_commit_failure_retries_exact_offset_without_reprocessing(self):
        from confluent_kafka import KafkaException, TopicPartition
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0)
            runner.owned = {key}; runner.generation = 1
            delivery = Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z", stage="DONE")
            runner.jobs[key] = Job(delivery, 1)
            consumer.commit.side_effect = [KafkaException(), [TopicPartition(*key, 6)]]
            runner._advance()
            self.assertIn(key, runner.jobs)
            runner._advance()
            self.assertNotIn(key, runner.jobs)
            self.assertEqual([c.kwargs["offsets"][0].offset for c in consumer.commit.call_args_list], [6, 6])
            runner.processor.step.assert_not_called()
        finally: runner.executor.shutdown()

    def test_revoked_inflight_completion_cannot_commit(self):
        from confluent_kafka import TopicPartition
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0); runner.owned = {key}; runner.generation = 1
            delivery = Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z")
            future = Future(); runner.jobs[key] = Job(delivery, 1, future=future)
            future.set_running_or_notify_cancel()
            runner._revoke(consumer, [TopicPartition(*key)])
            delivery.stage = "DONE"; future.set_result(0)
            runner._advance()
            consumer.commit.assert_not_called()
        finally: runner.executor.shutdown()

    def test_revoke_cancels_queued_work(self):
        from confluent_kafka import TopicPartition
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0); runner.owned = {key}; runner.generation = 1
            delivery = Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z")
            future = Future(); runner.jobs[key] = Job(delivery, 1, future=future)
            runner._revoke(consumer, [TopicPartition(*key)])
            self.assertTrue(future.cancelled())
            consumer.commit.assert_not_called()
        finally: runner.executor.shutdown()

    def test_lower_unresolved_offset_blocks_buffered_record(self):
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0); runner.owned = {key}; runner.generation = 1
            delivery = Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z")
            runner.jobs[key] = Job(delivery, 1, due=time.monotonic() + 100,
                                    buffered=[Record(*key, 6, b"key", b"{}")])
            runner._advance()
            consumer.commit.assert_not_called()
            runner.processor.step.assert_not_called()
        finally: runner.executor.shutdown()

    def test_assignment_cancels_old_jobs_and_clears_partition_health(self):
        from confluent_kafka import TopicPartition
        from matcher.health import Health
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0)
            runner.owned = {key}
            runner.health = Health()
            runner.health.blocked.add(key)
            future = Future()
            runner.jobs[key] = Job(Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z"), 0, future)
            partitions = [TopicPartition(key[0], 1)]
            runner._assign(consumer, partitions)
            self.assertTrue(future.cancelled())
            self.assertEqual(runner.jobs, {})
            self.assertEqual(runner.owned, {(key[0], 1)})
            self.assertEqual(runner.generation, 1)
            self.assertEqual(runner.health.blocked, set())
            consumer.assign.assert_called_once_with(partitions)
        finally:
            runner.executor.shutdown()

    def test_buffered_records_commit_in_order_and_resume_only_when_drained(self):
        from confluent_kafka import TopicPartition
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0)
            runner.owned = {key}
            def message(offset, partition=0):
                value = Mock()
                value.topic.return_value, value.partition.return_value = key[0], partition
                value.offset.return_value, value.key.return_value, value.value.return_value = offset, b"key", b"{}"
                return value
            runner._record(message(4, partition=1))
            self.assertFalse(runner.jobs)
            runner._record(message(5))
            runner._record(message(6))
            consumer.pause.assert_called_once()
            first, second = Future(), Future()
            consumer.commit.side_effect = [[TopicPartition(*key, 6)], [TopicPartition(*key, 7)]]
            with patch.object(runner.executor, "submit", side_effect=[first, second]) as submit:
                runner._advance()
                runner._advance()
                self.assertEqual(submit.call_count, 1)
                consumer.commit.assert_not_called()
                runner.jobs[key].delivery.stage = "DONE"
                first.set_result(0)
                runner._advance()
                self.assertEqual(runner.jobs[key].delivery.record.offset, 6)
                consumer.resume.assert_not_called()
                runner._advance()
                self.assertEqual(submit.call_args.args[1].record.offset, 6)
                runner.jobs[key].delivery.stage = "DONE"
                second.set_result(0)
                runner._advance()
            self.assertEqual([c.kwargs["offsets"][0].offset for c in consumer.commit.call_args_list], [6, 7])
            self.assertTrue(all(c.kwargs["asynchronous"] is False for c in consumer.commit.call_args_list))
            self.assertFalse(runner.jobs)
            consumer.resume.assert_called_once()
        finally:
            runner.executor.shutdown()

    def test_internal_worker_failure_pauses_without_acknowledging_or_leaking_exception(self):
        from matcher.kafka import Job
        runner, consumer = self.make_runner()
        try:
            key = ("ewaste.batch.events", 0)
            runner.owned = {key}
            runner.processor.retry_max = 4
            delivery = Delivery(Record(*key, 5, b"key", b"{}"), "2026-09-19T00:00:00.000000Z")
            future = Future()
            future.set_exception(RuntimeError("private-exception-detail"))
            runner.jobs[key] = Job(delivery, 0, future)
            with patch("matcher.kafka.time.monotonic", return_value=100):
                runner._advance()
            self.assertEqual(runner.jobs[key].due, 104)
            self.assertIsNone(runner.jobs[key].future)
            consumer.commit.assert_not_called()
            runner.observe.assert_called_once_with("delivery_paused", delivery, code="WORKER_INTERNAL_ERROR")
        finally:
            runner.executor.shutdown()

    def test_poll_errors_do_not_stop_consumption_and_shutdown_does_not_commit(self):
        from confluent_kafka import KafkaException, TopicPartition
        runner, consumer = self.make_runner()
        runner.health = Mock()
        message = Mock()
        message.error.return_value = None
        message.topic.return_value, message.partition.return_value = "ewaste.batch.events", 0
        message.offset.return_value, message.key.return_value, message.value.return_value = 5, b"key", b"{}"
        error = Mock()
        error.error.return_value = "test-broker-error"
        consumer.poll.side_effect = [KafkaException(), error, message, None]
        consumer.subscribe.side_effect = lambda topics, **callbacks: callbacks["on_assign"](consumer, [TopicPartition(topics[0], 0)])
        stop = Mock()
        stop.is_set.side_effect = [False, False, False, False, True]
        with patch.object(runner.executor, "submit", return_value=Future()) as submit, \
                patch.object(runner.executor, "shutdown", wraps=runner.executor.shutdown) as shutdown:
            runner.run(stop)
            self.assertEqual(submit.call_args.args[1].record.offset, 5)
            shutdown.assert_called_once_with(wait=True, cancel_futures=True)
        self.assertEqual(runner.health.polled.call_count, 3)
        self.assertEqual([c.args[0] for c in runner.observe.call_args_list].count("consumer_error"), 2)
        self.assertFalse(runner.jobs)
        self.assertFalse(runner.owned)
        consumer.close.assert_called_once_with()
        consumer.commit.assert_not_called()


@unittest.skipUnless(HAS_CLIENT, "Kafka client is installed in the pinned Docker test image")
class PublisherTests(unittest.TestCase):
    def setUp(self):
        from matcher.kafka import QuarantinePublisher
        producer_patch = patch("matcher.kafka.Producer")
        self.producer_factory = producer_patch.start()
        self.addCleanup(producer_patch.stop)
        self.producer = self.producer_factory.return_value
        self.publisher = QuarantinePublisher({"bootstrap.servers": "kafka:9092"}, "ewaste.matching.dlq", 1)
        record = Record("ewaste.batch.events", 0, 5, b"key", b'{"private":"invalid",')
        self.key, self.raw = quarantine(record, "INVALID_CONTRACT", "VALIDATE_EVENT", 0,
                                        "2026-09-19T00:00:00.000000Z", "2026-09-19T00:00:00.000000Z",
                                        max_parse_bytes=1000)

    def test_idempotent_publish_requires_delivery_acknowledgment(self):
        config = self.producer_factory.call_args.args[0]
        self.assertIs(config["enable.idempotence"], True)
        self.assertEqual((config["acks"], config["max.in.flight.requests.per.connection"], config["message.timeout.ms"]),
                         ("all", 1, 1000))
        self.producer.poll.side_effect = lambda _: self.producer.produce.call_args.kwargs["on_delivery"](None, Mock())
        self.publisher.publish(self.key, self.raw)
        call = self.producer.produce.call_args
        self.assertEqual(call.args, ("ewaste.matching.dlq",))
        self.assertEqual((call.kwargs["key"], call.kwargs["value"]), (self.key, self.raw))
        self.producer.poll.assert_called_once_with(0.1)

    def test_delivery_errors_and_timeout_require_retry(self):
        self.producer.poll.side_effect = lambda _: self.producer.produce.call_args.kwargs["on_delivery"]("test-error", Mock())
        with self.assertRaises(PublishError):
            self.publisher.publish(self.key, self.raw)
        self.producer.poll.reset_mock(side_effect=True)
        with patch("matcher.kafka.time.monotonic", side_effect=[0, 3]), self.assertRaises(PublishError):
            self.publisher.publish(self.key, self.raw)
        self.producer.poll.assert_not_called()

    def test_queue_and_broker_exceptions_require_retry(self):
        from confluent_kafka import KafkaException
        for error in (BufferError(), KafkaException()):
            with self.subTest(error=type(error).__name__):
                self.producer.produce.side_effect = error
                with self.assertRaises(PublishError):
                    self.publisher.publish(self.key, self.raw)

    def test_invalid_key_or_payload_is_rejected_before_publication(self):
        for key, raw in ((b"wrong-key", self.raw), (self.key, b"{}")):
            with self.subTest(key=key), self.assertRaises(ContractError):
                self.publisher.publish(key, raw)
        self.producer.produce.assert_not_called()

    def test_observations_expose_identity_without_raw_payload(self):
        from matcher.kafka import observe
        source = event()
        delivery = Delivery(Record("ewaste.batch.events", 0, 5, b"key", b"private-payload"),
                            "2026-09-19T00:00:00.000000Z", event=source, stage="PREPARE", retries=2)
        with self.assertLogs("matcher", level="INFO") as captured:
            observe("delivery_paused", delivery, code="FACADE_UNAVAILABLE")
        value = loads(captured.records[0].message)
        self.assertEqual((value["event_id"], value["batch_id"], value["correlation_id"]),
                         (source["event_id"], source["batch_id"], source["correlation_id"]))
        self.assertEqual((value["offset"], value["retry_count"], value["code"]), ("5", 2, "FACADE_UNAVAILABLE"))
        self.assertNotIn("private-payload", captured.output[0])
        self.assertNotIn("original_event", value)


@unittest.skipUnless(os.environ.get("MATCHER_TEST_BROKER") and HAS_CLIENT, "requires isolated Kafka test broker")
class BrokerTests(unittest.TestCase):
    def test_real_kafka_outage_replay_dlq_and_consumer_restart(self):
        from confluent_kafka import Consumer, Producer, TopicPartition
        from confluent_kafka.admin import AdminClient, NewTopic
        from matcher.kafka import KafkaRunner, QuarantinePublisher
        common = {"bootstrap.servers": os.environ["MATCHER_TEST_BROKER"], "security.protocol": "PLAINTEXT"}
        # Protocol-double fixtures must not enter the real Go persistence stream.
        topic, dlq, group = "matcher.protocol-test.events", "matcher.protocol-test.dlq", "matcher-fixture-v1"
        admin = AdminClient(common)
        for pending in admin.create_topics([NewTopic(topic, 2, 1, config={"cleanup.policy": "delete"}),
                                            NewTopic(dlq, 1, 1, config={"cleanup.policy": "delete"})]).values():
            pending.result(timeout=20)
        facade = FacadeDouble(); facade.fail_result_once = True
        server = FixtureServer(facade); server.status = 503
        observations, stops, threads, failures = [], [], [], []
        inspect = Consumer({**common, "group.id": group, "enable.auto.commit": False, "enable.auto.offset.store": False})
        reader = Consumer({**common, "group.id": "dlq-fixture-reader", "enable.auto.commit": False,
                           "auto.offset.reset": "earliest"})
        def observe(name, delivery=None, **fields):
            observations.append((name, delivery.record.offset if delivery else None, fields))

        def start():
            processor = Processor(server.client(), QuarantinePublisher(common, dlq, 5), max_bytes=1_000_000,
                                   retry_base=0.1, retry_max=0.2, max_refreshes=3, observe=observe)
            runner = KafkaRunner(common, topic=topic, group_id=group, offset_reset="earliest", max_poll_ms=300000,
                                 session_timeout_ms=6000, workers=2, processor=processor, observer=observe)
            stop = threading.Event()
            def run():
                try: runner.run(stop)
                except Exception as exc: failures.append(type(exc).__name__)
            thread = threading.Thread(target=run, daemon=True)
            thread.start(); stops.append(stop); threads.append(thread)

        def until(predicate, seconds=25):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                self.assertFalse(failures, failures)
                if predicate(): return
                time.sleep(0.05)
            self.fail("Kafka fixture timed out: " + str(observations))

        producer = Producer({**common, "enable.idempotence": True, "acks": "all"})
        accepted = []
        def publish(value):
            producer.produce(topic, partition=0, key=event()["batch_id"].encode(), value=value,
                             on_delivery=lambda err, msg: accepted.append(err))
            self.assertEqual(producer.flush(10), 0)
            self.assertIsNone(accepted[-1])
        try:
            publish(canonical(event())); publish(canonical(event()))
            publish(b'{"private_note":"must-not-leak",')
            publish(b'{"event_type":"UnrelatedFutureType"}')
            start()
            until(lambda: any(x[0] == "delivery_paused" for x in observations))
            self.assertLess(inspect.committed([TopicPartition(topic, 0)], timeout=5)[0].offset, 0)
            server.status = 0
            until(lambda: any(x[:2] == ("offset_committed", 3) for x in observations))
            self.assertEqual(inspect.committed([TopicPartition(topic, 0)], timeout=5)[0].offset, 4)
            self.assertEqual([name for name, _ in facade.calls].count("result"), 1)
            self.assertIn("get", [name for name, _ in facade.calls])
            reader.assign([TopicPartition(dlq, 0, 0)])
            message = reader.poll(10)
            self.assertIsNotNone(message); self.assertIsNone(message.error())
            payload = loads(message.value())
            self.assertEqual(payload["source_offset"], "2")
            self.assertEqual(message.key(), payload["quarantine_id"].encode())
            self.assertNotIn(b"must-not-leak", message.value())
            stops[-1].set(); threads[-1].join(10); self.assertFalse(threads[-1].is_alive())
            publish(canonical(event()))
            start()
            until(lambda: any(x[:2] == ("offset_committed", 4) for x in observations))
            self.assertEqual(inspect.committed([TopicPartition(topic, 0)], timeout=5)[0].offset, 5)
            self.assertEqual([name for name, _ in facade.calls].count("result"), 1)
            print("KAFKA_EVIDENCE " + canonical({"broker": "apache/kafka:4.1.0", "records": 5,
                  "committed_next_offset": 5, "result_posts": 1, "dlq_records_verified": 1,
                  "api_boundary": "HTTP protocol test double; no SQL persistence claim"}).decode())
        finally:
            for stop in stops: stop.set()
            for thread in threads: thread.join(10)
            inspect.close(); reader.close(); server.close()
