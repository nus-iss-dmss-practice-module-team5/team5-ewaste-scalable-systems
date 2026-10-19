"""EWSS-32: restart a replica under load, replay an event and send a poison event.

Runs the EWSS-18 cases RS-10, RS-04, RS-01 and RS-08 against a deployed
environment and reconciles each one in MySQL. It must run inside the Azure
VNet (the self-hosted runner), because MySQL has no public endpoint.

Every case writes one evidence folder: steps with UTC times, an HTTP log,
the SQL text with its results, Kafka records, container logs and a
PASS / FAIL / INCONCLUSIVE verdict. The database connection is read-only.
"""

import argparse
import concurrent.futures
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

import pymysql
from confluent_kafka import Consumer, ConsumerGroupTopicPartitions, Producer, TopicPartition
from confluent_kafka.admin import AdminClient

APP_SUFFIXES = ("auth", "api", "claim", "custody", "ui", "matcher", "analytic")
BATCH_TOPIC = "ewaste.batch.events"
DLQ_TOPIC = "ewaste.batch.events.matching.dlq.v1"
MATCHING_GROUP = "matching-worker-v1"
# Partition counts provisioned by terraform/environments/main.tf.
EXPECTED_PARTITIONS = {
    "ewaste.batch.events": 3,
    "ewaste.claim.events": 3,
    "batch.collector.assigned": 3,
    "batch.collection.completed": 3,
    "batch.collection.failed": 1,
    DLQ_TOPIC: 1,
}
RETRYABLE = {0, 429, 500, 502, 503, 504}
SECRET_PATTERNS = (re.compile(r"(Bearer\s+)[A-Za-z0-9._-]+"), re.compile(r"(SharedAccessKey=)[^;\s]+"))


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def redact(text):
    for pattern in SECRET_PATTERNS:
        text = pattern.sub(r"\1<redacted>", text)
    return text


class Env:
    def __init__(self):
        get = os.environ.get
        self.target = get("TARGET_ENV", "prod")
        self.rg = get("RESOURCE_GROUP", f"rg-ewastescalable-{self.target}")
        self.base_url = get("BASE_URL", "").rstrip("/")
        self.mysql = dict(host=get("MYSQL_HOST", ""), user=get("MYSQL_USER", ""),
                          password=get("MYSQL_PASSWORD", ""), database=get("MYSQL_DB", ""))
        self.evh_namespace = get("EVH_NAMESPACE", f"evh-ewastescalable-{self.target}")
        self.evh_connection = get("EVH_CONNECTION_STRING", "")
        self.donor_email = get("DONOR_EMAIL", "donor1@ewaste.test")
        self.donor_password = get("DONOR_PASSWORD", "")
        self.commit = get("GITHUB_SHA", "")
        missing = [name for name, value in (
            ("BASE_URL", self.base_url), ("MYSQL_HOST", self.mysql["host"]),
            ("MYSQL_USER", self.mysql["user"]), ("MYSQL_PASSWORD", self.mysql["password"]),
            ("MYSQL_DB", self.mysql["database"]), ("EVH_CONNECTION_STRING", self.evh_connection),
            ("DONOR_PASSWORD", self.donor_password)) if not value]
        if missing:
            sys.exit(f"missing environment: {', '.join(missing)}")

    def app(self, suffix):
        return f"aca-ewastescalable-{self.target}-{suffix}"

    def kafka(self, **extra):
        return {"bootstrap.servers": f"{self.evh_namespace}.servicebus.windows.net:9093",
                "security.protocol": "SASL_SSL", "sasl.mechanism": "PLAIN",
                "sasl.username": "$ConnectionString", "sasl.password": self.evh_connection, **extra}


class Case:
    """One evidence folder per case, tagged as EWSS-18 requires."""

    def __init__(self, root, case_id, run_id):
        self.id, self.run_id = case_id, run_id
        self.tag = f"RS:{case_id}:{run_id}"
        self.dir = root / case_id
        self.dir.mkdir(parents=True, exist_ok=True)
        self.checks = []
        self._lock = threading.Lock()

    def step(self, message):
        line = f"{utc()} {message}"
        print(f"[{self.id}] {line}", flush=True)
        with self._lock, open(self.dir / "steps.log", "a") as fh:
            fh.write(line + "\n")

    def http(self, entry):
        with self._lock, open(self.dir / "http.jsonl", "a") as fh:
            fh.write(json.dumps(entry) + "\n")

    def save(self, name, data):
        (self.dir / f"{name}.json").write_text(json.dumps(data, indent=2, default=str))

    def check(self, name, passed, detail):
        self.checks.append({"check": name, "passed": passed, "detail": detail})
        self.step(f"check {name}: {'PASS' if passed else 'FAIL'} ({detail})")

    def finish(self, verdict=None, note=""):
        if verdict is None:
            verdict = "PASS" if self.checks and all(c["passed"] for c in self.checks) else "FAIL"
        result = {"case": self.id, "run_id": self.run_id, "tag": self.tag, "verdict": verdict,
                  "note": note, "finished_at": utc(), "checks": self.checks}
        self.save("result", result)
        (self.dir / "result.txt").write_text(verdict + "\n")
        self.step(f"verdict {verdict}{': ' + note if note else ''}")
        return result


class Harness:
    def __init__(self, env, out, run_id):
        self.env, self.out, self.run_id = env, out, run_id
        self.started = utc()
        self._db = None

    # ---- Azure ------------------------------------------------------------
    def az(self, case, *args, as_json=True):
        cmd = ["az", *args] + (["-o", "json"] if as_json else [])
        case.step("az " + " ".join(args))
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
        if proc.returncode != 0:
            raise RuntimeError(f"az {' '.join(args[:3])} failed: {redact(proc.stderr.strip())[:500]}")
        return json.loads(proc.stdout) if as_json and proc.stdout.strip() else proc.stdout

    def active_revisions(self, case, suffix):
        return self.az(case, "containerapp", "revision", "list", "--name", self.env.app(suffix),
                       "--resource-group", self.env.rg, "--query", "[?properties.active].name")

    def replica_states(self, case, suffix, revision):
        return self.az(case, "containerapp", "replica", "list", "--name", self.env.app(suffix),
                       "--resource-group", self.env.rg, "--revision", revision,
                       "--query", "[].{name:name, state:properties.runningState}")

    def restart(self, case, suffix):
        for revision in self.active_revisions(case, suffix):
            before = self.replica_states(case, suffix, revision)
            case.step(f"restarting {self.env.app(suffix)} revision {revision} ({len(before)} replica(s))")
            self.az(case, "containerapp", "revision", "restart", "--name", self.env.app(suffix),
                    "--resource-group", self.env.rg, "--revision", revision, as_json=False)
            case.save(f"restart-{suffix}", {"revision": revision, "replicas_before": before, "at": utc()})

    def wait_running(self, case, suffix, timeout=300):
        """Wait until every active revision is healthy again after a restart."""
        time.sleep(20)  # a revision can still report Healthy just after the restart call
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            states = [self.az(case, "containerapp", "revision", "show", "--name", self.env.app(suffix),
                              "--resource-group", self.env.rg, "--revision", revision, "--query",
                              "{revision: name, running: properties.runningState,"
                              " health: properties.healthState, replicas: properties.replicas}")
                      for revision in self.active_revisions(case, suffix)]
            if states and all(s["health"] == "Healthy" and str(s["running"]).startswith("Running")
                              and (s["replicas"] or 0) >= 1 for s in states):
                case.step(f"{self.env.app(suffix)} healthy again: {states}")
                case.save(f"recovered-{suffix}", {"at": utc(), "revisions": states})
                return states
            time.sleep(10)
        raise RuntimeError(f"{self.env.app(suffix)} not healthy within {timeout}s of the restart")

    def restart_in_background(self, case, suffix, delay):
        """Restart after `delay` seconds without blocking the load; errors are kept as evidence."""
        errors = []

        def run():
            try:
                self.restart(case, suffix)
            except Exception as exc:
                errors.append(str(exc))
                case.step(f"restart of {suffix} failed: {redact(str(exc))}")

        timer = threading.Timer(delay, run)
        timer.start()
        return timer, errors

    def container_logs(self, case, suffix):
        try:
            text = self.az(case, "containerapp", "logs", "show", "--name", self.env.app(suffix),
                           "--resource-group", self.env.rg, "--tail", "300", "--format", "text",
                           as_json=False)
        except RuntimeError as exc:
            text = f"log capture failed: {exc}"
        (case.dir / f"logs-{suffix}.txt").write_text(redact(text))

    def manifest(self, case):
        apps = {}
        for suffix in APP_SUFFIXES:
            try:
                info = self.az(case, "containerapp", "show", "--name", self.env.app(suffix),
                               "--resource-group", self.env.rg, "--query",
                               "{image: properties.template.containers[0].image,"
                               " latest: properties.latestRevisionName,"
                               " ready: properties.latestReadyRevisionName,"
                               " min: properties.template.scale.minReplicas,"
                               " max: properties.template.scale.maxReplicas}")
                info["replicas"] = sum(len(self.replica_states(case, suffix, rev))
                                       for rev in self.active_revisions(case, suffix))
            except RuntimeError as exc:
                info = {"error": str(exc)}
            apps[self.env.app(suffix)] = info
        return {"run_id": self.run_id, "commit": self.env.commit, "environment": self.env.target,
                "resource_group": self.env.rg, "started_at": self.started, "apps": apps}

    # ---- HTTP -------------------------------------------------------------
    def call(self, case, method, path, token=None, body=None, headers=None, timeout=20):
        correlation = f"{case.tag}:{uuid.uuid4().hex[:12]}"
        hdrs = {"Content-Type": "application/json", "X-Correlation-ID": correlation, **(headers or {})}
        if token:
            hdrs["Authorization"] = f"Bearer {token}"
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.env.base_url + path, data=data, headers=hdrs, method=method)
        started, status, text = time.monotonic(), 0, ""
        try:
            with urllib.request.urlopen(request, timeout=timeout) as resp:
                status, text = resp.status, resp.read().decode()
        except urllib.error.HTTPError as exc:
            status, text = exc.code, exc.read().decode(errors="replace")
        except Exception as exc:  # timeouts and resets are evidence, not crashes
            text = f"{type(exc).__name__}: {exc}"
        try:
            parsed = json.loads(text)
        except ValueError:
            parsed = None
        error = parsed.get("code") if isinstance(parsed, dict) else (text[:120] if status == 0 else None)
        case.http({"at": utc(), "method": method, "path": path, "status": status,
                   "ms": round((time.monotonic() - started) * 1000), "correlation_id": correlation,
                   "idempotency_key": hdrs.get("Idempotency-Key"), "error": error})
        return status, parsed

    def login(self, case):
        status, body = self.call(case, "POST", "/api/v1/auth/login",
                                 body={"email": self.env.donor_email, "password": self.env.donor_password})
        if status != 200 or not body or not body.get("access_token"):
            raise RuntimeError(f"donor login failed with HTTP {status}")
        return body["access_token"]

    @staticmethod
    def draft(notes):
        deadline = dt.datetime.now(dt.timezone.utc) + dt.timedelta(days=7)
        return {"category": "ICT_EQUIPMENT", "quantity": 2, "estimated_weight_kg": 5.0,
                "condition_rating": "FUNCTIONAL", "is_data_bearing": False, "zone": "CENTRAL",
                "collection_deadline": deadline.isoformat().replace("+00:00", "Z"), "notes": notes}

    def key(self, case, n, kind):
        return f"ewss32-{self.run_id}-{case.id}-{n}-{kind}"[:64]

    def with_retries(self, case, label, send, attempts=8, delay=5):
        """Resend with the same Idempotency-Key until it lands or fails for good."""
        history = []
        for attempt in range(1, attempts + 1):
            status, body = send()
            history.append(status)
            if 200 <= status < 300:
                return status, body, history
            if status not in RETRYABLE:
                break
            case.step(f"{label}: HTTP {status}, retrying with the same key ({attempt}/{attempts})")
            time.sleep(delay)
        return status, body, history

    def create_and_submit(self, case, token, n, submit=True):
        notes = f"{case.tag}:{n}"
        status, body, created = self.with_retries(case, f"create {n}", lambda: self.call(
            case, "POST", "/api/v1/batches", token, self.draft(notes),
            {"Idempotency-Key": self.key(case, n, "create")}))
        record = {"n": n, "notes": notes, "create_status": status, "create_attempts": created}
        if not 200 <= status < 300:
            return record
        batch = body["data"]
        record.update(batch_id=batch["batch_id"], version=batch["version"])
        if submit:
            status, body, submitted = self.with_retries(case, f"submit {n}", lambda: self.call(
                case, "POST", f"/api/v1/batches/{batch['batch_id']}/submit", token, {},
                {"Idempotency-Key": self.key(case, n, "submit"), "If-Match-Version": str(batch["version"])}))
            record.update(submit_status=status, submit_attempts=submitted, submitted_at=utc(),
                          status=(body or {}).get("data", {}).get("status"))
        return record

    # ---- MySQL (read only) -------------------------------------------------
    def db(self):
        if self._db is None:
            self._db = pymysql.connect(**self.env.mysql, port=3306, connect_timeout=15, autocommit=True,
                                       ssl_ca="/etc/ssl/certs/ca-certificates.crt", ssl_verify_identity=True,
                                       cursorclass=pymysql.cursors.DictCursor)
            with self._db.cursor() as cur:
                cur.execute("SET SESSION TRANSACTION READ ONLY")
        return self._db

    def sql(self, case, name, query, params=()):
        with self.db().cursor() as cur:
            cur.execute(query, params)
            rows = cur.fetchall()
        case.save(f"sql-{name}", {"at": utc(), "query": query, "params": params, "rows": rows})
        return rows

    def identity(self, case):
        return self.sql(case, "identity", "SELECT DATABASE() AS db, @@hostname AS host, "
                        "(SELECT COUNT(*) FROM DATABASECHANGELOG) AS changesets, "
                        "(SELECT MAX(ID) FROM DATABASECHANGELOG WHERE ID LIKE 'EWSS%%') AS newest_ewss_changeset, "
                        "UTC_TIMESTAMP(6) AS db_utc")

    def drain_outbox(self, case, timeout=180):
        """Wait until the case's outbox rows are all published (EWSS-17 budget)."""
        deadline = time.monotonic() + timeout
        while True:
            rows = self.sql(case, "s5-states", S5_STATES, (case.tag + "%",))
            pending = sum(r["n"] for r in rows if r["publish_state"] != "PUBLISHED")
            if pending == 0 or time.monotonic() > deadline:
                case.step(f"outbox drain: {pending} unpublished row(s)")
                return pending
            time.sleep(10)

    # ---- Kafka ---------------------------------------------------------------
    def committed(self, topic, group, partitions):
        admin = AdminClient(self.env.kafka())
        request = ConsumerGroupTopicPartitions(group, [TopicPartition(topic, p) for p in partitions])
        result = admin.list_consumer_group_offsets([request], request_timeout=30)[group].result()
        return {tp.partition: tp.offset for tp in result.topic_partitions}

    def watermarks(self, topic, partitions):
        consumer = Consumer(self.env.kafka(**{"group.id": f"ewss32-probe-{self.run_id}",
                                              "enable.auto.commit": False}))
        try:
            return {p: consumer.get_watermark_offsets(TopicPartition(topic, p), timeout=20) for p in partitions}
        finally:
            consumer.close()

    # ---- Cases ------------------------------------------------------------------
    def rs10(self, case):
        """Partition counts as provisioned, and the matching group drained before tests."""
        counts = {}
        for topic, expected in EXPECTED_PARTITIONS.items():
            counts[topic] = self.az(case, "eventhubs", "eventhub", "show", "--resource-group", self.env.rg,
                                    "--namespace-name", self.env.evh_namespace, "--name", topic,
                                    "--query", "partitionCount")
            case.check(f"partitions {topic}", counts[topic] == expected,
                       f"{counts[topic]} provisioned, {expected} expected")
        partitions = range(EXPECTED_PARTITIONS[BATCH_TOPIC])
        lag = None
        for _ in range(6):
            try:
                committed = self.committed(BATCH_TOPIC, MATCHING_GROUP, partitions)
                ends = self.watermarks(BATCH_TOPIC, partitions)
            except Exception as exc:
                case.save("kafka-lag", {"error": str(exc)})
                return case.finish(None if not all(c["passed"] for c in case.checks) else "INCONCLUSIVE",
                                   f"partition counts checked; lag unreadable: {exc}")
            lag = {p: {"committed": committed.get(p), "end": ends[p][1],
                       "lag": ends[p][1] - committed[p] if committed.get(p, -1) >= 0 else None}
                   for p in partitions}
            if all(v["lag"] == 0 for v in lag.values()):
                break
            time.sleep(10)
        case.save("kafka-lag", {"group": MATCHING_GROUP, "topic": BATCH_TOPIC, "partitions": lag})
        case.check("matching group drained", all(v["lag"] == 0 for v in lag.values()),
                   ", ".join(f"p{p} lag {v['lag']}" for p, v in lag.items()))
        return case.finish()

    def rs04(self, case):
        """A schema-invalid RequestSubmitted goes to the DLQ and the partition keeps moving."""
        batch_id, event_id = str(uuid.uuid4()), str(uuid.uuid4())
        raw = json.dumps({"event_id": event_id, "event_type": "RequestSubmitted", "schema_version": 1,
                          "batch_id": batch_id, "correlation_id": case.tag}).encode()
        raw_sha = hashlib.sha256(raw).hexdigest()
        dlq_start = self.watermarks(DLQ_TOPIC, [0])[0][1]
        delivered = {}
        producer = Producer(self.env.kafka())
        producer.produce(BATCH_TOPIC, key=batch_id.encode(), value=raw,
                         on_delivery=lambda err, msg: delivered.update(
                             error=str(err) if err else None, partition=msg.partition(), offset=msg.offset()))
        producer.flush(30)
        case.save("poison-event", {"key": batch_id, "event_id": event_id, "value": raw.decode(),
                                   "sha256": raw_sha, "delivery": delivered, "published_at": utc()})
        if not delivered or delivered.get("error"):
            return case.finish("FAIL", f"poison event not delivered: {delivered}")
        case.step(f"poison published to {BATCH_TOPIC} p{delivered['partition']} offset {delivered['offset']}")

        record = None
        consumer = Consumer(self.env.kafka(**{"group.id": f"ewss32-dlq-{self.run_id}",
                                              "enable.auto.commit": False}))
        consumer.assign([TopicPartition(DLQ_TOPIC, 0, dlq_start)])
        deadline = time.monotonic() + 180
        try:
            while record is None and time.monotonic() < deadline:
                msg = consumer.poll(5)
                if msg is None or msg.error():
                    continue
                try:
                    value = json.loads(msg.value())
                except (TypeError, ValueError):
                    continue
                if isinstance(value, dict) and value.get("original_record_sha256") == raw_sha:
                    record = {"key": (msg.key() or b"").decode(), "partition": msg.partition(),
                              "offset": msg.offset(), "value": value}
        finally:
            consumer.close()
        case.save("dlq-record", record or {"found": False, "searched_from_offset": dlq_start})
        case.check("DLQ record written", record is not None,
                   f"error_code {record['value'].get('error_code')}, source offset "
                   f"{record['value'].get('source_offset')}" if record else "not found within 180 s")
        if record:
            case.check("DLQ record points at the poison event",
                       record["value"].get("source_partition") == delivered["partition"]
                       and record["value"].get("source_offset") == str(delivered["offset"]),
                       f"source p{record['value'].get('source_partition')} offset "
                       f"{record['value'].get('source_offset')}; keyed by quarantine_id {record['key']}")

        rows = self.sql(case, "s4", S4, (event_id, batch_id))
        case.check("S4 no matching decision", rows[0]["decisions"] == 0, f"{rows[0]['decisions']} decision(s)")
        offset = None
        for _ in range(12):
            offset = self.committed(BATCH_TOPIC, MATCHING_GROUP, [delivered["partition"]]).get(delivered["partition"])
            if offset is not None and offset > delivered["offset"]:
                break
            time.sleep(10)
        case.check("partition committed past the poison", offset is not None and offset > delivered["offset"],
                   f"committed offset {offset}, poison offset {delivered['offset']}")
        self.container_logs(case, "matcher")
        return case.finish()

    def rs01(self, case, count=10):
        """Restart the matcher while it works through fresh submissions; one decision each."""
        token = self.login(case)
        half = count // 2
        batches = [self.create_and_submit(case, token, n) for n in range(1, half + 1)]
        # Restart while the first half is being matched; the second half arrives during the restart.
        timer, restart_errors = self.restart_in_background(case, "matcher", 0)
        batches += [self.create_and_submit(case, token, n) for n in range(half + 1, count + 1)]
        timer.join()
        case.save("batches", batches)
        if restart_errors:
            return case.finish("INCONCLUSIVE", f"matcher restart failed: {restart_errors[0][:200]}")
        submitted = [b for b in batches if b.get("submit_status") in (200, 201)]
        if len(submitted) != count:
            return case.finish("INCONCLUSIVE", f"only {len(submitted)} of {count} batches submitted")
        self.wait_running(case, "matcher")
        deadline, rows = time.monotonic() + 300, []
        while time.monotonic() < deadline:
            rows = self.sql(case, "s1-progress", S1, (case.tag + "%",))
            if len(rows) == count and all(r["decisions"] >= 1 for r in rows):
                break
            time.sleep(15)
        case.step("all batches have a decision; waiting 45 s for any redelivered duplicate")
        time.sleep(45)
        self.drain_outbox(case)
        rows = self.sql(case, "s1", S1, (case.tag + "%",))
        case.check("S1 one decision and one trigger per batch",
                   len(rows) == count and all(r["decisions"] == 1 and r["triggers"] == 1 for r in rows),
                   f"{len(rows)} batches; decisions {[r['decisions'] for r in rows]}")
        dupes = self.sql(case, "s2", S2, (case.tag + "%",))
        case.check("S2 one durable event per command", not dupes, f"{len(dupes)} offending row(s)")
        self.container_logs(case, "matcher")
        return case.finish()

    def rs08(self, case, count=30, restart_after=15):
        """Restart batch-workflow mid-load; retried commands land once, none left IN_PROGRESS."""
        token = self.login(case)
        window_start = self.identity(case)[0]["db_utc"]
        timer, restart_errors = self.restart_in_background(case, "api", restart_after)
        with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
            futures = []
            for n in range(1, count + 1):
                futures.append(pool.submit(self.create_and_submit, case, token, n, False))
                time.sleep(3)
            results = [f.result() for f in futures]
        timer.join()
        case.save("commands", results)
        if restart_errors:
            return case.finish("INCONCLUSIVE", f"batch-workflow restart failed: {restart_errors[0][:200]}")
        self.wait_running(case, "api")
        window_end = self.identity(case)[0]["db_utc"]
        failed = [r for r in results if not 200 <= r["create_status"] < 300]
        retried = [r for r in results if len(r["create_attempts"]) > 1]
        case.check("every command eventually succeeded", not failed,
                   f"{count - len(failed)}/{count} succeeded; {len(retried)} needed a retry")
        rows = self.sql(case, "duplicates", DUPLICATES, (case.tag + ":%",))
        case.check("one batch per command (no duplicate durable row)",
                   len(rows) == count and all(r["n"] == 1 for r in rows),
                   f"{len(rows)} distinct commands stored, max {max((r['n'] for r in rows), default=0)} row(s) each")
        stuck = self.sql(case, "s7", S7, (f"ewss32-{self.run_id}-{case.id}-%", window_start, window_end))
        case.check("S7 no command stuck IN_PROGRESS", not stuck, f"{len(stuck)} row(s)")
        self.drain_outbox(case)
        dupes = self.sql(case, "s2", S2, (case.tag + "%",))
        case.check("S2 one durable event per command", not dupes, f"{len(dupes)} offending row(s)")
        self.container_logs(case, "api")
        note = "" if retried else "no request failed during the restart; the restart may not have hit in-flight work"
        return case.finish(note=note)


S1 = """SELECT b.id, b.status, COUNT(d.id) AS decisions, COUNT(DISTINCT d.trigger_id) AS triggers
FROM ewaste_batches b LEFT JOIN matching_decisions d ON d.batch_id = b.id
WHERE b.notes LIKE %s GROUP BY b.id, b.status"""

S2 = """SELECT o.batch_id, o.event_type, o.command_id, COUNT(*) AS rows_for_command,
       SUM(o.publish_state = 'PUBLISHED') AS published
FROM event_outbox o JOIN ewaste_batches b ON b.id = o.batch_id
WHERE b.notes LIKE %s
GROUP BY o.batch_id, o.event_type, o.command_id
HAVING rows_for_command <> 1 OR published <> 1"""

S4 = """SELECT COUNT(*) AS decisions FROM matching_decisions WHERE trigger_id = %s OR batch_id = %s"""

S5_STATES = """SELECT o.publish_state, COUNT(*) AS n, MAX(o.attempt_count) AS max_attempts
FROM event_outbox o JOIN ewaste_batches b ON b.id = o.batch_id
WHERE b.notes LIKE %s GROUP BY o.publish_state"""

S7 = """SELECT id, command_name, idempotency_key, state, created_at FROM command_idempotency
WHERE state = 'IN_PROGRESS' AND idempotency_key LIKE %s AND created_at BETWEEN %s AND %s"""

DUPLICATES = """SELECT notes, COUNT(*) AS n FROM ewaste_batches WHERE notes LIKE %s GROUP BY notes"""

CASES = {"RS-10": "rs10", "RS-04": "rs04", "RS-01": "rs01", "RS-08": "rs08"}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--cases", default="RS-10,RS-04,RS-01,RS-08")
    parser.add_argument("--out", default="artifacts/ewss-32")
    parser.add_argument("--run-id", default=dt.datetime.now(dt.timezone.utc).strftime("r%Y%m%dt%H%M"))
    args = parser.parse_args(argv)
    cases = [c.strip().upper() for c in args.cases.split(",") if c.strip()]
    unknown = [c for c in cases if c not in CASES]
    if unknown:
        parser.error(f"unknown case(s): {', '.join(unknown)}; choose from {', '.join(CASES)}")

    root = Path(args.out) / args.run_id
    root.mkdir(parents=True, exist_ok=True)
    harness = Harness(Env(), root, args.run_id)
    setup = Case(root, "run", args.run_id)
    harness.identity(setup)
    setup.save("manifest", harness.manifest(setup))

    results = []
    for case_id in cases:
        case = Case(root, case_id, args.run_id)
        case.step(f"start {case_id} (tag {case.tag})")
        try:
            results.append(getattr(harness, CASES[case_id])(case))
        except Exception as exc:  # one broken case must not hide the others
            case.step(f"error: {redact(str(exc))}")
            results.append(case.finish("INCONCLUSIVE", f"harness error: {redact(str(exc))[:300]}"))

    lines = [f"## EWSS-32 resilience run {args.run_id}", "",
             f"Commit `{harness.env.commit[:7]}` on `{harness.env.target}`. Evidence: one folder per case.", "",
             "| Case | Verdict | Checks |", "| --- | --- | --- |"]
    for r in results:
        passed = sum(c["passed"] for c in r["checks"])
        lines.append(f"| {r['case']} | **{r['verdict']}** | {passed}/{len(r['checks'])} passed"
                     f"{' - ' + r['note'] if r['note'] else ''} |")
    (root / "summary.md").write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    return 1 if any(r["verdict"] == "FAIL" for r in results) else 0


if __name__ == "__main__":
    sys.exit(main())
