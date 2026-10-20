"""Disposable environment only: real HTTP lifecycle, Kafka and transactional MySQL evidence."""
import json
import os
import secrets
import threading
import time
import unittest
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone
from decimal import Decimal
from pathlib import Path

from confluent_kafka import Consumer, Producer, TopicPartition

import test_persistence as persistence
from matcher.analytics import acknowledgement
from matcher.analytics_worker import AnalyticsFacade, AnalyticsProcessor
from matcher.contract import canonical, loads, validate
from matcher.facade import FacadeError
from matcher.health import Health
from matcher.kafka import KafkaRunner, observe as log_observation

eventually = persistence.eventually


@unittest.skipUnless(os.environ.get("MATCHER_TEST_API") and os.environ.get("ANALYTICS_TEST_EVIDENCE"),
                     "disposable analytics evidence environment not configured")
class AnalyticsPersistenceTests(unittest.TestCase):
    def setUp(self):
        self.evidence = Path(os.environ["ANALYTICS_TEST_EVIDENCE"])
        self.evidence.mkdir(parents=True, exist_ok=True)
        self.helper = persistence.PersistenceTests()
        self.helper.setUpClass()
        self.addCleanup(self.helper.tearDownClass)
        self.addCleanup(self.helper.doCleanups)
        self.url, self.db, self.broker = self.helper.url, self.helper.db, self.helper.broker
        self.tokens = {}
        # Synthetic accounts only, in the freshly migrated disposable database.
        import bcrypt
        password = secrets.token_urlsafe(24)
        hashed = bcrypt.hashpw(password.encode(), bcrypt.gensalt(rounds=4)).decode()
        for email in ("donor1", "recycler1", "collector1", "collector2", "auditor"):
            self.helper.sql("UPDATE users SET password_hash=%s WHERE email=%s", (hashed, email + "@ewaste.test"))
            result = self.http("POST", "/api/v1/auth/login", email,
                               {"email": email + "@ewaste.test", "password": password})
            self.tokens[email] = result["access_token"]
        self.helper.start_worker("analytics-journey-matching-v1")

    def write(self, name, value):
        (self.evidence / name).write_text(json.dumps(value, indent=2, default=str) + "\n")

    def sql(self, query, args=()):
        with self.db.cursor() as cursor:
            expanded = cursor.mogrify(query, args)
        result = self.helper.sql(query, args)
        with (self.evidence / "queries.sql").open("a") as file:
            file.write(expanded.rstrip(";") + ";\n")
        with (self.evidence / "sql-results.jsonl").open("a") as file:
            file.write(json.dumps({"query": expanded, "result": result}, default=str) + "\n")
        return result

    def http(self, method, path, actor, body=None, key=None, version=None, status=200):
        correlation = "analytics-journey-" + (key or actor)
        headers = {"Content-Type": "application/json", "X-Correlation-ID": correlation}
        if actor in self.tokens:
            headers["Authorization"] = "Bearer " + self.tokens[actor]
        if key:
            headers["Idempotency-Key"] = "analytics-e2e-" + key
        if version is not None:
            headers["If-Match-Version"] = str(version)
        request = urllib.request.Request(self.url + path, data=canonical(body) if body is not None else None,
                                         headers=headers, method=method)
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            value = loads(response.read())
        if path != "/api/v1/auth/login":
            with (self.evidence / "api-journeys.jsonl").open("a") as file:
                file.write(json.dumps({"method": method, "path": path, "actor": actor, "correlation_id": correlation,
                                       "idempotency_key": headers.get("Idempotency-Key"), "version": version, "request": body,
                                       "status": response.status, "response": value}) + "\n")
        self.assertEqual(response.status, status, {"path": path, "response": value})
        return value

    def recycled(self, label, receipt, treatment):
        body = {"category": "ICT_EQUIPMENT", "quantity": 5, "estimated_weight_kg": 12,
                "condition_rating": "REPAIRABLE", "is_data_bearing": True, "zone": "NORTH",
                "collection_deadline": (datetime.now(timezone.utc) + timedelta(days=3)).isoformat(),
                "notes": "analytics-evidence:" + label}
        created = self.http("POST", "/api/v1/batches", "donor1", body, label + "-create", status=201)["data"]
        batch = created["batch_id"]; path = "/api/v1/batches/" + batch
        self.http("POST", path + "/submit", "donor1", {}, label + "-submit", created["version"])
        matched = eventually(lambda: self.helper.sql("SELECT version FROM ewaste_batches WHERE id=%s AND status='MATCHED'", (batch,)))[0]
        claimed = self.http("POST", path + "/claim", "recycler1", {"expected_version": matched["version"], "claim_epoch": "1"},
                            label + "-claim", matched["version"])["data"]
        # Claim confirmation now atomically assigns the next collector in the
        # ring. Read that assignment's principal instead of issuing the former
        # manual-selection command, which must conflict once the batch is ASSIGNED.
        assigned = self.sql("""SELECT assignment.id, collector.email
                             FROM batch_assignments AS assignment
                             JOIN users AS collector ON collector.user_id = assignment.collector_user_id
                             WHERE assignment.id=%s""", (claimed["assignment_id"],))[0]
        collector = assigned["email"].split("@", 1)[0]
        self.assertIn(collector, self.tokens)
        self.http("POST", "/api/v1/assignments/" + assigned["id"] + "/accept", collector, {},
                  label + "-accept", claimed["version"])
        handoff_version = self.sql("SELECT version FROM ewaste_batches WHERE id=%s", (batch,))[0]["version"]
        self.http("POST", "/api/v1/assignments/" + assigned["id"] + "/handoff", collector,
                            {"pickup_occurred_at": datetime.now(timezone.utc).isoformat(timespec="microseconds"),
                             "donor_representative_name": "Synthetic Test", "actual_item_count": 5, "verification_hash": "a" * 64},
                            label + "-handoff", handoff_version)
        batch_version = self.sql("SELECT version FROM ewaste_batches WHERE id=%s", (batch,))[0]["version"]
        received = self.http("POST", path + "/receipt", "recycler1", receipt, label + "-receipt", batch_version)
        replay = self.http("POST", path + "/receipt", "recycler1", receipt, label + "-receipt", batch_version)
        self.assertEqual(received["data"], replay["data"])
        treated = self.http("POST", path + "/treatment", "recycler1", treatment, label + "-treatment", received["data"]["version"])
        replay = self.http("POST", path + "/treatment", "recycler1", treatment, label + "-treatment", received["data"]["version"])
        self.assertEqual(treated["data"], replay["data"])
        event = loads(self.sql("SELECT payload_json FROM event_outbox WHERE batch_id=%s AND event_type='RecyclingCompleted'", (batch,))[0]["payload_json"])
        return event

    def start_analytics(self, *, lose_response=False):
        client = AnalyticsFacade(self.url, None, 5, 1048576, local=True,
                                 token_provider=lambda: os.environ["ANALYTICS_SERVICE_TOKEN"])
        observations = []
        lost = []
        original_prepare = client.prepare
        original = client.commit
        def prepare(event):
            result = original_prepare(event)
            with (self.evidence / "analytics-facade.jsonl").open("a") as file:
                file.write(json.dumps({"operation": "prepare", "batch_id": event["batch_id"], "response": result}) + "\n")
            return result
        client.prepare = prepare
        def commit(event, output):
            result = original(event, output)
            with (self.evidence / "analytics-facade.jsonl").open("a") as file:
                file.write(json.dumps({"operation": "commit", "batch_id": event["batch_id"],
                                       "correlation_id": event["correlation_id"], "expected_version": event["aggregate_version"],
                                       "idempotency_key": "analytics-v1:" + event["event_id"],
                                       "request": output, "response": result}) + "\n")
            if lose_response and not lost:
                lost.append(event["batch_id"])
                item = {"observation": "injected_response_loss", "batch_id": event["batch_id"], "event_id": event["event_id"]}
                observations.append(item)
                with (self.evidence / "recovery.jsonl").open("a") as file:
                    file.write(json.dumps(item) + "\n")
                raise FacadeError()
            return result
        client.commit = commit
        health = Health()
        def observer(name, delivery=None, **fields):
            fields.setdefault("consumer_group", "analytics-processing-e2e-v1")
            log_observation(name, delivery, **fields)
            health.observe(name, delivery, **fields)
            item = {"observation": name, **fields}
            if delivery:
                item.update(offset=delivery.record.offset, stage=delivery.stage, retries=delivery.retries,
                            batch_id=delivery.event["batch_id"] if delivery.event else None)
            observations.append(item)
            with (self.evidence / "recovery.jsonl").open("a") as file:
                file.write(json.dumps(item) + "\n")
        processor = AnalyticsProcessor(client, max_bytes=1048576, retry_base=.1, retry_max=.5, observe=observer)
        runner = KafkaRunner({"bootstrap.servers": self.broker}, topic="ewaste.batch.events",
                             group_id="analytics-processing-e2e-v1", offset_reset="earliest", max_poll_ms=300000,
                             session_timeout_ms=6000, workers=1, processor=processor, observer=observer, health=health)
        stop = threading.Event()
        thread = threading.Thread(target=runner.run, args=(stop,), daemon=True); thread.start()
        def finish():
            stop.set(); thread.join(15)
            self.assertFalse(thread.is_alive())
        self.addCleanup(finish)
        return finish, observations, health, client, lost

    def counts(self, batch):
        return {table: self.sql("SELECT COUNT(*) AS n FROM " + table + " WHERE batch_id=%s" + extra, (batch,))[0]["n"]
                for table, extra in (("batch_impact_metrics", ""), ("command_idempotency", " AND command_name='AcknowledgeAnalyticsResult'"),
                                     ("batch_audit_events", " AND event_type='AnalyticsCompleted'"),
                                     ("event_outbox", " AND event_type='RequestCompleted'"))}

    def test_journeys_atomic_failure_restart_response_loss_and_redelivery(self):
        receipt = {"actual_category": "ICT_EQUIPMENT", "actual_item_count": 5, "actual_weight_kg": "12.00"}
        events = [self.recycled("clean", receipt, {"reused_kg": "2.00", "recycled_kg": "9.00", "disposed_kg": "1.00"}),
                  self.recycled("discrepant", {**receipt, "actual_category": "BATTERIES", "actual_item_count": 4, "actual_weight_kg": "10.50"},
                                {"reused_kg": "2.00", "recycled_kg": "7.00", "disposed_kg": "1.00"}),
                  self.recycled("missing", receipt, {}),
                  self.recycled("zero", receipt, {"reused_kg": "0.00", "recycled_kg": "0.00", "disposed_kg": "0.00"})]
        batch = events[0]["batch_id"]
        # Fail at the final outbox insert, after command/result/audit writes: the entire transaction must roll back.
        self.sql("""CREATE TRIGGER analytics_test_failure BEFORE INSERT ON event_outbox FOR EACH ROW
                 BEGIN IF NEW.batch_id = '%s' AND NEW.event_type = 'RequestCompleted' THEN
                 SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='synthetic analytics persistence failure'; END IF; END""" % batch)
        try:
            finish, before, health, _, _ = self.start_analytics()
            eventually(lambda: any(i["observation"] == "delivery_paused" and i.get("batch_id") == batch for i in before))
            self.assertFalse(health.state(ready=True))
            finish()
            self.assertEqual(set(self.counts(batch).values()), {0})
            self.assertEqual(self.sql("SELECT status FROM ewaste_batches WHERE id=%s", (batch,))[0]["status"], "RECYCLED")
            self.write("rollback.json", {"batch_id": batch, "durable_counts": self.counts(batch), "status": "RECYCLED", "ready": False})
        finally:
            self.sql("DROP TRIGGER IF EXISTS analytics_test_failure")
        finish, after, health, client, lost = self.start_analytics(lose_response=True)
        for event in events:
            eventually(lambda: self.helper.sql("SELECT id FROM ewaste_batches WHERE id=%s AND status='COMPLETED'", (event["batch_id"],)))
            eventually(lambda: any(i["observation"] == "offset_committed" and i.get("batch_id") == event["batch_id"] for i in after))
        self.assertEqual(lost, [batch])
        eventually(lambda: health.state(ready=True))
        finish()
        producer = Producer({"bootstrap.servers": self.broker, "enable.idempotence": True, "acks": "all"})
        duplicate_offsets = []
        for event in events:
            producer.produce("ewaste.batch.events", key=event["batch_id"], value=canonical(event),
                             on_delivery=lambda error, message: duplicate_offsets.append((error, message.offset())))
        self.assertEqual(producer.flush(10), 0)
        self.assertTrue(all(error is None for error, _ in duplicate_offsets))
        finish, restarted, _, _, _ = self.start_analytics()
        eventually(lambda: {offset for _, offset in duplicate_offsets}.issubset({i.get("offset") for i in restarted if i["observation"] == "offset_committed"}))
        finish()
        expected_flags = [[], ["CATEGORY_MISMATCH", "COUNT_MISMATCH", "WEIGHT_MISMATCH", "UNALLOCATED_WEIGHT"],
                          ["MISSING_OUTCOME"], ["UNALLOCATED_WEIGHT"]]
        journeys = []
        for event, flags in zip(events, expected_flags):
            batch = event["batch_id"]
            self.assertEqual(set(self.counts(batch).values()), {1})
            output = acknowledgement(client.prepare(event), event)
            self.assertEqual(output["anomaly_codes"], flags)
            result = client.commit(event, output)
            self.assertEqual(result["event_state"], "REPLAYED")
            self.assertEqual(set(self.counts(batch).values()), {1})
            rows = {table: self.sql("SELECT * FROM " + table + " WHERE " + ("id" if table == "ewaste_batches" else "batch_id") + "=%s", (batch,))
                    for table in ("ewaste_batches", "batch_receipts", "batch_treatments", "batch_impact_metrics", "batch_anomalies",
                                  "batch_audit_events", "command_idempotency", "event_outbox")}
            self.write(batch + "-rows.json", rows)
            metric = rows["batch_impact_metrics"][0]
            audit = [r for r in rows["batch_audit_events"] if r["event_type"] == "AnalyticsCompleted"]
            completed_event = [r for r in rows["event_outbox"] if r["event_type"] == "RequestCompleted"]
            self.assertEqual(len(audit), 1)
            self.assertEqual(len(completed_event), 1)
            self.assertEqual(audit[0]["command_id"], metric["command_id"])
            self.assertEqual(completed_event[0]["command_id"], metric["command_id"])
            self.assertEqual(audit[0]["correlation_id"], event["correlation_id"])
            self.assertEqual(sorted(r["anomaly_code"] for r in rows["batch_anomalies"]), sorted(flags))
            snapshot = loads(metric["input_snapshot_json"])
            self.assertEqual(snapshot["metrics"], output["metrics"])
            self.assertEqual(metric["correlation_id"], event["correlation_id"])
            self.assertEqual(metric["source_event_id"], event["event_id"])
            self.assertEqual(metric["source_batch_version"], event["aggregate_version"])
            for key in ("reused_kg", "recycled_kg", "disposed_kg", "unknown_kg", "diverted_kg"):
                actual = None if metric[key] is None else format(metric[key], ".2f")
                self.assertEqual(actual, output["metrics"][key])
            detail = self.http("GET", "/api/v1/processing/batches/" + batch, "recycler1")["data"]
            for key in ("actual_weight_kg", "reused_kg", "recycled_kg", "disposed_kg", "unknown_kg", "diverted_kg"):
                self.assertEqual(detail[key], output["metrics"][key])
            self.assertEqual(sorted(detail["anomaly_codes"]), sorted(flags))
            self.http("GET", "/api/v1/audit/batches/" + batch + "/timeline", "auditor")
            anomalies = self.http("GET", "/api/v1/audit/batches/" + batch + "/anomalies", "auditor")
            self.assertEqual(sorted(item["code"] for item in anomalies["data"]), sorted(flags))
            journeys.append({"batch_id": batch, "correlation_id": event["correlation_id"], "source_event_id": event["event_id"],
                             "expected": output, "processing_api": detail, "sql_file": batch + "-rows.json"})
        impact = self.http("GET", "/api/v1/audit/impact", "auditor")["data"]
        by_batch = {item["batch_id"]: item for item in impact["items"]}
        self.assertEqual(impact["total_count"], len(by_batch))
        for journey in journeys:
            self.assertEqual(by_batch[journey["batch_id"]]["metrics"], journey["expected"]["metrics"])
        totals = {key: format(sum(Decimal(j["expected"]["metrics"][key] or "0") for j in journeys), ".2f")
                  for key in ("actual_weight_kg", "reused_kg", "recycled_kg", "disposed_kg", "unknown_kg", "diverted_kg")}
        self.assertEqual(totals, {"actual_weight_kg": "46.50", "reused_kg": "4.00", "recycled_kg": "16.00",
                                  "disposed_kg": "2.00", "unknown_kg": "24.50", "diverted_kg": "20.00"})
        self.verify_kafka(events)
        self.write("index.json", {"tasks": ["EWCSB-171", "EWCSB-172", "EWCSB-178"], "journeys": journeys,
                                   "distinct_completed_batches_in_fixture": 4, "totals": totals,
                                   "verification": "PASS: SQL, processing/auditor APIs, Kafka, rollback, restart, response loss, redelivery",
                                   "ui_verification": "PENDING: current frontend processing/impact views are placeholders",
                                   "evidence": ["queries.sql", "sql-results.jsonl", "api-journeys.jsonl", "analytics-facade.jsonl", "kafka.json",
                                                "rollback.json", "recovery.jsonl", "../coverage.xml", "../tests.log", "../migrations.log"]})

    def verify_kafka(self, events):
        batches = {e["batch_id"] for e in events}
        rows = []
        for batch in batches:
            eventually(lambda: not self.helper.sql("SELECT event_id FROM event_outbox WHERE batch_id=%s AND publish_state <> 'PUBLISHED'", (batch,)))
            rows.extend(self.sql("SELECT * FROM event_outbox WHERE batch_id=%s AND event_type IN ('ReceiptVerified','RecyclingCompleted','RequestCompleted') ORDER BY aggregate_version", (batch,)))
        expected = {r["event_id"]: loads(r["payload_json"]) for r in rows}
        consumer = Consumer({"bootstrap.servers": self.broker, "group.id": "analytics-evidence-reader", "enable.auto.commit": False})
        self.addCleanup(consumer.close)
        part = TopicPartition("ewaste.batch.events", 0, 0)
        end = consumer.get_watermark_offsets(part, timeout=10)[1]
        consumer.assign([part])
        records, seen, order = [], set(), {}
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            message = consumer.poll(.2)
            if message is not None and not message.error():
                value = loads(message.value())
                if value.get("event_id") in expected:
                    validate(value["event_type"], value)
                    self.assertEqual(value, expected[value["event_id"]])
                    self.assertEqual(message.key().decode(), value["batch_id"])
                    if value["event_id"] not in seen:
                        previous = order.get(value["batch_id"], 0)
                        self.assertGreater(value["aggregate_version"], previous)
                        order[value["batch_id"]] = value["aggregate_version"]
                    seen.add(value["event_id"])
                    records.append({"topic": message.topic(), "partition": message.partition(), "offset": message.offset(),
                                    "key": message.key().decode(), "event": value})
            if consumer.position([part])[0].offset >= end:
                break
        self.assertEqual(seen, set(expected))
        self.assertEqual(len(expected), 12)
        self.assertEqual(len(records), 16)  # Four exact source replays, no duplicate RequestCompleted.
        self.write("kafka.json", records)
