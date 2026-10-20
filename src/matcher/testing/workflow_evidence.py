"""EWCSB-1..4 evidence against the existing disposable Go/MySQL/Kafka stack.

Run through scripts/test-matcher.sh --workflow-evidence, never against Azure.
Business rows are created via HTTP; SQL supplies matching configuration only.
"""
import concurrent.futures
import json
import os
from pathlib import Path
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

from confluent_kafka import Consumer, TopicPartition

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tests"))
from test_persistence import PersistenceTests, eventually  # noqa: E402
from matcher.contract import loads, validate  # noqa: E402


EVIDENCE = Path(os.environ["WORKFLOW_EVIDENCE_DIR"])
TOPICS = {"RequestSubmitted": "ewaste.batch.events", "MatchingCompleted": "ewaste.batch.events",
          "ClaimConfirmed": "ewaste.claim.events", "CollectorAssigned": "batch.collector.assigned",
          "CollectionCompleted": "batch.collection.completed", "CollectionFailed": "batch.collection.failed"}


def write(name, value):
    (EVIDENCE / name).write_text(json.dumps(value, indent=2, default=str) + "\n")


class Evidence(PersistenceTests):
    def sql(self, query, args=()):
        with self.db.cursor() as cursor:
            expanded = cursor.mogrify(query, args)
        with (EVIDENCE / "queries.sql").open("a") as log:
            log.write(expanded.rstrip(";") + ";\n")
        result = super().sql(query, args)
        with (EVIDENCE / "query-results.jsonl").open("a") as log:
            log.write(json.dumps({"query": expanded, "result": result}, default=str) + "\n")
        return result

    def request(self, method, path, actor, body=None, key=None, version=None, expected=200):
        headers = {"Content-Type": "application/json", "X-Correlation-ID": "EWCSB-evidence-" + (key or actor)}
        if actor in self.tokens:
            headers["Authorization"] = "Bearer " + self.tokens[actor]
        if key:
            headers["Idempotency-Key"] = key
        if version is not None:
            headers["If-Match-Version"] = str(version)
        request = urllib.request.Request(self.url + path, method=method, headers=headers,
                                         data=json.dumps(body).encode() if body is not None else None)
        start = time.time_ns()
        try:
            response = urllib.request.urlopen(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status, result = response.status, json.load(response)
        record = {"method": method, "path": path, "actor": actor, "key": key, "version": version,
                  "started_ns": start, "finished_ns": time.time_ns(), "status": status}
        if path != "/api/v1/auth/login":
            record.update(request=body, response=result)
        with self.http_lock, (EVIDENCE / "http.jsonl").open("a") as log:
            log.write(json.dumps(record) + "\n")
        if expected is not None:
            self.assertEqual(status, expected, record)
        return status, result

    def login(self):
        self.tokens = {}
        self.http_lock = threading.Lock()
        for actor in ("donor1", "recycler1", "recycler2", "collector1", "collector2"):
            _, result = self.request("POST", "/api/v1/auth/login", actor,
                                     {"email": actor + "@ewaste.test", "password": "TestOnly#2026!"})
            self.tokens[actor] = result["access_token"]

    def batch(self, batch_id):
        return self.sql("SELECT * FROM ewaste_batches WHERE id=%s", (batch_id,))[0]

    def create_matched(self, label):
        body = {"category": "ICT_EQUIPMENT", "quantity": 2, "estimated_weight_kg": 10,
                "condition_rating": "REPAIRABLE", "is_data_bearing": True, "zone": "NORTH",
                "collection_deadline": (datetime.now(timezone.utc) + timedelta(hours=72)).isoformat(),
                "notes": "workflow-evidence:" + label}
        key = "workflow-create-" + label
        _, created = self.request("POST", "/api/v1/batches", "donor1", body, key, expected=201)
        _, replay = self.request("POST", "/api/v1/batches", "donor1", body, key, expected=201)
        self.assertEqual(created["data"], replay["data"])
        batch_id = created["data"]["batch_id"]
        _, edited = self.request("PATCH", f"/api/v1/batches/{batch_id}", "donor1", body,
                                 "workflow-edit-" + label, created["data"]["version"])
        version = edited["data"]["version"]
        submit_path = f"/api/v1/batches/{batch_id}/submit"
        _, submitted = self.request("POST", submit_path, "donor1", {}, "workflow-submit-" + label, version)
        _, replay = self.request("POST", submit_path, "donor1", {}, "workflow-submit-" + label, version)
        self.assertEqual(submitted["data"], replay["data"])
        eventually(lambda: self.sql("SELECT id FROM ewaste_batches WHERE id=%s AND status='MATCHED'", (batch_id,)))
        for actor in ("recycler1", "recycler2"):
            self.request("GET", f"/api/v1/opportunities/{batch_id}", actor)
        return batch_id

    def claim(self, batch_id, actor, version, key, expected=200):
        return self.request("POST", f"/api/v1/batches/{batch_id}/claim", actor,
                            {"expected_version": version, "claim_epoch": "1"}, key, version, expected)

    def select(self, batch_id, actor, scope, key):
        version = self.batch(batch_id)["version"]
        body = {"expected_version": version, "claim_epoch": "1", "collector_scope_id": scope}
        path = f"/api/v1/batches/{batch_id}/assignments"
        _, selected = self.request("POST", path, actor, body, key, version, 201)
        _, replay = self.request("POST", path, actor, body, key, version, 201)
        self.assertEqual(selected["data"], replay["data"])
        return selected["data"]["assignment_id"]

    def snapshot(self, label, batches):
        rows = {}
        for table in ("ewaste_batches", "matching_decisions", "matched_results", "batch_claims",
                      "capacity_reservations", "batch_assignments", "batch_handoffs", "assignment_actions",
                      "batch_audit_events", "command_idempotency", "event_outbox"):
            column = "id" if table == "ewaste_batches" else "batch_id"
            rows[table] = self.sql(f"SELECT * FROM {table} WHERE {column} IN (%s,%s) ORDER BY {column}", batches)
        write(label + "-rows.json", rows)
        return rows

    def prepare(self):
        # Existing integration tests already seeded PROC-001; give both seeded
        # recyclers valid, separate local pools without inventing business history.
        for n in (1, 2):
            org, pool = f"PROC-00{n}", f"e1230000-0000-4000-8000-{n:012d}"
            self.sql("""INSERT INTO recycler_matching_profiles VALUES(%s,1,1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))
                     ON DUPLICATE KEY UPDATE is_active=1,version=version+1""", (org,))
            self.sql("""INSERT INTO recycler_capacity_pools VALUES(%s,%s,'EVIDENCE',1000,0,1,1,UTC_TIMESTAMP(6))""", (pool, org))
            self.sql("""INSERT INTO recycler_category_capabilities VALUES(%s,%s,'ICT_EQUIPMENT','["REPAIRABLE"]',1,1,%s,1,UTC_TIMESTAMP(6))
                     ON DUPLICATE KEY UPDATE capacity_pool_id=VALUES(capacity_pool_id),is_active=1,
                     accepted_conditions_json=VALUES(accepted_conditions_json),supports_data_bearing=1,version=version+1""",
                     (f"e1231000-0000-4000-8000-{n:012d}", org, pool))
            self.sql("""INSERT INTO recycler_service_zones VALUES(%s,%s,'NORTH',0,1,1,UTC_TIMESTAMP(6))
                     ON DUPLICATE KEY UPDATE minimum_lead_minutes=0,is_active=1,version=version+1""",
                     (f"e1232000-0000-4000-8000-{n:012d}", org))
        _, _, observations = self.start_worker("workflow-evidence")
        self.request("POST", "/api/v1/batches", "donor1", {"quantity": 0}, "workflow-invalid-draft", expected=400)
        race_batch = self.create_matched("fcfs")
        version = self.batch(race_batch)["version"]
        self.claim(race_batch, "donor1", version, "workflow-forbidden-claim", expected=403)
        barrier = threading.Barrier(2)

        def contender(actor):
            barrier.wait(timeout=10)
            status, response = self.claim(race_batch, actor, version, "workflow-race-" + actor, expected=None)
            return {"actor": actor, "status": status, "response": response}

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
            results = list(executor.map(contender, ["recycler1", "recycler2"]))
        requests = [loads(line) for line in (EVIDENCE / "http.jsonl").read_text().splitlines()]
        requests = [r for r in requests if (r.get("key") or "").startswith("workflow-race-")]
        overlap = max(r["started_ns"] for r in requests) < min(r["finished_ns"] for r in requests)
        write("fcfs.json", {"batch_id": race_batch, "barrier_participants": 2,
                            "request_intervals_overlap": overlap, "requests": requests, "results": results})
        self.assertTrue(overlap, "the two claim requests must overlap in time")
        self.assertEqual(sorted(r["status"] for r in results), [200, 409])
        winner = next(r for r in results if r["status"] == 200)
        _, replay = self.claim(race_batch, winner["actor"], version, "workflow-race-" + winner["actor"])
        self.assertEqual(winner["response"]["data"], replay["data"])
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM batch_claims WHERE batch_id=%s AND claim_status='ACCEPTED'", (race_batch,))[0]["n"], 1)
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM capacity_reservations WHERE batch_id=%s", (race_batch,))[0]["n"], 1)

        failed_batch = self.create_matched("failed-pickup")
        self.claim(failed_batch, "recycler1", self.batch(failed_batch)["version"], "workflow-recovery-claim")
        assignment = self.select(failed_batch, "collector1", "e1070000-0000-4000-8000-000000000001", "workflow-first-assignment")
        version = self.batch(failed_batch)["version"]
        body = {"failure_reason": "DONOR_UNAVAILABLE", "observed_details": "No custody transfer"}
        path = f"/api/v1/assignments/{assignment}/fail"
        _, failed = self.request("POST", path, "collector1", body, "workflow-failed-pickup", version)
        _, replay = self.request("POST", path, "collector1", body, "workflow-failed-pickup", version)
        self.assertEqual(failed["data"], replay["data"])
        self.assertEqual(self.batch(failed_batch)["status"], "FAILED_COLLECTION")
        batches = [race_batch, failed_batch]
        self.snapshot("before-recovery", batches)
        write("state.json", {"batches": batches, "failed_assignment": assignment})
        (EVIDENCE / "failed-assignment.txt").write_text(assignment + "\n")
        write("worker-observations.json", observations)

    def finish(self):
        state = loads((EVIDENCE / "state.json").read_bytes())
        batches = state["batches"]
        batch_id = batches[1]
        self.assertEqual(self.batch(batch_id)["status"], "APPROVED")
        self.assertIsNone(self.batch(batch_id)["current_assignment_id"])
        self.snapshot("after-recovery", batches)
        replacement = self.select(batch_id, "collector2", "e1070000-0000-4000-8000-000000000003", "workflow-replacement-assignment")
        body = {"pickup_occurred_at": datetime.now(timezone.utc).isoformat(), "donor_representative_name": "Evidence Donor",
                "actual_item_count": 2, "verification_hash": "a" * 64}
        path = f"/api/v1/assignments/{replacement}/handoff"
        version = self.batch(batch_id)["version"]
        _, completed = self.request("POST", path, "collector2", body, "workflow-completed-handoff", version)
        _, replay = self.request("POST", path, "collector2", body, "workflow-completed-handoff", version)
        self.assertEqual(completed["data"], replay["data"])
        self.assertEqual(self.batch(batch_id)["status"], "COLLECTED")
        history = self.sql("SELECT assignment_status,previous_assignment_id FROM batch_assignments WHERE batch_id=%s ORDER BY assignment_sequence", (batch_id,))
        self.assertEqual([h["assignment_status"] for h in history], ["FAILED", "COMPLETED"])
        self.assertEqual(history[1]["previous_assignment_id"], state["failed_assignment"])
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM batch_handoffs WHERE batch_id=%s", (batch_id,))[0]["n"], 2)
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM batch_audit_events WHERE batch_id=%s AND event_type='CollectionRecoveryApproved'", (batch_id,))[0]["n"], 1)
        eventually(lambda: self.sql("SELECT COUNT(*) AS n FROM event_outbox WHERE batch_id IN (%s,%s) AND publish_state<>'PUBLISHED'", batches)[0]["n"] == 0)
        rows = self.snapshot("final", batches)
        self.assertEqual(len(rows["event_outbox"]), 10)
        self.assertTrue(all(c["state"] == "COMPLETED" for c in rows["command_idempotency"]))
        for batch in batches:
            ordered = self.sql("SELECT aggregate_version,sequence_in_command,published_at FROM event_outbox WHERE batch_id=%s ORDER BY aggregate_version,sequence_in_command", (batch,))
            self.assertEqual([r["published_at"] for r in ordered], sorted(r["published_at"] for r in ordered))
        self.verify_kafka(rows["event_outbox"])
        write("assertions.json", {"EWCSB-1": "PASS: create/edit/submit, validation, replay, audit and command persistence",
                                  "EWCSB-2": "PASS: real worker decisions, results, source IDs and Kafka contract assertions",
                                  "EWCSB-3": "PASS: concurrent HTTP 200/409, one accepted claim/reservation; replay",
                                  "EWCSB-4": "PASS: fail, explicit service recovery, replacement, handoff and replay",
                                  "limitations": ["Recovery service invoked explicitly; no automatic recovery trigger exists.",
                                                  "Kafka ordering asserted within a topic/partition; no cross-topic ordering guarantee.",
                                                  "JMeter omitted as requested; HTTP clients use a two-party barrier."]})

    def verify_kafka(self, rows):
        expected = {r["event_id"]: r for r in rows}
        consumer = Consumer({"bootstrap.servers": self.broker, "group.id": "workflow-evidence-inspector", "enable.auto.commit": False})
        self.addCleanup(consumer.close)
        partitions = [TopicPartition(topic, 0, 0) for topic in sorted(set(TOPICS.values()))]
        ends = {p.topic: consumer.get_watermark_offsets(p, timeout=10)[1] for p in partitions}
        consumer.assign(partitions)
        seen, records, last_version = set(), [], {}
        deadline = time.monotonic() + 40
        while time.monotonic() < deadline:
            message = consumer.poll(.2)
            if message is not None and not message.error():
                event = loads(message.value())
                if event.get("batch_id") in {r["batch_id"] for r in rows}:
                    validate(event["event_type"], event)
                    row = expected[event["event_id"]]
                    self.assertEqual(event, loads(row["payload_json"]))
                    self.assertEqual(message.key().decode(), row["batch_id"])
                    self.assertEqual(message.topic(), TOPICS[event["event_type"]])
                    self.assertEqual(event["correlation_id"], row["correlation_id"])
                    stream = (message.topic(), message.partition(), event["batch_id"])
                    order = (event["batch_version"], event["sequence_in_command"])
                    self.assertGreaterEqual(order, last_version.get(stream, (0, 0)))
                    last_version[stream] = order
                    seen.add(event["event_id"])
                    records.append({"topic": message.topic(), "partition": message.partition(), "offset": message.offset(),
                                    "key": message.key().decode(), "event": event})
            if all(p.offset >= ends[p.topic] for p in consumer.position(partitions)):
                break
        write("kafka-records.json", records)
        self.assertTrue(all(p.offset >= ends[p.topic] for p in consumer.position(partitions)),
                        "Kafka inspection must reach the captured end of every partition")
        self.assertEqual(seen, set(expected), "every persisted outbox event must be observed on Kafka")
        self.assertEqual({r["event"]["event_type"] for r in records}, set(TOPICS))


if __name__ == "__main__":
    Evidence.setUpClass()
    suite = Evidence()
    try:
        suite.login()
        {"prepare": suite.prepare, "finish": suite.finish}[sys.argv[1]]()
        print(f"PASS workflow evidence {sys.argv[1]}", flush=True)
    except Exception:
        if (EVIDENCE / "state.json").exists():
            suite.snapshot("failure", loads((EVIDENCE / "state.json").read_bytes())["batches"])
        raise
    finally:
        try:
            if not suite.doCleanups():
                raise AssertionError("evidence worker/consumer cleanup failed")
        finally:
            Evidence.tearDownClass()
