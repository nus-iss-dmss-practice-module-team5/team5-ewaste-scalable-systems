"""Real Go/MySQL/Kafka acceptance tests; database access exists only in tests."""
import hashlib
import json
import os
import threading
import time
import unittest
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

from confluent_kafka import Consumer, Producer, TopicPartition
from confluent_kafka.admin import AdminClient, NewTopic

from matcher.contract import canonical, loads, validate
from matcher.facade import FacadeClient, FacadeError
from matcher.kafka import KafkaRunner, QuarantinePublisher
from matcher.worker import Processor
from matcher.runtime import token_provider


def eventually(check, seconds=40):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(.2)
    raise AssertionError("condition did not become true within timeout")


@unittest.skipUnless(os.environ.get("MATCHER_TEST_API"), "real Go/MySQL integration not configured")
class PersistenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import pymysql
        cls.db = pymysql.connect(host=os.environ["MATCHER_TEST_MYSQL"], user="matcher", password="matcher-local-only",
                                 database="matcher_test", autocommit=True, cursorclass=pymysql.cursors.DictCursor)
        cls.broker = os.environ["MATCHER_TEST_BROKER"]
        cls.url = os.environ["MATCHER_TEST_API"]
        topics = ["ewaste.batch.events", "ewaste.claim.events", "batch.collector.assigned",
                  "batch.collection.completed", "batch.collection.failed", "ewaste.batch.events.matching.dlq.v1"]
        admin = AdminClient({"bootstrap.servers": cls.broker})
        existing = admin.list_topics(timeout=10).topics
        missing = [NewTopic(t, 1, 1) for t in topics if t not in existing]
        if missing:
            for future in admin.create_topics(missing).values():
                future.result(10)
        def ready():
            try:
                with urllib.request.urlopen(cls.url + "/readyz", timeout=2) as response:
                    return response.status == 200
            except OSError:
                return False
        eventually(ready)

    @classmethod
    def tearDownClass(cls):
        cls.db.close()

    def sql(self, query, args=()):
        with self.db.cursor() as cursor:
            cursor.execute(query, args)
            return cursor.fetchall() if cursor.description else cursor.rowcount

    def seed(self, suffix):
        batch_id = f"b1000000-0000-4000-8000-{suffix:012d}"
        command_id = f"c1000000-0000-4000-8000-{suffix:012d}"
        event_id = f"e1000000-0000-4000-8000-{suffix:012d}"
        submitted = datetime.now(timezone.utc).replace(microsecond=0)
        deadline = submitted + timedelta(hours=72)
        stamp = lambda value: value.strftime("%Y-%m-%dT%H:%M:%S.%fZ")
        data = {"organization_id": "DON-001", "submitted_at": stamp(submitted), "category": "ICT_EQUIPMENT",
                "quantity": 10, "estimated_weight_kg": "100.00", "condition_rating": "REPAIRABLE",
                "is_data_bearing": True, "zone": "NORTH", "collection_deadline": stamp(deadline)}
        event = {"event_id": event_id, "event_type": "RequestSubmitted", "schema_version": 1,
                 "command_id": command_id, "batch_id": batch_id, "batch_version": 2, "claim_epoch": "1",
                 "sequence_in_command": 1, "occurred_at": stamp(submitted), "correlation_id": "end-to-end-" + str(suffix), "data": data}
        validate("RequestSubmitted", event)
        self.sql("UPDATE recycler_capacity_pools SET reserved_kg=0,version=version+1 WHERE recycler_org_id='PROC-001'")
        self.sql("UPDATE recycler_service_zones SET minimum_lead_minutes=60,version=version+1 WHERE recycler_org_id='PROC-001'")
        self.sql("""INSERT INTO ewaste_batches(id,organization_id,created_by,status,category,quantity,estimated_weight_kg,
                 condition_rating,is_data_bearing,zone,collection_deadline,claim_epoch,version,submitted_at,created_at,updated_at)
                 VALUES(%s,'DON-001','USR-003','SUBMITTED','ICT_EQUIPMENT',10,100,'REPAIRABLE',1,'NORTH',%s,1,2,%s,%s,%s)""",
                 (batch_id, deadline.replace(tzinfo=None), *[submitted.replace(tzinfo=None)] * 3))
        self.sql("""INSERT INTO command_idempotency(id,service_principal,actor_scope,command_name,idempotency_key,request_hash,
                 batch_id,state,response_status,response_json,created_at,completed_at,retain_until)
                 VALUES(%s,'fixture','service:fixture','SubmitBatch',%s,%s,%s,'COMPLETED',200,'{}',%s,%s,%s)""",
                 (command_id, event_id, "a" * 64, batch_id, submitted.replace(tzinfo=None), submitted.replace(tzinfo=None),
                  (submitted + timedelta(days=365)).replace(tzinfo=None)))
        self.sql("""INSERT INTO event_outbox(event_id,batch_id,command_id,event_type,topic,schema_version,aggregate_version,
                 sequence_in_command,partition_key,payload_json,correlation_id,occurred_at,created_at,publish_state,next_attempt_at)
                 VALUES(%s,%s,%s,'RequestSubmitted','ewaste.batch.events',1,2,1,%s,%s,%s,%s,%s,'PENDING',%s)""",
                 (event_id, batch_id, command_id, batch_id, canonical(event).decode(), event["correlation_id"],
                  *[submitted.replace(tzinfo=None)] * 3))
        return event

    def start_worker(self, group, lose_response=False):
        common = {"bootstrap.servers": self.broker, "security.protocol": "PLAINTEXT"}
        provider = token_provider({"MATCHER_SIGNING_SECRET": "local-matching-secret-at-least-32-characters",
                                   "MATCHER_TOKEN_ISSUER": "matcher-integration-test", "MATCHER_TOKEN_AUDIENCE": "matching-api"})
        client = FacadeClient(self.url, None, 5, 16 << 20, local=True, token_provider=provider)
        if lose_response:
            original = client.run
            lost = [False]
            def run(*args, **kwargs):
                value = original(*args, **kwargs)
                action = kwargs.get("action") or (args[2] if len(args) > 2 else None)
                if action == "result" and not lost[0]:
                    lost[0] = True
                    observations.append(("lost_result_response", None, {}))
                    raise FacadeError()  # Server committed; worker never received that response.
                return value
            client.run = run
        observations = []
        def observe(name, delivery=None, **fields):
            if delivery and delivery.event:
                fields["batch_id"] = delivery.event["batch_id"]
            observations.append((name, delivery.record.offset if delivery else None, fields))
        processor = Processor(client, QuarantinePublisher(common, "ewaste.batch.events.matching.dlq.v1", 10),
                              max_bytes=1048576, retry_base=.1, retry_max=.5, max_refreshes=5, observe=observe)
        runner = KafkaRunner(common, topic="ewaste.batch.events", group_id=group, offset_reset="earliest",
                             max_poll_ms=300000, session_timeout_ms=10000, workers=1, processor=processor, observer=observe)
        stop = threading.Event()
        thread = threading.Thread(target=runner.run, args=(stop,), daemon=True)
        thread.start()
        def finish():
            stop.set(); thread.join(15)
            self.assertFalse(thread.is_alive(), "consumer did not stop")
        self.addCleanup(finish)
        return stop, thread, observations

    def test_real_outbox_worker_persistence_and_replay(self):
        event = self.seed(1)
        group = "matching-e2e-replay"
        stop, thread, observations = self.start_worker(group, lose_response=True)
        batch = event["batch_id"]
        result = eventually(lambda: self.sql("SELECT id,correlation_id,input_snapshot_json FROM matching_decisions WHERE batch_id=%s", (batch,)))
        self.assertEqual(result[0]["correlation_id"], event["correlation_id"])
        snapshot = loads(result[0]["input_snapshot_json"])
        self.assertEqual(snapshot["trigger_id"], event["event_id"])
        self.assertEqual(snapshot["batch"]["batch_id"], batch)
        expected_count = self.sql("SELECT COUNT(*) AS n FROM organisations WHERE organisation_type='PROCESSING_FACILITY' AND status='ACTIVE'")[0]["n"]
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM matched_results WHERE batch_id=%s", (batch,))[0]["n"], expected_count)
        published = eventually(lambda: self.sql("SELECT payload_json FROM event_outbox WHERE batch_id=%s AND event_type='MatchingCompleted' AND publish_state='PUBLISHED'", (batch,)))
        completed = loads(published[0]["payload_json"])
        validate("MatchingCompleted", completed)
        self.assertEqual(completed["batch_id"], batch)
        self.assertEqual(completed["correlation_id"], event["correlation_id"])
        eventually(lambda: any(name == "offset_committed" and fields.get("batch_id") == batch for name, _, fields in observations))
        self.assertTrue(any(name == "lost_result_response" for name, _, _ in observations))
        stop.set(); thread.join(15)
        producer = Producer({"bootstrap.servers": self.broker})
        duplicates = []
        producer.produce("ewaste.batch.events", key=batch, value=canonical(event),
                         on_delivery=lambda error, message: duplicates.append((error, message.offset())))
        self.assertEqual(producer.flush(10), 0)
        self.assertIsNone(duplicates[0][0])
        _, _, after_restart = self.start_worker(group)
        eventually(lambda: any(name == "offset_committed" and offset == duplicates[0][1] for name, offset, _ in after_restart))
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM matching_decisions WHERE batch_id=%s", (batch,))[0]["n"], 1)
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM batch_audit_events WHERE batch_id=%s AND event_type='MatchingCompleted'", (batch,))[0]["n"], 1)
        self.assertEqual(self.sql("SELECT COUNT(*) AS n FROM event_outbox WHERE batch_id=%s AND event_type='MatchingCompleted'", (batch,))[0]["n"], 1)
        self.assertEqual(self.sql("SELECT status,version FROM ewaste_batches WHERE id=%s", (batch,))[0], {"status": "MATCHED", "version": 3})

    def test_invalid_source_is_quarantined_on_real_broker(self):
        common = {"bootstrap.servers": self.broker}
        group = "matching-e2e-invalid"
        stop, thread, observations = self.start_worker(group)
        producer = Producer(common)
        producer.produce("ewaste.batch.events", key="bad-batch", value=b'{"event_type":"RequestSubmitted","schema_version":1}')
        self.assertEqual(producer.flush(10), 0)
        consumer = Consumer({**common, "group.id": "e2e-dlq-inspector", "auto.offset.reset": "earliest", "enable.auto.commit": False})
        self.addCleanup(consumer.close)
        consumer.subscribe(["ewaste.batch.events.matching.dlq.v1"])
        def quarantined():
            message = consumer.poll(.5)
            if message is None or message.error():
                return None
            value = loads(message.value())
            return value if value["original_record_sha256"] == hashlib.sha256(b'{"event_type":"RequestSubmitted","schema_version":1}').hexdigest() else None
        try:
            record = eventually(quarantined)
        except AssertionError:
            self.fail("DLQ was not observed; worker observations: " + repr(observations[-20:]))
        validate("MatchingQuarantine", record)
        self.assertEqual(record["error_code"], "INVALID_CONTRACT")
        self.assertNotIn("original_event", record)
