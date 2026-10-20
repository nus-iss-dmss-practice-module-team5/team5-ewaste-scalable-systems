from copy import deepcopy
from pathlib import Path

from matcher.contract import hashes, loads

FIXTURES = Path(__file__).parent / "fixtures"


def fixture(name="M-F01"):
    return loads((FIXTURES / (name + ".json")).read_bytes())


def frozen(value):
    value = deepcopy(value)
    value["profile_snapshot_hash"], value["input_hash"] = hashes(value)
    return value


def event():
    context = fixture()["input"]
    return {"event_id": context["trigger_id"], "event_type": "RequestSubmitted", "schema_version": 1,
            "command_id": "c0000000-0000-4000-8000-000000000001", "sequence_in_command": 1,
            "occurred_at": context["batch"]["submitted_at"], "correlation_id": context["correlation_id"],
            **{key: context["batch"][key] for key in ("batch_id", "batch_version", "claim_epoch")},
            "data": {key: context["batch"][key] for key in ("organization_id", "submitted_at", "category", "quantity",
                     "estimated_weight_kg", "condition_rating", "is_data_bearing", "zone", "collection_deadline")}}


def committed(context, output, replay=False):
    return {"disposition": "COMMITTED", "run_id": context["run_id"], "correlation_id": context["correlation_id"],
            "replay": replay, **{key: output[key] for key in ("decision_id", "batch_id", "claim_epoch", "outcome", "evaluated_count", "eligible_count")},
            "batch_status": "MATCHED" if output["outcome"] == "MATCHED" else "SUBMITTED",
            "committed_batch_version": context["batch"]["batch_version"] + (output["outcome"] == "MATCHED")}
