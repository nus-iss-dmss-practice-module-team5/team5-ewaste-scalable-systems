# Sprint 3 fake integration test report

Date: 2026-10-06
Branch: `feature/EWCSB-170`
Scope: EWCSB-164, EWCSB-166, EWCSB-167, EWCSB-170

## Test

`internal/service/sprint3_integration_test.go` — `TestFakeSprint3ReceiptTreatmentAnalyticsCompletionFlow`

The test uses the real Go receipt, treatment, and analytics services with the existing transactional fake repository. It replaces external infrastructure with:

- an in-memory fake Kafka broker with Go-to-Python and Python-to-Go channels;
- a Go test double of the planned Python analytics worker that consumes `RecyclingCompleted`, independently reconstructs the canonical frozen-input JSON, calculates the SHA-256 input hash, and calls the Go analytics acknowledgement façade;
- the fake repository transaction to verify durable atomic effects.

## Exercised flow

```text
COLLECTED (v5)
  -> VerifyReceipt
  -> VERIFIED (v6) + ReceiptVerified
  -> RecordTreatment with missing outcomes
  -> RECYCLED (v7) + RecyclingCompleted
  -> fake Kafka
  -> mock Python worker
  -> POST-equivalent analytics acknowledgement
  -> COMPLETED (v8) + AnalyticsCompleted audit + RequestCompleted
```

## Assertions

- Receipt and treatment events are persisted in order on `ewaste.batch.events`.
- Kafka partition key equals the batch ID and event schema version is `1`.
- The worker test double receives the frozen treatment event rather than reading live tables.
- The independently implemented Go test double and service calculate the same frozen-input hash; this is not Python execution evidence.
- Only the analytics service scope acknowledges completion.
- `MISSING` treatment data produces `MISSING_OUTCOME` and does not block completion after a valid acknowledgement.
- The final state is `COMPLETED` at aggregate version `8`.
- Analytics result, audit record, and `RequestCompleted` outbox row are durable together.

## Result

```text
PASS — TestFakeSprint3ReceiptTreatmentAnalyticsCompletionFlow
```

Commands used:

```text
go test ./internal/service -run TestFakeSprint3ReceiptTreatmentAnalyticsCompletionFlow -v
go test ./...
go vet ./...
```

This is a deterministic contract/integration test. It does not replace staging evidence with real MySQL, Redis, Kafka/Event Hubs, Azure Blob Storage, or the deployed Python worker. Those environments still require an end-to-end run and traceability links before the Jira items can be administratively marked Done.

## Preparation and deployment contract

`GET /api/v1/batches/{batch_id}/analytics-input?source_event_id={event_id}` uses the same analytics service authentication as result acknowledgement. It returns the committed `RecyclingCompleted` snapshot as `input_canonical_json`, its SHA-256 `input_hash`, source identity/version, correlation ID and pinned `rule_version`. Hash the exact UTF-8 string before parsing it. Preparation writes no commands or results and can be repeated after completion.

`EWASTE_ANALYTICS_APPROVED_RULE_VERSION` selects the policy for new treatment events. Preparation, result validation and replay use the policy stored in that event, even after configuration changes. Results must retain the returned source/version/hash/policy and use `If-Match-Version: <source_event_version>`, including replay. Existing source events without a policy must be reviewed before rollout; do not silently label or rehash previously published events using the current configuration.

Set the GitHub environment secret `ANALYTICS_SERVICE_TOKEN` to a separate random value of at least 32 characters. CD wires it to the API as `EWASTE_AUTH_ANALYTICS_SERVICE_TOKEN` and to the worker as `ANALYTICS_SERVICE_TOKEN`, then probes the authenticated preparation route. This configures the boundary; it does not implement or demonstrate the deployed Sprint 3 Python consumer.

## Real MySQL regression checks

Use a disposable database named `processing_test`, apply the main Liquibase changelog with the `seed` context, and run from `src/backend`:

```sh
PROCESSING_TEST_DSN='root@tcp(127.0.0.1:3306)/processing_test?parseTime=true&loc=UTC' \
  go test -race -v ./internal/service -run TestProcessingMySQL
```

The complete and missing-outcome cases exercise the actual GORM repository from a collected batch with a completed assignment. They verify receipt/treatment/result persistence, read-only preparation, replay, cross-organisation rejection, rollback after late outbox failure, and publication validation of all three events read back from MySQL at microsecond precision. Each case rolls back its outer transaction. No Azure or broker traffic is produced.
