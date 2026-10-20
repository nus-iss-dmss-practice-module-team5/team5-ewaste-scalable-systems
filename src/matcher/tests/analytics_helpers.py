import hashlib
import json
from pathlib import Path

from matcher.contract import canonical


def fixtures():
    return json.loads((Path(__file__).parent / "fixtures/analytics-d3-v1.json").read_text())["cases"]


def source(case=None):
    case = case or fixtures()[0]
    data = {**case["input"], "batch_id": "b2000000-0000-4000-8000-000000000001",
            "receipt_id": "receipt-1", "receipt_version": 1, "treatment_id": "treatment-1", "treatment_version": 1,
            "facility_org_id": "PROC-001", "actor_user_id": "USR-004", "claim_epoch": "1",
            "evidence_id": None, "evidence_status": "ABSENT", "data_quality": case["expected"]["data_quality"],
            **{k: case["expected"]["metrics"][k] for k in ("unknown_kg", "diverted_kg")}}
    return {"event_id": "e2000000-0000-4000-8000-000000000001", "event_type": "RecyclingCompleted",
            "schema_version": 1, "producer": "go-workflow-service", "aggregate_type": "EWasteBatch",
            "aggregate_id": data["batch_id"], "batch_id": data["batch_id"], "aggregate_version": 8,
            "command_id": "c2000000-0000-4000-8000-000000000001", "claim_epoch": "1",
            "sequence_in_command": 1, "occurred_at": "2026-10-07T00:00:00.000000Z",
            "correlation_id": "analytics-golden-1", "data": data}


def preparation(event):
    frozen = {**event["data"], "source_event_id": event["event_id"], "aggregate_version": event["aggregate_version"],
              "correlation_id": event["correlation_id"]}
    raw = canonical(frozen).decode()
    return {"batch_id": event["batch_id"], "source_event_id": event["event_id"],
            "source_event_version": event["aggregate_version"], "correlation_id": event["correlation_id"],
            "rule_version": event["data"]["rule_version"], "input_hash": hashlib.sha256(raw.encode()).hexdigest(),
            "input_canonical_json": raw}


def completion(event, output):
    return {"data": {"batch_id": event["batch_id"], "status": "COMPLETED", "version": event["aggregate_version"] + 1,
                     "analytics_result_id": "a2000000-0000-4000-8000-000000000001",
                     **{k: output[k] for k in ("metrics", "data_quality", "anomaly_codes")}},
            "correlation_id": event["correlation_id"], "event_state": "PENDING",
            "event_id": "e2000000-0000-4000-8000-000000000002"}
