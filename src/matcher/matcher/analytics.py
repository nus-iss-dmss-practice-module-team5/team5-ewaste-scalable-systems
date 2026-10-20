"""D1/D3 d3-v1: exact decimal arithmetic, optional outcomes, no CO2 estimates."""
import hashlib
import re
import uuid
from decimal import Decimal

from .contract import exact_types, loads, require, timestamp, validate

RULE_VERSION = "d3-v1"
CATEGORIES = {"ICT_EQUIPMENT", "LARGE_APPLIANCE", "BATTERIES", "CONSUMER_ELECTRONICS"}
AMOUNTS = ("reused_kg", "recycled_kg", "disposed_kg")


def weight(value):
    require(isinstance(value, str), "INVALID_WEIGHT_FORMAT")
    require(not value.startswith("-"), "NEGATIVE_OUTCOME_WEIGHT")
    if re.fullmatch(r"[0-9]+\.[0-9]{3,}", value):
        require(False, "EXCESS_PRECISION")
    require(re.fullmatch(r"(?:0|[1-9][0-9]*)(?:\.[0-9]{1,2})?", value) is not None
            and len(value) <= 8, "INVALID_WEIGHT_FORMAT")
    return Decimal(value)


def kg(value):
    return format(value, ".2f")


def evaluate(input):
    require(isinstance(input, dict), "MISSING_RECEIPT")
    require(input.get("rule_version") == RULE_VERSION, "UNSUPPORTED_RULE_VERSION")
    require(all(input.get(k) is not None for k in ("actual_category", "actual_item_count", "actual_weight_kg")),
            "MISSING_RECEIPT")
    require(all(input.get(k) in CATEGORIES for k in ("declared_category", "actual_category")), "INVALID_CATEGORY")
    require(all(type(input.get(k)) is int and 1 <= input[k] <= 100000
                for k in ("declared_quantity", "actual_item_count")), "INVALID_ITEM_COUNT")
    declared, actual = (weight(input.get(k)) for k in ("declared_weight_kg", "actual_weight_kg"))
    require(all(Decimal("0.10") <= w <= Decimal("50000") for w in (declared, actual)), "INVALID_WEIGHT_RANGE")
    metrics = {"declared_weight_kg": kg(declared), "actual_weight_kg": kg(actual),
               "declared_quantity": input["declared_quantity"], "actual_item_count": input["actual_item_count"],
               "category_match": input["declared_category"] == input["actual_category"],
               "weight_delta_kg": kg(actual - declared),
               "count_delta": input["actual_item_count"] - input["declared_quantity"]}
    flags = []
    for mismatch, flag in ((not metrics["category_match"], "CATEGORY_MISMATCH"),
                           (metrics["count_delta"] != 0, "COUNT_MISMATCH"),
                           (actual != declared, "WEIGHT_MISMATCH")):
        if mismatch:
            flags.append(flag)
    values = [input.get(k) for k in AMOUNTS]
    if all(v is None for v in values):
        metrics.update(dict.fromkeys((*AMOUNTS, "diverted_kg")))
        metrics["unknown_kg"] = kg(actual)
        quality = "MISSING"
        flags.append("MISSING_OUTCOME")
    else:
        require(all(v is not None for v in values), "INCOMPLETE_ALLOCATION_OBJECT")
        reused, recycled, disposed = map(weight, values)
        unknown = actual - reused - recycled - disposed
        require(unknown >= 0, "OUTCOME_EXCEEDS_RECEIVED_WEIGHT")
        metrics.update(zip(AMOUNTS, map(kg, (reused, recycled, disposed))))
        metrics.update(diverted_kg=kg(reused + recycled), unknown_kg=kg(unknown))
        quality = "PARTIAL" if unknown else "COMPLETE"
        if unknown:
            flags.append("UNALLOCATED_WEIGHT")
    return {"rule_version": RULE_VERSION, "data_quality": quality, "metrics": metrics, "anomaly_codes": flags}


def source_event(raw, key, max_bytes):
    require(isinstance(raw, bytes) and len(raw) <= max_bytes, "INVALID_ANALYTICS_EVENT")
    event = loads(raw)
    require(isinstance(event, dict) and isinstance(event.get("event_type"), str), "INVALID_ANALYTICS_EVENT")
    if event["event_type"] != "RecyclingCompleted":
        return None
    validate("RecyclingCompleted", event)
    exact_types(event)
    require(key == event["batch_id"].encode("utf-8"), "ANALYTICS_KEY_MISMATCH")
    require(event["batch_id"] == event["aggregate_id"] == event["data"]["batch_id"], "ANALYTICS_IDENTITY_MISMATCH")
    require(event["claim_epoch"] == event["data"]["claim_epoch"]
            and 1 <= int(event["claim_epoch"]) <= 2**64 - 1, "ANALYTICS_IDENTITY_MISMATCH")
    require(event["aggregate_version"] < 2**32 - 1, "INVALID_ANALYTICS_EVENT")
    timestamp(event["occurred_at"])
    return event


def acknowledgement(prepared, event):
    require(isinstance(prepared, dict), "INVALID_ANALYTICS_PREPARATION")
    identity = {"batch_id": event["batch_id"], "source_event_id": event["event_id"],
                "source_event_version": event["aggregate_version"], "rule_version": event["data"]["rule_version"],
                "correlation_id": event["correlation_id"]}
    require(all(prepared.get(k) == v for k, v in identity.items()), "ANALYTICS_IDENTITY_MISMATCH")
    raw = prepared.get("input_canonical_json")
    require(isinstance(raw, str), "INVALID_ANALYTICS_PREPARATION")
    input_hash = hashlib.sha256(raw.encode("utf-8")).hexdigest()
    require(input_hash == prepared.get("input_hash"), "ANALYTICS_INPUT_HASH_MISMATCH")
    frozen = loads(raw)
    exact_types(frozen)
    expected = {**event["data"], "source_event_id": event["event_id"],
                "aggregate_version": event["aggregate_version"], "correlation_id": event["correlation_id"]}
    require(frozen == expected, "ANALYTICS_FROZEN_INPUT_MISMATCH")
    result = evaluate(frozen)
    require(frozen["data_quality"] == result["data_quality"] and all(
        frozen[k] == result["metrics"][k] for k in (*AMOUNTS, "unknown_kg", "diverted_kg",
                                                  "declared_weight_kg", "actual_weight_kg")),
        "ANALYTICS_FROZEN_TOTAL_MISMATCH")
    # Stable across redelivery, processes and response loss; never based on wall-clock time.
    run_id = str(uuid.uuid5(uuid.NAMESPACE_URL, "ewaste:analytics:" + event["event_id"] + ":" +
                            RULE_VERSION + ":" + input_hash))
    return {**result, "source_event_id": event["event_id"], "source_event_version": event["aggregate_version"],
            "analytics_run_id": run_id, "input_hash": input_hash}


def check_completion(response, event, output):
    require(isinstance(response, dict), "INVALID_ANALYTICS_ACK")
    data = response.get("data", {})
    require(isinstance(data, dict) and data.get("batch_id") == event["batch_id"]
            and data.get("status") == "COMPLETED" and data.get("version") == event["aggregate_version"] + 1
            and response.get("correlation_id") == event["correlation_id"]
            and response.get("event_state") in ("PENDING", "REPLAYED"), "INVALID_ANALYTICS_ACK")
    require(all(data.get(k) == output[k] for k in ("metrics", "data_quality", "anomaly_codes")), "INVALID_ANALYTICS_ACK")
    for value in (data.get("analytics_result_id"), response.get("event_id")):
        require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value),
                "INVALID_ANALYTICS_ACK")
