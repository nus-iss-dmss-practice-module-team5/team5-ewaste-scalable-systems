import copy
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from matcher.contract import canonical, loads
from matcher.facade import FacadeClient, FacadeError, transport_trace
from helpers import event, fixture


class FixtureServer:
    """HTTP protocol simulator. No Go implementation or database is involved."""
    def __init__(self, facade):
        self.facade, self.requests = facade, []
        self.status = 0
        outer = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass

            def do_GET(self): self.handle_call()
            def do_POST(self): self.handle_call()

            def handle_call(self):
                body = loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b'{}')
                outer.requests.append((self.command, self.path, dict(self.headers), copy.deepcopy(body)))
                if self.headers.get("Authorization") != "Bearer local-fixture-token":
                    code, response = 401, {"code": "UNAUTHENTICATED"}
                elif outer.status:
                    code, response = outer.status, {"code": "UNAVAILABLE"}
                else:
                    try:
                        if self.path == "/internal/v1/matching/runs":
                            if self.headers.get("Idempotency-Key") != "REQUEST_SUBMITTED:" + body["trigger_id"]:
                                raise FacadeError(400, "INVALID_CONTRACT")
                            response = outer.facade.prepare(body["original_event"])
                        else:
                            segments = self.path.split("/")
                            response = outer.facade.run(segments[5], event(), segments[6] if len(segments) > 6 else None, body)
                        code = 200
                    except FacadeError as exc:
                        code, response = exc.status, {"code": exc.code, **exc.body}
                raw = canonical(response)
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.temp = tempfile.TemporaryDirectory()
        self.token = Path(self.temp.name) / "token"
        self.token.write_text("local-fixture-token")
        self.thread.start()

    def client(self):
        return FacadeClient(f"http://127.0.0.1:{self.server.server_port}", self.token, 2, 1_000_000, local=True)

    def close(self):
        self.server.shutdown(); self.server.server_close(); self.thread.join(); self.temp.cleanup()


class FacadeTests(unittest.TestCase):
    def test_workload_header_original_trace_and_token_rotation(self):
        from test_worker import FacadeDouble
        server = FixtureServer(FacadeDouble())
        try:
            source = event(); source["correlation_id"] = "t" * 128
            server.client().prepare(source)
            method, path, headers, body = server.requests[-1]
            self.assertEqual(body["correlation_id"], source["correlation_id"])
            self.assertEqual(body["original_event"], source)
            self.assertLessEqual(len(headers["X-Correlation-Id"]), 100)
            self.assertEqual(headers["Idempotency-Key"], "REQUEST_SUBMITTED:" + source["event_id"])
            server.token.write_text("expired-fixture-token")
            with self.assertRaises(FacadeError) as caught: server.client().prepare(source)
            self.assertEqual(caught.exception.status, 401)
        finally:
            server.close()

    def test_trace_normalization_never_truncates_body(self):
        for trace in ["a" * 101, "contains whitespace", "newline\n", "é", "a" * 128]:
            normalized = transport_trace(trace)
            self.assertEqual(len(normalized), 36)
        self.assertEqual(transport_trace("trace-1"), "trace-1")
