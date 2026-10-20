# Approved matching contracts

These files were copied without modification from the supplied `handoff.zip`:

- `contracts/` and `api/`: revised `EWCSB-2_Companion_Files.zip`.
- `kafka/`: `Task5_Kafka_Companion_Files.zip`, including the preserved C2 schemas.

`approved-contracts.sha256` records the imported file bytes. The test runner
verifies them before running. The original API README and interface docstrings
describe the approved design package; current implementation status and evidence
are documented in [the matcher README](../../src/matcher/README.md).

The approved API is implemented by the Go [matching package](../../src/backend/internal/matching/README.md).
The worker, real Kafka/MySQL verification, and Azure integration are described in
the matcher README. The imported design documents are retained verbatim; their
original implementation-status wording is historical.
