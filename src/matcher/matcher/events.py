"""Versioned business-event consumption and allowlisted quarantine publication."""
import hashlib
import re
from datetime import timedelta

from .contract import (ContractError, _schemas, canonical, digest, exact_types,
                       loads, require, timestamp, validate)


def submission(raw, key, max_bytes):
    require(raw is not None and len(raw) <= max_bytes, "LIMIT_EXCEEDED")
    event = loads(raw)
    require(isinstance(event, dict) and isinstance(event.get("event_type"), str))
    kind = event["event_type"]
    if kind not in ("RequestSubmitted", "MatchingCompleted"):
        return None  # An unrelated type never invokes the matching façade.
    require(type(event.get("schema_version")) is int and event["schema_version"] == 1,
            "UNSUPPORTED_SCHEMA_VERSION")
    # Unknown compatible fields are retained for the authoritative replay check.
    # Validate only known fields so optional extension values do not change v1.
    safe = allowlist(event, kind)
    exact_types(safe)
    validate(kind, event)
    require(key == event["batch_id"].encode("utf-8"))
    require(int(event["claim_epoch"]) <= 2**64 - 1)
    data = event["data"]
    occurred = timestamp(event["occurred_at"])
    if kind == "MatchingCompleted":
        require(data["eligible_count"] <= data["evaluated_count"])
        require(event["batch_version"] == data["input_batch_version"] + (data["outcome"] == "MATCHED"))
        require(timestamp(data["evaluation_at"]) <= occurred)
        return None
    submitted = timestamp(data["submitted_at"])
    try:
        require(submitted + timedelta(hours=48) <= timestamp(data["collection_deadline"]) <= submitted + timedelta(days=90))
    except OverflowError as exc:
        raise ContractError() from exc
    require(occurred == submitted)
    return event


def allowlist(event, kind="RequestSubmitted"):
    schema = _schemas[kind + ".v1.schema.json"]
    result = {k: event[k] for k in schema["properties"] if k in event}
    if isinstance(event.get("data"), dict):
        result["data"] = {k: event["data"][k] for k in schema["properties"]["data"]["properties"] if k in event["data"]}
    return result


def quarantine(record, error_code, stage, retries, first_seen, last_seen, event=None, *, max_parse_bytes):
    raw_hash = hashlib.sha256(record.value or b"").hexdigest()
    value = {
        "dlq_schema_version": 1,
        "quarantine_id": digest([record.topic, record.partition, str(record.offset), raw_hash]),
        "source_topic": record.topic, "source_partition": record.partition,
        "source_offset": str(record.offset), "source_event_id": None,
        "original_record_sha256": raw_hash, "error_code": error_code,
        "failed_stage": stage, "retry_count": retries,
        "first_seen_at": first_seen, "last_seen_at": last_seen, "correlation_id": None,
    }
    if event is None and record.value is not None and len(record.value) <= max_parse_bytes:
        try:
            parsed = loads(record.value)
            if isinstance(parsed, dict):
                identity, trace = parsed.get("event_id"), parsed.get("correlation_id")
                if isinstance(identity, str) and re.fullmatch(r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", identity):
                    value["source_event_id"] = identity
                if isinstance(trace, str) and 1 <= len(trace) <= 128:
                    value["correlation_id"] = trace
        except ContractError:
            pass
    if event is not None:
        # Only an already validated RequestSubmitted is eligible for reconstruction.
        safe = allowlist(event)
        validate("RequestSubmitted", safe)
        value.update(source_event_id=safe["event_id"], correlation_id=safe["correlation_id"], original_event=safe)
    require(0 <= record.offset <= 2**63 - 1)
    validate("MatchingQuarantine", value)
    return value["quarantine_id"].encode("ascii"), canonical(value)
