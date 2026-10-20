"""Client for the approved Go API, not a replacement persistence service."""
import email.utils
import http.client
import re
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

from .contract import ContractError, canonical, loads, require, validate_input

UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\Z")


class FacadeError(Exception):
    def __init__(self, status=503, code="UNAVAILABLE", body=None, retry_after=0):
        self.status, self.code = status, code
        self.body, self.retry_after = body or {}, retry_after
        super().__init__(code)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        # Never forward workload credentials to a redirect target.
        return None


def transport_trace(correlation):
    return correlation if 1 <= len(correlation) <= 100 and all(33 <= ord(c) <= 126 for c in correlation) else str(uuid.uuid4())


def retry_after(value):
    try:
        return max(0, float(value))
    except (TypeError, ValueError):
        try:
            return max(0, email.utils.parsedate_to_datetime(value).timestamp() - time.time())
        except (TypeError, ValueError, OverflowError):
            return 0


class FacadeClient:
    def __init__(self, base_url, token_file, timeout, max_response_bytes, *, local=False, token_provider=None):
        url = urllib.parse.urlsplit(base_url)
        require(url.scheme == "https" or (local and url.scheme == "http"))
        require(bool(url.hostname) and not url.username and not url.password and not url.query and not url.fragment)
        require(timeout > 0 and max_response_bytes > 0)
        self.base_url = base_url.rstrip("/")
        self.token_file = Path(token_file) if token_file else None
        self.token_provider = token_provider
        require(self.token_file is not None or token_provider is not None)
        self.timeout, self.max_bytes = timeout, max_response_bytes
        self.opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context()))

    def request(self, method, path, correlation, body=None, key=None, *, expected_version=None,
                preserve_correlation=False):
        try:
            token = self.token_provider() if self.token_provider else self.token_file.read_text().strip()
            if not token or any(ord(c) < 33 or ord(c) > 126 for c in token):
                raise FacadeError(401, "UNAUTHENTICATED")
            trace = transport_trace(correlation)
            if preserve_correlation:
                require(isinstance(correlation, str) and 1 <= len(correlation) <= 128
                        and correlation == correlation.strip()
                        and all(ord(c) >= 32 and ord(c) != 127 for c in correlation))
                # urllib writes header values as Latin-1; Go receives the original UTF-8 bytes.
                trace = correlation.encode("utf-8").decode("latin-1")
            headers = {"Authorization": "Bearer " + token, "X-Correlation-ID": trace,
                       "Accept": "application/json", "Content-Type": "application/json"}
            if key:
                headers["Idempotency-Key"] = key
            if expected_version is not None:
                headers["If-Match-Version"] = str(expected_version)
            request = urllib.request.Request(self.base_url + path, data=canonical(body) if body is not None else None,
                                             method=method, headers=headers)
            try:
                response = self.opener.open(request, timeout=self.timeout)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                raw = response.read(self.max_bytes + 1)
                if len(raw) > self.max_bytes:
                    raise FacadeError(503, "INVALID_FACADE_RESPONSE")
                try:
                    value = loads(raw)
                except ContractError:
                    raise FacadeError(503, "INVALID_FACADE_RESPONSE") from None
                if not isinstance(value, dict):
                    raise FacadeError(503, "INVALID_FACADE_RESPONSE")
                if response.status not in (200, 201):
                    raise FacadeError(response.status, value.get("code", "UNAVAILABLE"), value,
                                      retry_after(response.headers.get("Retry-After")))
                return value
        except (OSError, urllib.error.URLError, http.client.HTTPException) as exc:
            raise FacadeError() from exc

    def prepare(self, event):
        body = {"trigger_id": event["event_id"], "trigger_type": "REQUEST_SUBMITTED", "original_event": event,
                **{key: event[key] for key in ("batch_id", "batch_version", "claim_epoch", "correlation_id")}}
        return self.request("POST", "/internal/v1/matching/runs", event["correlation_id"], body,
                            "REQUEST_SUBMITTED:" + event["event_id"])

    def run(self, run_id, event, action=None, body=None):
        require(isinstance(run_id, str) and UUID.fullmatch(run_id))
        require(action in (None, "result", "refresh"))
        path = "/internal/v1/matching/runs/" + run_id + ("/" + action if action else "")
        return self.request("POST" if action else "GET", path, event["correlation_id"], body)


def check_result(result, event, run_id, output=None):
    require(isinstance(result, dict))
    require(result.get("run_id") == run_id and result.get("batch_id") == event["batch_id"])
    require(result.get("correlation_id") == event["correlation_id"] and type(result.get("replay")) is bool)
    if result.get("disposition") == "SKIPPED":
        require(result.get("code") == "STATE_CONFLICT")
        return "SKIPPED"
    require(result.get("disposition") == "COMMITTED")
    require(isinstance(result.get("decision_id"), str) and UUID.fullmatch(result["decision_id"]))
    require(result.get("claim_epoch") == event["claim_epoch"])
    require(type(result.get("evaluated_count")) is int and type(result.get("eligible_count")) is int)
    require(0 <= result["eligible_count"] <= result["evaluated_count"] <= 2**32 - 1)
    matched = result.get("outcome") == "MATCHED"
    require(result.get("outcome") in ("MATCHED", "NO_MATCH") and matched == (result["eligible_count"] > 0))
    require(result.get("batch_status") == ("MATCHED" if matched else "SUBMITTED"))
    require(type(result.get("committed_batch_version")) is int and
            1 <= result["committed_batch_version"] <= 2**32 - 1 and
            result["committed_batch_version"] == event["batch_version"] + matched)
    if output is not None:
        require(all(result.get(k) == output[k] for k in ("decision_id", "outcome", "evaluated_count", "eligible_count")))
    return "COMMITTED"


def check_run(response, event, run_id=None, output=None):
    require(isinstance(response, dict))
    identity = response.get("run_id")
    require(isinstance(identity, str) and UUID.fullmatch(identity))
    require(run_id is None or run_id == identity)
    if response.get("phase") == "COMPLETED":
        return check_result(response.get("result"), event, identity, output), response
    require(response.get("phase") == "PREPARED")
    context = validate_input(response.get("prepared_context"))
    require(context["run_id"] == identity and context["trigger_id"] == event["event_id"]
            and context["trigger_type"] == "REQUEST_SUBMITTED" and context["correlation_id"] == event["correlation_id"])
    require(all(context["batch"][key] == event[key] for key in ("batch_id", "batch_version", "claim_epoch")))
    source_fields = ("organization_id", "submitted_at", "category", "quantity", "estimated_weight_kg",
                     "condition_rating", "is_data_bearing", "zone", "collection_deadline")
    require(all(context["batch"][key] == event["data"][key] for key in source_fields))
    return "PREPARED", context
