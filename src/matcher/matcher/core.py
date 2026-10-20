"""binary-v1: pure exact-decimal M1–M5 evaluation of a frozen snapshot."""
from datetime import timedelta
from decimal import Decimal

from .contract import ContractError, stamp, timestamp, validate, validate_input


def _candidate(batch, org, evaluation):
    profile = org["profile"]
    cap = next((c for c in org["category_capabilities"] if c["category"] == batch["category"]), None)
    pool = next((p for p in org["capacity_pools"] if cap and p["id"] == cap["capacity_pool_id"]), None)
    zone = next((z for z in org["service_zones"] if z["zone"] == batch["zone"]), None)
    active_cap = cap is not None and cap["is_active"]
    available = Decimal(pool["total_kg"]) - Decimal(pool["reserved_kg"]) if pool and pool["is_active"] else None
    feasible = None
    if zone and zone["is_active"]:
        try:
            feasible = evaluation + timedelta(minutes=zone["minimum_lead_minutes"])
        except OverflowError as exc:
            raise ContractError("DATA_INTEGRITY_ERROR") from exc
    deadline = timestamp(batch["collection_deadline"])
    flags = [
        active_cap,
        bool(profile and profile["is_active"] and active_cap
             and batch["condition_rating"] in cap["accepted_conditions"]
             and (not batch["is_data_bearing"] or cap["supports_data_bearing"])),
        bool(pool and pool["is_active"] and available >= Decimal(batch["estimated_weight_kg"])),
        bool(zone and zone["is_active"]),
        bool(zone and zone["is_active"] and evaluation < deadline and feasible <= deadline),
    ]
    reasons = ["CATEGORY_UNSUPPORTED", "CAPABILITY_UNSUPPORTED",
               "INSUFFICIENT_CAPACITY" if pool and pool["is_active"] else "CAPACITY_UNAVAILABLE",
               "OUT_OF_SERVICE_ZONE", "DEADLINE_UNACHIEVABLE"]
    failures = [{"rule_id": f"M{i + 1}", "reason_code": reasons[i]}
                for i, passed in enumerate(flags) if not passed]
    return {
        "recycler_org_id": org["recycler_org_id"],
        "profile_version": profile["version"] if profile else None,
        **dict(zip(("category_match", "capability_match", "capacity_available", "zone_match", "deadline_viable"), flags)),
        "is_matched": all(flags),
        "available_capacity_kg": format(available, ".2f") if available is not None else None,
        "capacity_pool_id": pool["id"] if pool else None,
        "capacity_version": pool["version"] if pool else None,
        "minimum_lead_minutes": zone["minimum_lead_minutes"] if zone and zone["is_active"] else None,
        "feasible_at": stamp(feasible) if feasible else None,
        "reason_code": failures[0]["reason_code"] if failures else "ELIGIBLE",
        "failed_rules_json": failures,
        "evidence_json": {
            "capability_id": cap["id"] if cap else None,
            "capability_version": cap["version"] if cap else None,
            "service_zone_id": zone["id"] if zone else None,
            "service_zone_version": zone["version"] if zone else None,
            "missing_configuration": [name for name, value in
                                      (("PROFILE", profile), ("CATEGORY_CAPABILITY", cap),
                                       ("CAPACITY_POOL", pool), ("SERVICE_ZONE", zone)) if value is None],
        },
    }


def evaluate(snapshot):
    snapshot = validate_input(snapshot)
    candidates = [_candidate(snapshot["batch"], org, timestamp(snapshot["evaluation_at"]))
                  for org in snapshot["organisations"]]
    eligible = sum(c["is_matched"] for c in candidates)
    output = {key: snapshot[key] for key in ("contract_version", "run_id", "decision_id",
              "context_generation", "input_hash", "profile_snapshot_hash", "evaluation_at")}
    output.update({key: snapshot["batch"][key] for key in ("batch_id", "batch_version", "claim_epoch")})
    output.update(rule_set_id=snapshot["rule_set"]["rule_set_id"], candidates=candidates,
                  outcome="MATCHED" if eligible else "NO_MATCH", evaluated_count=len(candidates),
                  eligible_count=eligible, primary_reason="ELIGIBLE_EXISTS" if eligible else
                  "NO_ELIGIBLE_ORGANISATION" if candidates else "NO_APPROVED_ORGANISATION")
    validate("MatchingOutput", output)
    return output
