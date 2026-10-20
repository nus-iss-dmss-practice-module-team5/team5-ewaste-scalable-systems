"""Strict parsing and offline validation of the approved v1 wire contracts."""
import copy
import hashlib
import json
from datetime import datetime, timedelta
from decimal import Decimal
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker, ValidationError
from referencing import Registry, Resource

CONTRACTS = Path(__file__).resolve().parents[3] / "contracts" / "matching"


class ContractError(ValueError):
    def __init__(self, code="INVALID_CONTRACT"):
        self.code = code
        super().__init__(code)


def require(condition, code="INVALID_CONTRACT"):
    if not condition:
        raise ContractError(code)


def _pairs(pairs):
    obj = {}
    for key, value in pairs:
        require(key not in obj)
        obj[key] = value
    return obj


def _invalid_constant(_):
    raise ContractError()


def loads(raw):
    try:
        value = json.loads(raw, object_pairs_hook=_pairs, parse_constant=_invalid_constant)
        _unicode_values(value)
        return value
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise ContractError() from exc


def _unicode_values(value):
    if isinstance(value, str):
        value.encode("utf-8")  # Reject escaped, unpaired Unicode surrogates too.
    elif isinstance(value, dict):
        for key, child in value.items():
            _unicode_values(key)
            _unicode_values(child)
    elif isinstance(value, list):
        for child in value:
            _unicode_values(child)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False, allow_nan=False).encode("utf-8")


def exact_types(value):
    # JSON Schema treats 1.0 as an integer; the wire contract requires JSON integers.
    require(not isinstance(value, float))
    if isinstance(value, dict):
        for child in value.values():
            exact_types(child)
    elif isinstance(value, list):
        for child in value:
            exact_types(child)


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def timestamp(value):
    try:
        parsed = datetime.strptime(value, "%Y-%m-%dT%H:%M:%S.%fZ")
        require(parsed.year >= 1000 and len(value) == 27)
        return parsed
    except (ValueError, TypeError) as exc:
        raise ContractError() from exc


def stamp(value):
    return value.isoformat(timespec="microseconds") + "Z"


_schemas = {}
_registry = Registry()
for _path in CONTRACTS.glob("*/*.schema.json"):
    _schema = loads(_path.read_bytes())
    _schemas[_path.name] = _schema
    _registry = _registry.with_resource(_path.name, Resource.from_contents(_schema))


def validate(name, value):
    try:
        Draft202012Validator(_schemas[name + ".v1.schema.json"], registry=_registry,
                             format_checker=FormatChecker()).validate(value)
    except (ValidationError, ValueError, TypeError, RecursionError) as exc:
        raise ContractError() from exc


def normalize(snapshot):
    """Copy before sorting; identities and frozen clock are never generated here."""
    result = copy.deepcopy(snapshot)
    result["organisations"].sort(key=lambda o: o["recycler_org_id"])
    for org in result["organisations"]:
        org["category_capabilities"].sort(key=lambda c: (c["category"], c["id"]))
        org["capacity_pools"].sort(key=lambda p: p["id"])
        org["service_zones"].sort(key=lambda z: (z["zone"], z["id"]))
        for cap in org["category_capabilities"]:
            cap["accepted_conditions"].sort()
    return result


def hashes(snapshot):
    normalized = normalize(snapshot)
    fields = ("contract_version", "batch", "rule_set", "evaluation_at", "organisations")
    return digest(normalized["organisations"]), digest({k: normalized[k] for k in fields})


def _unique(items, field):
    require(len({item[field] for item in items}) == len(items), "DATA_INTEGRITY_ERROR")


def validate_input(snapshot):
    require(isinstance(snapshot, dict))
    exact_types(snapshot)
    require(isinstance(snapshot.get("rule_set"), dict))
    require(snapshot["rule_set"].get("rule_set_version") == "binary-v1",
            "UNSUPPORTED_RULE_SET")
    validate("MatchingInput", snapshot)
    require(type(snapshot["contract_version"]) is int)
    batch = snapshot["batch"]
    require(str(int(batch["claim_epoch"])) == batch["claim_epoch"])
    require(int(batch["claim_epoch"]) <= 2**64 - 1)
    require(format(Decimal(batch["estimated_weight_kg"]), ".2f") == batch["estimated_weight_kg"])
    require(Decimal("0.10") <= Decimal(batch["estimated_weight_kg"]) <= Decimal("50000.00"))
    submitted = timestamp(batch["submitted_at"])
    deadline = timestamp(batch["collection_deadline"])
    try:
        require(submitted + timedelta(hours=48) <= deadline <= submitted + timedelta(days=90))
    except OverflowError as exc:
        raise ContractError() from exc
    timestamp(snapshot["evaluation_at"])
    orgs = snapshot["organisations"]
    _unique(orgs, "recycler_org_id")
    # Foreign pools and duplicate identifiers across owners are integrity errors,
    # while an absent pool is missing configuration and must fail closed.
    owners = {}
    seen_ids = set()
    for org in orgs:
        require(all(33 <= ord(c) <= 126 for c in org["recycler_org_id"]))
        for pool in org["capacity_pools"]:
            require(all(33 <= ord(c) <= 126 for c in pool["pool_code"]))
            for field in ("total_kg", "reserved_kg"):
                require(format(Decimal(pool[field]), ".2f") == pool[field])
            require(pool["id"] not in owners, "DATA_INTEGRITY_ERROR")
            owners[pool["id"]] = org["recycler_org_id"]
    for org in orgs:
        for collection, unique_field in (("category_capabilities", "category"),
                                          ("capacity_pools", "pool_code"),
                                          ("service_zones", "zone")):
            items = org[collection]
            _unique(items, "id")
            _unique(items, unique_field)
            for item in items:
                require(item["id"] not in seen_ids, "DATA_INTEGRITY_ERROR")
                seen_ids.add(item["id"])
                require(str(int(item["version"])) == item["version"])
                require(int(item["version"]) <= 2**63 - 1)
        if org["profile"] is not None:
            require(str(int(org["profile"]["version"])) == org["profile"]["version"])
            require(int(org["profile"]["version"]) <= 2**63 - 1)
        for pool in org["capacity_pools"]:
            require(Decimal(pool["reserved_kg"]) <= Decimal(pool["total_kg"]), "DATA_INTEGRITY_ERROR")
        for cap in org["category_capabilities"]:
            owner = owners.get(cap["capacity_pool_id"])
            require(owner is None or owner == org["recycler_org_id"], "DATA_INTEGRITY_ERROR")
    normalized = normalize(snapshot)
    profile_hash, input_hash = hashes(normalized)
    require(profile_hash == snapshot["profile_snapshot_hash"] and input_hash == snapshot["input_hash"])
    return normalized
