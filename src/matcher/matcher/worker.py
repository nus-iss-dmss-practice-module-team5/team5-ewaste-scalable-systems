"""Resumable delivery state machine; only the runner may commit Kafka offsets."""
import time
from dataclasses import dataclass
from datetime import datetime, timezone

from .contract import ContractError, require
from .core import evaluate
from .events import quarantine, submission
from .facade import FacadeError, check_result, check_run


def utc_now():
    return datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z")


@dataclass(frozen=True)
class Record:
    topic: str
    partition: int
    offset: int
    key: bytes | None
    value: bytes | None


@dataclass
class Delivery:
    record: Record
    first_seen: str
    stage: str = "VALIDATE_EVENT"
    event: dict | None = None
    run_id: str | None = None
    context: dict | None = None
    output: dict | None = None
    quarantine_record: tuple | None = None
    error_code: str | None = None
    failed_stage: str | None = None
    pending_rejection: str | None = None
    refresh_allowed: bool = False
    refreshes: int = 0
    retries: int = 0
    failure_streak: int = 0
    disposition: str | None = None


class Processor:
    def __init__(self, facade, publisher, *, max_bytes, retry_base, retry_max,
                 max_refreshes, observe, clock=utc_now):
        self.facade, self.publisher = facade, publisher
        self.max_bytes, self.retry_base, self.retry_max = max_bytes, retry_base, retry_max
        self.max_refreshes, self.observe, self.clock = max_refreshes, observe, clock

    def _quarantine(self, delivery, code):
        delivery.error_code = code
        delivery.failed_stage = delivery.stage if delivery.stage in ("VALIDATE_EVENT", "PREPARE", "EVALUATE", "COMMIT") else "COMMIT"
        delivery.stage = "QUARANTINE"

    def _pause(self, delivery, code, retry_after=0):
        delivery.retries += 1
        delivery.failure_streak += 1
        delay = max(retry_after, min(self.retry_max, self.retry_base * 2 ** min(delivery.failure_streak - 1, 20)))
        self.observe("delivery_paused", delivery, code=code)
        return delay

    def _run_response(self, delivery, response, *, resolving=False):
        phase, value = check_run(response, delivery.event, delivery.run_id,
                                 None if delivery.refresh_allowed else delivery.output)
        delivery.run_id = response["run_id"]
        if phase != "PREPARED":
            delivery.disposition, delivery.stage = phase, "DONE"
            return
        if resolving and delivery.pending_rejection:
            # GET proves the run remains prepared after a definitive rejection.
            self._quarantine(delivery, delivery.pending_rejection)
            return
        if delivery.refresh_allowed:
            previous = delivery.context
            require(value["decision_id"] == previous["decision_id"])
            if value["context_generation"] == previous["context_generation"]:
                require(value == previous)
                delivery.stage = "REFRESH"
                return
            require(value["context_generation"] > previous["context_generation"])
            delivery.refresh_allowed = False
            delivery.refreshes += 1
            delivery.context, delivery.output = value, None
            delivery.stage = "EVALUATE"
        elif delivery.output is not None:
            require(value == delivery.context)
            # A lost result response resubmits the exact existing output.
            delivery.stage = "COMMIT"
        else:
            delivery.context = value
            delivery.stage = "EVALUATE"

    def step(self, delivery):
        operation, started = delivery.stage, time.monotonic()
        outcome = "FAILED"
        try:
            delay = self._step(delivery)
            outcome = "RETRYING" if delay else ("QUARANTINED" if delivery.stage == "QUARANTINE" or delivery.disposition == "QUARANTINED" else "SUCCEEDED")
            return delay
        finally:
            self.observe("processing_step", delivery, operation=operation, outcome=outcome,
                         duration_ms=round((time.monotonic()-started)*1000, 3))

    def _step(self, delivery):
        """Perform one bounded operation; return seconds until the next attempt."""
        operation = delivery.stage
        try:
            if delivery.stage == "VALIDATE_EVENT":
                try:
                    delivery.event = submission(delivery.record.value, delivery.record.key, self.max_bytes)
                except ContractError as exc:
                    self._quarantine(delivery, exc.code)
                else:
                    if delivery.event is None:
                        delivery.disposition, delivery.stage = "IGNORED_EVENT", "DONE"
                    else:
                        delivery.stage = "PREPARE"
            elif delivery.stage == "PREPARE":
                self._run_response(delivery, self.facade.prepare(delivery.event))
            elif delivery.stage == "EVALUATE":
                try:
                    delivery.output = evaluate(delivery.context)
                except ContractError as exc:
                    self._quarantine(delivery, exc.code)
                else:
                    delivery.stage = "COMMIT"
            elif delivery.stage == "COMMIT":
                # Set RESOLVE before the call: any uncertain response must GET.
                delivery.stage = "RESOLVE"
                value = self.facade.run(delivery.run_id, delivery.event, "result", delivery.output)
                delivery.disposition = check_result(value, delivery.event, delivery.run_id, delivery.output)
                delivery.stage = "DONE"
            elif delivery.stage == "RESOLVE":
                self._run_response(delivery, self.facade.run(delivery.run_id, delivery.event), resolving=True)
            elif delivery.stage == "REFRESH":
                if delivery.refreshes >= self.max_refreshes:
                    return self._pause(delivery, "CONTEXT_CHURN")
                delivery.stage = "RESOLVE"
                response = self.facade.run(delivery.run_id, delivery.event, "refresh",
                                           {"expected_input_hash": delivery.context["input_hash"]})
                self._run_response(delivery, response)
            elif delivery.stage == "QUARANTINE":
                if delivery.quarantine_record is None:
                    delivery.quarantine_record = quarantine(
                        delivery.record, delivery.error_code, delivery.failed_stage, delivery.retries,
                        delivery.first_seen, max(delivery.first_seen, self.clock()), delivery.event,
                        max_parse_bytes=self.max_bytes)
                self.publisher.publish(*delivery.quarantine_record)
                delivery.disposition, delivery.stage = "QUARANTINED", "DONE"
            delivery.failure_streak = 0
            return 0
        except FacadeError as exc:
            if exc.code == "STALE_CONTEXT" and exc.status == 409 and operation in ("COMMIT", "REFRESH") and delivery.output is not None:
                delivery.refresh_allowed = True
                delivery.stage = "REFRESH"
                return 0
            if exc.code == "STATE_CONFLICT" and exc.status == 409:
                disposition = exc.body.get("durable_disposition")
                if disposition and delivery.run_id:
                    try:
                        require(check_result(disposition, delivery.event, delivery.run_id) == "SKIPPED")
                    except ContractError:
                        return self._pause(delivery, "INVALID_FACADE_RESPONSE")
                    delivery.disposition, delivery.stage = "SKIPPED", "DONE"
                    return 0
                # A bare 409 is never a terminal acknowledgment.
                if delivery.run_id:
                    delivery.stage = "RESOLVE"
                return self._pause(delivery, "STATE_CONFLICT")
            terminal = {"INVALID_CONTRACT", "INVALID_RESULT", "UNSUPPORTED_RULE_SET", "IDEMPOTENCY_CONFLICT", "DATA_INTEGRITY_ERROR", "LIMIT_EXCEEDED"}
            if exc.status in (400, 409, 422) and exc.code in terminal:
                if delivery.run_id and delivery.stage == "RESOLVE":
                    delivery.pending_rejection = exc.code
                else:
                    self._quarantine(delivery, exc.code)
                return 0
            return self._pause(delivery, "FACADE_UNAVAILABLE", exc.retry_after)
        except ContractError:
            # Malformed/mismatched API responses are dependency incidents, not
            # proof that the original Kafka record should be discarded.
            return self._pause(delivery, "INVALID_FACADE_RESPONSE")
        except PublishError:
            return self._pause(delivery, "DLQ_UNAVAILABLE")


class PublishError(Exception):
    """No confirmed broker acknowledgment; source offset must remain pending."""
