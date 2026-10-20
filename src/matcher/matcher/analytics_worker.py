"""Processing consumer: acknowledge Kafka only after Go confirms durable completion."""
import time
from urllib.parse import quote, urlencode

from .analytics import acknowledgement, check_completion, source_event
from .contract import ContractError
from .facade import FacadeClient, FacadeError
from .worker import Processor


class AnalyticsFacade(FacadeClient):
    def prepare(self, event):
        path = "/api/v1/batches/" + quote(event["batch_id"], safe="") + "/analytics-input?"
        return self.request("GET", path + urlencode({"source_event_id": event["event_id"]}),
                            event["correlation_id"], preserve_correlation=True)

    def commit(self, event, output):
        path = "/api/v1/batches/" + quote(event["batch_id"], safe="") + "/analytics-results"
        return self.request("POST", path, event["correlation_id"], output,
                            "analytics-v1:" + event["event_id"], expected_version=event["aggregate_version"],
                            preserve_correlation=True)


class AnalyticsProcessor(Processor):
    """Reuse delivery/backoff convention, with no unapproved processing DLQ contract.

    Invalid/unsupported records and permanent API rejections block their partition
    and readiness. Operators repair the cause; we never silently discard an event.
    An uncertain POST is replayed with exactly the same key, body and source version.
    """
    def __init__(self, facade, *, max_bytes, retry_base, retry_max, observe):
        super().__init__(facade, None, max_bytes=max_bytes, retry_base=retry_base,
                         retry_max=retry_max, max_refreshes=0, observe=observe)

    def step(self, delivery):
        started, operation = time.monotonic(), delivery.stage
        delay = 0
        try:
            if delivery.stage == "VALIDATE_EVENT":
                delivery.event = source_event(delivery.record.value, delivery.record.key, self.max_bytes)
                if delivery.event is None:
                    delivery.stage, delivery.disposition = "DONE", "IGNORED"
                else:
                    delivery.stage = "PREPARE"
            elif delivery.stage == "PREPARE":
                delivery.context = self.facade.prepare(delivery.event)
                delivery.stage = "EVALUATE"
            elif delivery.stage == "EVALUATE":
                delivery.output = acknowledgement(delivery.context, delivery.event)
                delivery.run_id = delivery.output["analytics_run_id"]
                delivery.stage = "COMMIT"
            elif delivery.stage == "COMMIT":
                result = self.facade.commit(delivery.event, delivery.output)
                check_completion(result, delivery.event, delivery.output)
                delivery.stage, delivery.disposition = "DONE", "COMMITTED"
            delivery.failure_streak = 0
            delivery.error_code = None
        except FacadeError as exc:
            # Fixed codes only: never echo API bodies or exception strings into logs.
            code = ("ANALYTICS_AUTH_FAILED" if exc.status in (401, 403) else
                    "ANALYTICS_FACADE_UNAVAILABLE" if exc.status >= 500 or exc.status == 429 else
                    "ANALYTICS_FACADE_REJECTED")
            delivery.error_code = code
            delay = self._pause(delivery, code, exc.retry_after)
        except ContractError as exc:
            delivery.error_code = exc.code
            if delivery.stage == "EVALUATE":
                # Fetch again after a façade/configuration repair; do not cache a bad response forever.
                delivery.stage = "PREPARE"
            delay = self._pause(delivery, exc.code)
        self.observe("processing_step", delivery, operation="analytics_" + operation.lower(),
                     outcome="RETRYING" if delay else "SUCCEEDED", duration_ms=(time.monotonic() - started) * 1000)
        return delay
