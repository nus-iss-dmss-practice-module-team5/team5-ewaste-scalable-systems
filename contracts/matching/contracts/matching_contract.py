"""EWCSB-2 v1 draft type/interface contract. NOT a matcher implementation.

Boundary values retain exact string representations. Runtime parsing, validation,
hashing, transport, and persistence belong outside this module. ConfigVersion is
a positive SIGNED BIGINT decimal string; Epoch is positive UINT64. API timestamps
use UTC with six fractional digits. Only MatchingStrategy.evaluate is the
pure worker interface; it must perform no I/O and read no clock or randomness.
"""
from typing import Literal, Protocol, TypedDict

UUID = str
SHA256 = str
Timestamp = str
DecimalKg = str
ConfigVersion = str  # 1..9223372036854775807, no leading zeroes
Epoch = str  # 1..18446744073709551615, no leading zeroes
Category = Literal['ICT_EQUIPMENT', 'LARGE_APPLIANCE', 'BATTERIES', 'CONSUMER_ELECTRONICS']
Condition = Literal['FUNCTIONAL', 'REPAIRABLE', 'END_OF_LIFE']
Zone = Literal['NORTH', 'SOUTH', 'EAST', 'WEST', 'CENTRAL']
TriggerType = Literal['REQUEST_SUBMITTED', 'EXPLICIT_RUN']
RuleId = Literal['M1', 'M2', 'M3', 'M4', 'M5']
ReasonCode = Literal['ELIGIBLE', 'CATEGORY_UNSUPPORTED', 'CAPABILITY_UNSUPPORTED',
                     'INSUFFICIENT_CAPACITY', 'CAPACITY_UNAVAILABLE',
                     'OUT_OF_SERVICE_ZONE', 'DEADLINE_UNACHIEVABLE']


class BatchInput(TypedDict):
    batch_id: UUID
    organization_id: str  # Existing VARCHAR(32), not UUID-only
    batch_version: int  # 1..4294967295
    claim_epoch: Epoch
    status: Literal['SUBMITTED']
    submitted_at: Timestamp
    category: Category
    quantity: int  # 1..100000
    estimated_weight_kg: DecimalKg  # 0.10..50000.00, exactly two places
    condition_rating: Condition
    is_data_bearing: bool
    zone: Zone
    collection_deadline: Timestamp


class Profile(TypedDict):
    is_active: bool
    version: ConfigVersion


class CategoryCapability(TypedDict):
    id: UUID
    category: Category
    accepted_conditions: list[Condition]
    supports_data_bearing: bool
    is_active: bool
    capacity_pool_id: UUID
    version: ConfigVersion


class CapacityPool(TypedDict):
    id: UUID
    pool_code: str
    total_kg: DecimalKg
    reserved_kg: DecimalKg
    is_active: bool
    version: ConfigVersion


class ServiceZone(TypedDict):
    id: UUID
    zone: Zone
    minimum_lead_minutes: int  # 0..4294967295
    is_active: bool
    version: ConfigVersion


class OrganisationInput(TypedDict):
    recycler_org_id: str
    organisation_type: Literal['PROCESSING_FACILITY']
    organisation_status: Literal['ACTIVE']
    profile: Profile | None
    category_capabilities: list[CategoryCapability]
    capacity_pools: list[CapacityPool]
    service_zones: list[ServiceZone]


class RulePredicates(TypedDict):
    M1: Literal['ACTIVE_EXACT_CATEGORY']
    M2: Literal['ACTIVE_CAPABILITY_AND_PROFILE_CONDITION_AND_DATA']
    M3: Literal['OWNED_ACTIVE_POOL_FULL_WEIGHT']
    M4: Literal['ACTIVE_EXACT_ZONE']
    M5: Literal['EVALUATION_BEFORE_DEADLINE_AND_LEAD_ARRIVAL_AT_OR_BEFORE_DEADLINE']


class RulesJson(TypedDict):
    categories: list[Category]
    conditions: list[Condition]
    zones: list[Zone]
    weight_scale: Literal[2]
    rule_ids: list[RuleId]
    predicates: RulePredicates
    failure_precedence: list[RuleId]
    ranking: Literal[False]


class RuleSet(TypedDict):
    rule_set_id: UUID
    rule_set_version: Literal['binary-v1']
    rules_json: RulesJson  # Exact draft shape in MatchingInput.v1.schema.json


class MatchingInputV1(TypedDict):
    contract_version: Literal[1]
    run_id: UUID
    decision_id: UUID
    context_generation: int
    trigger_id: UUID
    trigger_type: TriggerType
    evaluation_at: Timestamp
    correlation_id: str  # Original trace1..128; HTTP transport header is separate
    batch: BatchInput
    rule_set: RuleSet
    organisations: list[OrganisationInput]
    profile_snapshot_hash: SHA256
    input_hash: SHA256


class FailedRule(TypedDict):
    rule_id: RuleId
    reason_code: ReasonCode  # ELIGIBLE forbidden for failed entries by JSON schema


class CandidateEvidence(TypedDict):
    capability_id: UUID | None
    capability_version: ConfigVersion | None
    service_zone_id: UUID | None
    service_zone_version: ConfigVersion | None
    missing_configuration: list[Literal['PROFILE', 'CATEGORY_CAPABILITY', 'CAPACITY_POOL', 'SERVICE_ZONE']]


class CandidateResult(TypedDict):
    recycler_org_id: str
    profile_version: ConfigVersion | None
    category_match: bool
    capability_match: bool
    capacity_available: bool
    zone_match: bool
    deadline_viable: bool
    is_matched: bool
    available_capacity_kg: DecimalKg | None
    capacity_pool_id: UUID | None
    capacity_version: ConfigVersion | None
    minimum_lead_minutes: int | None
    feasible_at: Timestamp | None
    reason_code: ReasonCode
    failed_rules_json: list[FailedRule]
    evidence_json: CandidateEvidence


class MatchingOutputV1(TypedDict):
    contract_version: Literal[1]
    run_id: UUID
    decision_id: UUID
    context_generation: int
    input_hash: SHA256
    profile_snapshot_hash: SHA256
    batch_id: UUID
    batch_version: int
    claim_epoch: Epoch
    rule_set_id: UUID
    evaluation_at: Timestamp
    candidates: list[CandidateResult]
    outcome: Literal['MATCHED', 'NO_MATCH']
    primary_reason: Literal['ELIGIBLE_EXISTS', 'NO_ELIGIBLE_ORGANISATION', 'NO_APPROVED_ORGANISATION']
    evaluated_count: int
    eligible_count: int


class MatchingStrategy(Protocol):
    """Registry resolves binary-v1; unsupported rules fail, never silently fall back.

    The adapter validates and freezes input. evaluate must not mutate it. IDs,
    generation, hashes and evaluation_at are echoed, never generated by Python.
    No database/Kafka/Redis access, no wall-clock or randomness, no capacity
    reservation, ranking, claim, collector selection, or lifecycle writes.
    """

    def evaluate(self, snapshot: MatchingInputV1) -> MatchingOutputV1: ...
