"""Real container replicas; no worker doubles or direct worker execution in tests.

Run through scripts/test-worker-services.sh in its isolated, seeded environment.
The script kills one replica per group between phases, then restores both apps.
"""
import json
import os
from pathlib import Path
from unittest.mock import patch

import pytest
from confluent_kafka import Consumer, KafkaException, Producer, TopicPartition
from confluent_kafka.admin import AdminClient

import test_analytics_persistence as journeys
import test_persistence as persistence
from matcher.contract import canonical, loads

pytestmark = pytest.mark.skipif(os.environ.get("WORKER_SERVICES_TEST") != "1",
                                reason="requires the isolated two-app Compose acceptance run")
TOPIC = "ewaste.batch.events"
GROUPS = ("matching-worker-v1", "analytics-processing-v1")


def membership(admin, expected_members):
    try:
        descriptions = {group: future.result(10)
                        for group, future in admin.describe_consumer_groups(list(GROUPS)).items()}
        result = {}
        for group, description in descriptions.items():
            assignments = [[p.partition for p in member.assignment.topic_partitions if p.topic == TOPIC]
                           for member in description.members]
            if (len(assignments) != expected_members or not all(assignments)
                    or sorted(p for assignment in assignments for p in assignment) != [0, 1, 2]):
                return None
            result[group] = assignments
        return result
    except KafkaException:
        return None


def snapshot(helper, batch):
    result = helper.sql("SELECT status,version FROM ewaste_batches WHERE id=%s", (batch,))[0]
    for table in ("matching_decisions", "matched_results", "batch_impact_metrics", "batch_anomalies",
                  "command_idempotency", "batch_audit_events", "event_outbox"):
        result[table] = helper.sql("SELECT COUNT(*) AS n FROM " + table + " WHERE batch_id=%s", (batch,))[0]["n"]
    for event_type in ("MatchingCompleted", "RequestCompleted"):
        result[event_type] = helper.sql("SELECT COUNT(*) AS n FROM event_outbox WHERE batch_id=%s AND event_type=%s",
                                        (batch, event_type))[0]["n"]
    assert result["status"] == "COMPLETED"
    assert result["matching_decisions"] == result["batch_impact_metrics"] == 1
    assert result["MatchingCompleted"] == result["RequestCompleted"] == 1
    return result


def test_two_apps_replay_without_duplicate_durable_effects():
    evidence = Path(os.environ["ANALYTICS_TEST_EVIDENCE"])
    phase = os.environ["WORKER_SERVICES_PHASE"]
    expected_members = 1 if phase == "takeover" else 2
    common = {"bootstrap.servers": os.environ["MATCHER_TEST_BROKER"]}
    admin = AdminClient(common)
    assignments = persistence.eventually(lambda: membership(admin, expected_members), seconds=90)
    # Existing journey helper supplies real HTTP users and SQL assertions. Its
    # in-process matcher is disabled: only the separate application containers run.
    helper = journeys.AnalyticsPersistenceTests()
    try:
        with patch.object(persistence.PersistenceTests, "start_worker") as in_process_worker:
            helper.setUp()
        assert in_process_worker.call_count == 1
        if phase == "initial":
            receipt = {"actual_category": "ICT_EQUIPMENT", "actual_item_count": 5, "actual_weight_kg": "12.00"}
            treatment = {"reused_kg": "2.00", "recycled_kg": "9.00", "disposed_kg": "1.00"}
            analytics_events = [helper.recycled("workers-" + str(i), receipt, treatment) for i in range(3)]
            sources, baseline = [], {}
            for event in analytics_events:
                batch = event["batch_id"]
                persistence.eventually(lambda: helper.helper.sql(
                    "SELECT id FROM ewaste_batches WHERE id=%s AND status='COMPLETED'", (batch,)), seconds=90)
                sources.extend(loads(row["payload_json"]) for row in helper.sql(
                    "SELECT payload_json FROM event_outbox WHERE batch_id=%s AND event_type IN ('RequestSubmitted','RecyclingCompleted')",
                    (batch,)))
                baseline[batch] = snapshot(helper, batch)
            helper.write("worker-sources.json", sources)
            helper.write("worker-baseline.json", baseline)
        else:
            sources = json.loads((evidence / "worker-sources.json").read_text())
            baseline = json.loads((evidence / "worker-baseline.json").read_text())

        producer = Producer({**common, "enable.idempotence": True, "acks": "all"})
        offsets, errors = {}, []
        def delivered(error, message):
            if error:
                errors.append(str(error))
            else:
                offsets[message.partition()] = max(offsets.get(message.partition(), 0), message.offset() + 1)
        # Deliberately duplicate each immutable source on every partition to race
        # consumers against the same command. Normal producers still key by batch.
        for event in sources:
            for partition in range(3):
                producer.produce(TOPIC, partition=partition, key=event["batch_id"], value=canonical(event),
                                 on_delivery=delivered)
        assert producer.flush(15) == 0
        assert not errors
        assert set(offsets) == {0, 1, 2}
        committed = {}
        for group in GROUPS:
            inspector = Consumer({**common, "group.id": group, "enable.auto.commit": False})
            try:
                def caught_up():
                    values = inspector.committed([TopicPartition(TOPIC, p) for p in offsets], timeout=10)
                    current = {p.partition: p.offset for p in values}
                    return current if all(current[p] >= target for p, target in offsets.items()) else None
                committed[group] = persistence.eventually(caught_up, seconds=90)
            finally:
                inspector.close()
        actual = {batch: snapshot(helper, batch) for batch in baseline}
        assert actual == baseline
        helper.write(phase + "-worker-evidence.json", {
            "phase": phase, "consumer_assignments": assignments, "committed_next_offsets": committed,
            "duplicate_records": len(sources) * 3, "batches": actual,
            "api_boundary": "real Go API and MySQL; workers use authenticated HTTP only",
        })
    finally:
        helper.doCleanups()
