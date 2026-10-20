"""Container probes expose only aggregate health, never event payloads."""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Health:
    def __init__(self, observer=None):
        self.observer = observer
        self.lock = threading.Lock()
        self.last_poll = self.last_stats = 0
        self.broker_up = False
        self.blocked = set()

    def polled(self):
        with self.lock:
            self.last_poll = time.monotonic()

    def stats(self, raw):
        value = json.loads(raw)
        with self.lock:
            self.last_stats = time.monotonic()
            self.broker_up = any(b.get("state") == "UP" for b in value.get("brokers", {}).values()) and value.get("cgrp", {}).get("state") == "up"

    def observe(self, name, delivery=None, **fields):
        if delivery is None:
            return
        key = (delivery.record.topic, delivery.record.partition)
        with self.lock:
            if name in ("delivery_paused", "offset_commit_failed"):
                self.blocked.add(key)
            elif name == "offset_committed":
                self.blocked.discard(key)

    def revoked(self, partitions):
        with self.lock:
            self.blocked.difference_update(partitions)

    def state(self, ready=False):
        with self.lock:
            now = time.monotonic()
            alive = now - self.last_poll < 120
            return alive and (not ready or (self.broker_up and now - self.last_stats < 15 and not self.blocked))

    def serve(self, port):
        health = self
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path not in ("/healthz", "/readyz"):
                    self.send_error(404)
                    return
                okay = health.state(ready=self.path == "/readyz")
                if self.path == "/readyz" and health.observer:
                    health.observer("readiness", ready=okay, outcome="READY" if okay else "NOT_READY",
                                    code="" if okay else "DEPENDENCY_UNAVAILABLE")
                body = json.dumps({"status": "ok" if okay else "unavailable"}).encode()
                self.send_response(200 if okay else 503)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass
        server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        return server
