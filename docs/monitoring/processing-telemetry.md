# Processing telemetry v1

Implementation contract for the API/outbox and current Python Kafka ingestion
component. Team sign-off and deployed Azure query execution are not claimed.

| Field                                          | Meaning                                                                              |
| ---------------------------------------------- | ------------------------------------------------------------------------------------ |
| telemetry_version, service, observation        | 1; api/analytics; stable signal name                                                 |
| operation                                      | HTTP route template or event type                                                    |
| batch_id, event_id, command_id, correlation_id | Original processing identities when applicable                                       |
| outcome, code                                  | Stable success/rejection/failure/retry/quarantine classification                     |
| duration_ms                                    | Nonnegative elapsed stage time; not end-to-end workflow latency                      |
| retry_count, attempt                           | Previous processing retries and current outbox attempt                               |
| consumer_group, topic, partition, offset       | Delivery identity                                                                    |
| committed_offset, end_offset, consumer_lag     | Next committed offset, read-committed visible end, librdkafka committed consumer_lag |
| sample_available, ready                        | Explicit availability/readiness; unknown is never zero or healthy                    |

HTTP logging retains existing fields and adds route, batch, correlation, outcome,
error code and millisecond latency. The shared error writer sets the stable API
code. Outbox logs include all identifiers, attempt, duration and the outcome of
persisting publish/retry/quarantine state. Failed persistence must not log success.
No request bodies, tokens, passwords, evidence contents/keys or raw exceptions
are introduced by these structured observations.

Python instrumentation extends the deployed worker under `src/matcher`. It keeps
existing pause/retry/idempotency/offset handling and health decisions. Every bounded
PREPARE/EVALUATE/COMMIT/RESOLVE operation emits `processing_step` duration and outcome.
The existing statistics callback updates health every second and emits lag/readiness
at most once per 30s. Lag uses librdkafka's committed-offset consumer_lag and
read-committed visible end (ls_offset); unknown negative offsets remain UNKNOWN.
Only currently owned partitions are reported, avoiding duplicate replica totals.
`/readyz` also logs each probe result without changing its existing 200/503 behavior.
Receipt/treatment analytics integration remains the component owner's responsibility;
these hooks instrument the existing worker, not an invented new processing service.

## Checks and operator workflow

- `cd src/backend && go test ./internal/middleware ./internal/outbox ./internal/response`
  checks request correlation, readiness failure and outbox success/retry/quarantine/
  persistence failure telemetry.
- `PYTHONPATH=src/matcher python -m unittest discover -s src/matcher/tests -v`
  (install the existing pinned src/matcher requirements) checks safe field selection, known/
  unknown lag, sampling failure, readiness 200/503, facade failure/retry and the
  existing offset-commit recovery suite. Unit mocks exercise failure paths; the
  separate Kafka integration suite is required for live broker evidence.
- Enable console logs in the ACA environment's Log Analytics destination. Run each
  complete .kql file independently and adjust AppPrefix/Lookback. Queries support
  legacy ContainerAppConsoleLogs_CL and resource-specific ContainerAppConsoleLogs.
  A warning about the unused table is expected when only one destination exists.
- Use processing-trace for a correlation ID; failures-retries for stable codes;
  latency for separate stage percentiles; consumer-lag for latest per-partition
  committed lag; readiness for failures AND missing/stale telemetry. No rows is
  not evidence of health. Inspect replica/system logs when console signals stop.
- Quarantined events require contract/data repair before an authorised redrive;
  transient outbox failures remain pending. A publish acknowledgment persistence
  failure may redeliver: consumer/result idempotency remains required.
- Component owners integrate these fields when adding receipt/treatment/result
  handlers. Confirm shared names before merging; event/command IDs must originate
  from the real transaction, never be fabricated solely for monitoring.

Azure table reference: https://learn.microsoft.com/en-us/azure/container-apps/log-monitoring
Resource-specific table: https://learn.microsoft.com/en-us/azure/azure-monitor/reference/tables/containerappconsolelogs
KQL files are provided for deployment verification; this package makes no claim
that they were executed against your Azure workspace or that alerts were deployed.

Lag field reference: https://github.com/confluentinc/librdkafka/blob/master/STATISTICS.md
