"""Manual contiguous offsets; polling continues while API/DLQ work is pending."""
import json
import logging
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timezone

from confluent_kafka import Consumer, KafkaException, Producer, TopicPartition

from .contract import loads, require, validate
from .worker import Delivery, PublishError, Record, utc_now
from .telemetry import lag_samples

LOG = logging.getLogger("matcher")


def observe(name, delivery=None, **fields):
    # Never log raw records, API bodies, tokens, snapshots or exception strings.
    allowed = {"code", "count", "duration_ms", "operation", "outcome", "ready",
               "consumer_group", "topic", "partition", "consumer_lag", "committed_offset",
               "end_offset", "sample_available"}
    payload = {"component": "matcher-worker", "telemetry_version": 1, "service": "analytics", "timestamp": utc_now(),
               "observation": name, **{k: v for k, v in fields.items() if k in allowed}}
    payload.setdefault("outcome", {"delivery_paused": "RETRYING", "offset_commit_failed": "FAILED",
                                   "consumer_error": "FAILED", "offset_committed": "SUCCEEDED"}.get(name, "OBSERVED"))
    if name == "offset_commit_failed": payload.setdefault("code", "OFFSET_COMMIT_FAILED")
    if name == "consumer_error": payload.setdefault("code", "CONSUMER_UNAVAILABLE")
    if delivery is not None:
        latency_ms = None
        if hasattr(delivery, "first_seen") and delivery.first_seen:
            try:
                dt = datetime.fromisoformat(delivery.first_seen.replace("Z", "+00:00"))
                latency_ms = max(0, int((datetime.now(timezone.utc) - dt).total_seconds() * 1000))
            except Exception:
                pass
        payload.update(topic=delivery.record.topic, partition=delivery.record.partition,
                       offset=str(delivery.record.offset), stage=delivery.stage,
                       retry_count=delivery.retries, failure_streak=delivery.failure_streak,
                       disposition=delivery.disposition)
        if latency_ms is not None:
            payload["latency_ms"] = latency_ms
        if delivery.error_code:
            payload["error_code"] = delivery.error_code
        if delivery.event:
            payload.update(event_id=delivery.event["event_id"], batch_id=delivery.event["batch_id"],
                           correlation_id=delivery.event["correlation_id"], run_id=delivery.run_id,
                           command_id=delivery.event.get("command_id"),
                           event_type=delivery.event.get("event_type"))
    LOG.info(json.dumps(payload, separators=(",", ":"), ensure_ascii=True))


class QuarantinePublisher:
    def __init__(self, common_config, topic, timeout):
        require(timeout > 0)
        self.topic, self.timeout = topic, timeout
        self.producer = Producer({**common_config, "enable.idempotence": True, "acks": "all",
                                  "max.in.flight.requests.per.connection": 1,
                                  "message.timeout.ms": int(timeout * 1000)})

    def publish(self, key, raw):
        value = loads(raw)
        validate("MatchingQuarantine", value)
        require(key == value["quarantine_id"].encode("ascii"))
        outcome = []
        try:
            self.producer.produce(self.topic, key=key, value=raw,
                                  on_delivery=lambda error, message: outcome.append(error))
            deadline = time.monotonic() + self.timeout + 1
            while not outcome and time.monotonic() < deadline:
                self.producer.poll(0.1)
        except (KafkaException, BufferError) as exc:
            raise PublishError() from exc
        if not outcome or outcome[0] is not None:
            raise PublishError()


@dataclass
class Job:
    delivery: Delivery
    generation: int
    future: object = None
    due: float = 0
    buffered: list = field(default_factory=list)


class KafkaRunner:
    def __init__(self, common_config, *, topic, group_id, offset_reset, max_poll_ms,
                 session_timeout_ms, workers, processor, observer=observe, health=None):
        require(offset_reset in ("earliest", "latest", "error"))
        self.topic, self.processor, self.observe = topic, processor, observer
        self.health = health
        self.group_id, self.next_telemetry_at = group_id, 0
        monitoring = {"statistics.interval.ms": 1000, "stats_cb": self._stats}
        self.consumer = Consumer({**common_config, **monitoring, "group.id": group_id,
                                  "enable.auto.commit": False, "enable.auto.offset.store": False,
                                  "auto.offset.reset": offset_reset, "isolation.level": "read_committed",
                                  "partition.assignment.strategy": "range",
                                  "max.poll.interval.ms": max_poll_ms, "session.timeout.ms": session_timeout_ms,
                                  "allow.auto.create.topics": False})
        self.executor = ThreadPoolExecutor(max_workers=workers, thread_name_prefix="matching-delivery")
        self.jobs, self.owned, self.generation = {}, set(), 0

    def _stats(self, raw):
        # Keep one-second health updates; rate-limit telemetry to one sample per 30s.
        try:
            if self.health:
                self.health.stats(raw)
            now = time.monotonic()
            if now < self.next_telemetry_at:
                return
            self.next_telemetry_at = now + 30
            for sample in lag_samples(json.loads(raw), self.owned):
                self.observe("consumer_lag", consumer_group=self.group_id, **sample)
            if self.health:
                ready = self.health.state(ready=True)
                self.observe("readiness", ready=ready, outcome="READY" if ready else "NOT_READY",
                             code="" if ready else "DEPENDENCY_UNAVAILABLE")
        except (ValueError, TypeError, KeyError, AttributeError):
            self.observe("consumer_lag", consumer_group=self.group_id, sample_available=False,
                         outcome="UNKNOWN", code="LAG_SAMPLE_FAILED")

    def _assign(self, consumer, partitions):
        for job in self.jobs.values():
            if job.future is not None:
                job.future.cancel()
        if self.health:
            self.health.revoked(self.owned)
        self.jobs.clear()
        self.generation += 1
        self.owned = {(p.topic, p.partition) for p in partitions}
        consumer.assign(partitions)
        self.observe("partitions_assigned", count=len(partitions))

    def _revoke(self, consumer, partitions):
        if self.health:
            self.health.revoked((p.topic, p.partition) for p in partitions)
        for part in partitions:
            key = (part.topic, part.partition)
            self.owned.discard(key)
            job = self.jobs.pop(key, None)
            if job is not None and job.future is not None:
                job.future.cancel()  # Prevent queued work starting after revocation.
        self.observe("partitions_revoked", count=len(partitions))
        # In-flight API work may finish; its result is never acknowledged here.

    def _record(self, message):
        key = (message.topic(), message.partition())
        if key not in self.owned:
            return
        record = Record(*key, message.offset(), message.key(), message.value())
        if key in self.jobs:
            # Preserve a record already buffered by the client before pause.
            self.jobs[key].buffered.append(record)
        else:
            self.jobs[key] = Job(Delivery(record, utc_now()), self.generation)
            self.consumer.pause([TopicPartition(*key)])

    def _advance(self):
        for key, job in list(self.jobs.items()):
            if key not in self.owned or job.generation != self.generation:
                continue
            if job.future is not None:
                if not job.future.done():
                    continue
                try:
                    delay = job.future.result()
                except Exception:
                    self.observe("delivery_paused", job.delivery, code="WORKER_INTERNAL_ERROR")
                    delay = self.processor.retry_max
                job.future, job.due = None, time.monotonic() + delay
            if time.monotonic() < job.due:
                continue
            if job.delivery.stage == "DONE":
                try:
                    offsets = self.consumer.commit(offsets=[TopicPartition(*key, job.delivery.record.offset + 1)],
                                                   asynchronous=False)
                    if not offsets or any(p.error is not None for p in offsets):
                        raise KafkaException()
                except KafkaException:
                    self.observe("offset_commit_failed", job.delivery)
                    job.due = time.monotonic() + self.processor.retry_max
                    continue
                self.observe("offset_committed", job.delivery)
                if job.buffered:
                    record = job.buffered.pop(0)
                    self.jobs[key] = Job(Delivery(record, utc_now()), job.generation, buffered=job.buffered)
                else:
                    self.jobs.pop(key)
                    self.consumer.resume([TopicPartition(*key)])
            else:
                job.future = self.executor.submit(self.processor.step, job.delivery)

    def run(self, stop):
        self.consumer.subscribe([self.topic], on_assign=self._assign, on_revoke=self._revoke, on_lost=self._revoke)
        try:
            while not stop.is_set():
                try:
                    message = self.consumer.poll(0.1)
                    if self.health:
                        self.health.polled()
                except KafkaException:
                    self.observe("consumer_error")
                    continue
                if message is not None:
                    if message.error():
                        self.observe("consumer_error")
                    else:
                        self._record(message)
                self._advance()
        finally:
            self.owned.clear()
            self.jobs.clear()
            self.consumer.close()  # Auto commit is disabled even on close.
            self.executor.shutdown(wait=True, cancel_futures=True)
