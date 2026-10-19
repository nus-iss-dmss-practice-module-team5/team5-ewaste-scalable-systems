# Operational Runbook: CI/CD Pipeline & End-to-End Application Testing

**Platform:** Enterprise E-Waste Recycling & Chain-of-Custody Tracking System
**Environment:** Azure Malaysia West (`rg-ewastescalable-dev`)
**Architecture:** Zero-Trust Private VNet Delegation, Microservices on Azure Container Apps (ACA), Private MySQL Flexible Server, Kafka/Redis Outbox Messaging.

---

## Table of Contents

1. [Architecture & Environment Topology](#1-architecture--environment-topology)
2. [CI/CD Pipeline Operations](#2-cicd-pipeline-operations)
   - [Workflow Matrix](#workflow-matrix)
   - [Pipeline 1: Infrastructure Deployment (Terraform)](#pipeline-1-infrastructure-deployment-terraform)
   - [Pipeline 2: CI Quality Gate & Security Baseline](#pipeline-2-ci-quality-gate--security-baseline)
   - [Pipeline 3: Private Database Migration (Liquibase)](#pipeline-3-private-database-migration-liquibase)
   - [Pipeline 4: Continuous Delivery (Build, Push & Deploy)](#pipeline-4-continuous-delivery-build-push--deploy)
3. [Test Personas & System Credentials](#3-test-personas--system-credentials)
4. [End-to-End Functional Testing (MVP Walkthrough)](#4-end-to-end-functional-testing-mvp-walkthrough)
   - [Phase 1: Donor Submits Batch](#phase-1-donor-submits-batch)
   - [Phase 2: Event-Driven Matching Engine Processing](#phase-2-event-driven-matching-engine-processing)
   - [Phase 3: Recycler Claim Flow](#phase-3-recycler-claim-flow)
   - [Phase 4: Concurrency Lock Verification (409 Conflict)](#phase-4-concurrency-lock-verification-409-conflict)
   - [Phase 5: Collector Logistics & Custody Finalization](#phase-5-collector-logistics--custody-finalization)
5. [Diagnostics & Troubleshooting Runbook](#5-diagnostics--troubleshooting-runbook)
6. [Rollback & Disaster Recovery Overview](#6-rollback--disaster-recovery-overview)

---

## 1. Architecture & Environment Topology

### Network Layout (`vnet-ewastescalable-dev` - `10.0.0.0/16`)

| Subnet Name              | Address Range | Purpose & Access Model                                                                                                            |
| :----------------------- | :------------ | :-------------------------------------------------------------------------------------------------------------------------------- |
| `snet-aca`               | `10.0.8.0/21` | Delegated to Azure Container Apps environment (`cae-ewastescalable-dev`).                                                         |
| `snet-mysql`             | `10.0.2.0/24` | **100% Private Delegation** to `Microsoft.DBforMySQL/flexibleServers`. Zero public IP.                                            |
| `snet-redis`             | `10.0.1.0/24` | Private Endpoint for Redis Cache.                                                                                                 |
| `snet-private-endpoints` | `10.0.3.0/24` | Private Endpoints for Key Vault and Shared ACR.                                                                                   |
| `snet-runner`            | `10.0.4.0/24` | Self-hosted GitHub Actions VM runner (`vm-runner-ewastescalable-dev`). Inbound strictly restricted to long-poll egress to GitHub. |

### Active Service Endpoints

- **Frontend UI:** `https://aca-ewastescalable-dev-ui.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
- **Backend API Gateway:** `https://aca-ewastescalable-dev-api.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
  - Health Endpoint: `/healthz`
  - Internal RPC: `/internal/v1/*` (Protected by workload bearer token)
- **Analytics Worker:** `https://aca-ewastescalable-dev-analytic.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
  - Readiness Endpoint: `/readyz`

---

## 2. CI/CD Pipeline Operations

### Workflow Matrix

```
       [Developer Push / PR]
                 │
                 ▼
      ┌─────────────────────┐
      │     ci-gate.yml     │  (Pre-commit, Gitleaks, Unit Tests, Checkov)
      └──────────┬──────────┘
                 │ (Merge to dev)
                 ▼
 ┌───────────────────────────┐
 │   terraform-infra-create.yml│  (Provisions/updates Azure resources)
 └───────────┬───────────────┘
                 │
                 ▼
 ┌───────────────────────────┐
 │    database-migration.yml   │  (Runs on Self-Hosted Runner inside VNet)
 └───────────┬───────────────┘
                 │
                 ▼
 ┌───────────────────────────┐
 │        cd-pipeline.yml      │  (ACR Build -> Deploy ACA API, UI & Worker)
 └───────────────────────────┘
```

---

### Pipeline 1: Infrastructure Deployment (Terraform)

- **Workflow:** `.github/workflows/terraform-infra-create.yml`
- **Trigger:** Dispatch or changes in `terraform/**`.
- **Execution Environment:** GitHub-hosted Ubuntu runner using Azure OIDC credentials.
- **Key Tasks:**
  1. Checks out repository and initializes remote state in Azure Blob Storage (`tfstate-rg`).
  2. Runs Checkov static analysis across all `.tf` modules.
  3. Executes `terraform apply` targeting `terraform/shared` and `terraform/environments`.
  4. Automatically provisions delegated subnets, Private DNS zones, Container Apps environment, and private MySQL Flexible Server.

---

### Pipeline 2: CI Quality Gate & Security Baseline

- **Workflow:** `.github/workflows/ci-gate.yml`
- **Trigger:** Pull Requests targeting `dev` or `main`; pushes to `dev` or `main`.
- **Gate Checks:**
  1. **Pre-commit Quality Checks:** Linting, whitespace, markdown formatting.
  2. **Gitleaks Secret Detection:** Prevents credential leaks in commits.
  3. **Backend Service Gate:** Go 1.26 tests (`go test -race ./...`) and `golangci-lint`.
  4. **Frontend UI Gate:** Node.js 22 linting, unit test suite (`npm run test`), and bundle build (`npm run build`).
  5. **Matcher Analytics Gate:** Python 3.11 `pytest` with enforced minimum code coverage threshold.
  6. **Checkov Security Gate:** Enforces zero high/critical vulnerabilities across IaC templates.

---

### Pipeline 3: Private Database Migration (Liquibase)

- **Workflow:** `.github/workflows/database-migration.yml`
- **Trigger:** Dispatch or push modifying `database/**`.
- **Execution Environment:** **Self-Hosted Runner (`runs-on: self-hosted`)** inside `snet-runner`.
- **Key Tasks:**
  1. **Pre-flight Healthcheck:** Validates Azure login, retrieves DB credentials from Azure Key Vault, and verifies `ewastedb` exists.
  2. **Liquibase Migration:** Executes Liquibase Docker container directly on the internal VNet.
  3. **Contexts:** Applies changesets for `--contexts=schema,seed`.
  4. Applies changesets `001` through `025` and seed files `101` through `105`.

---

### Pipeline 4: Continuous Delivery (Build, Push & Deploy)

- **Workflow:** `.github/workflows/cd-pipeline.yml`
- **Trigger:** Push to `dev` or manual workflow dispatch.
- **Execution Environment:** Self-hosted runner for zero-network-egress deployment.
- **Key Tasks:**
  1. **ACR Authentication:** Authenticates using Azure User-Assigned Managed Identity via OIDC (No admin passwords).
  2. **Immutable Image Build:** Builds three immutable container images:
     - `workflow-api:dev-<sha>`
     - `workflow-ui:dev-<sha>`
     - `workflow-analytics:dev-<sha>`
  3. **Container App Revisions:** Deploys new revisions using `az containerapp update` with zero-downtime rolling updates.
  4. **Matcher Integration Script:** Executes `scripts/deploy-matcher.sh` to bind the worker to the API internal endpoint and validates `/readyz` health.

---

## 3. Test Personas & System Credentials

All mock accounts share the unified test password:

> **Default Test Password:** `TestPassword123!`

| Role           | Username / Email         | Organization            | Org ID      | Capabilities / Responsibilities                                                                                                                 |
| :------------- | :----------------------- | :---------------------- | :---------- | :---------------------------------------------------------------------------------------------------------------------------------------------- |
| **Donor**      | `donor1@ewaste.test`     | GreenCorp Tech          | `ORG-001`   | Creates, edits, and submits e-waste disposal batches.                                                                                           |
| **Recycler 1** | `recycler1@ewaste.test`  | EcoCycle Processors     | `PROC-001`  | Claims batches in `NORTH`, `SOUTH`, `EAST`, `WEST`, `CENTRAL`. Handles `ICT_EQUIPMENT`, `LARGE_APPLIANCE`, `BATTERIES`, `CONSUMER_ELECTRONICS`. |
| **Recycler 2** | `recycler2@ewaste.test`  | RenewTech Solutions     | `PROC-002`  | Competitor recycler. Used for concurrency conflict testing (409).                                                                               |
| **Collector**  | `collector1@ewaste.test` | GreenHaul Logistics     | `COL-001`   | Linked to `PROC-001`. Finalizes pickup completion or logs pickup failure.                                                                       |
| **Admin**      | `admin@ewaste.test`      | State Regulatory Agency | `ADMIN-001` | System audit, global batch oversight, matching rule inspection.                                                                                 |

---

## 4. End-to-End Functional Testing (MVP Walkthrough)

### Deliverable MVP Acceptance Flow

```
[Donor Submit] ──► [Matcher Worker] ──► [Recycler Claim] ──► [Collector Pickup]
 (SUBMITTED)         (MATCHED)            (CLAIMED)          (COLLECTED / FAILED)
```

---

### Phase 1: Donor Submits Batch

1. Open the UI: `https://aca-ewastescalable-dev-ui.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
2. Log in as **`donor1@ewaste.test`** / `TestPassword123!`.
3. Click **\"New Batch\"** and fill in valid parameters:
   - **Category:** `ICT_EQUIPMENT`
   - **Estimated Weight (kg):** `150.50` _(Maximum 2 decimal places, 0.10 to 50000.00)_
   - **Quantity:** `10` _(Integer, 1 to 100000)_
   - **Condition:** `REPAIRABLE`
   - **Data Bearing:** `Yes`
   - **Collection Zone:** `NORTH`
   - **Collection Deadline:** Choose a date between **48 hours** and **90 days** in the future.
4. Click **\"Submit Batch\"** .
5. **Expected Result:**
   - Status updates to **`SUBMITTED`**.
   - Batch is written to `ewaste_batches` and an event is queued in Kafka topic `ewaste.batch.events`.

---

### Phase 2: Event-Driven Matching Engine Processing

1. The analytics container (`aca-ewastescalable-dev-analytic`) polls the Kafka topic.
2. The worker extracts the batch event and queries `POST /internal/v1/matching/runs`.
3. The matching engine evaluates the 5 business rules:
   - `M1` (Category Capability): `PROC-001` supports `ICT_EQUIPMENT`.
   - `M2` (Condition & Data): `PROC-001` accepts `REPAIRABLE` and supports data wipe.
   - `M3` (Capacity Pool): Pool capacity (`50000 kg`) >= `150.50 kg`.
   - `M4` (Zone Coverage): `NORTH` is active in service zones.
   - `M5` (Feasibility): Evaluation timestamp + minimum lead time <= Collection deadline.
4. **Expected Result:**
   - Matcher successfully posts run decision.
   - Batch transitions from `SUBMITTED` to **`MATCHED`**.
   - An opportunity record is created for `PROC-001`.

---

### Phase 3: Recycler Claim Flow

1. Log out and log in as **`recycler1@ewaste.test`** / `TestPassword123!`.
2. Navigate to **\"Matched Opportunities\"** (`/opportunities`).
3. Locate the batch submitted in Phase 1.
4. Click **\"Claim Opportunity\"**.
5. Select default collector: **`COL-001 (GreenHaul Logistics)`**.
6. Submit the claim.
7. **Expected Result:**
   - Batch status transitions to **`CLAIMED`**.
   - Assigned Recycler is set to `PROC-001`.
   - Assigned Collector is set to `COL-001`.

---

### Phase 4: Concurrency Lock Verification (409 Conflict)

To prove the MVP requirement: _\"Exactly one concurrent claim succeeds\"_:

1. Open an Incognito window or second browser.
2. Log in as **`recycler2@ewaste.test`** / `TestPassword123!`.
3. Attempt to call the claim endpoint for the exact same batch ID.
4. **Expected Result:**
   - Backend rejects the request with HTTP **`409 Conflict`**.
   - Message: `BATCH_ALREADY_CLAIMED` or `CLAIM_EPOCH_MISMATCH`.
   - The database record remains assigned exclusively to `PROC-001`.

---

### Phase 5: Collector Logistics & Custody Finalization

1. Log in as **`collector1@ewaste.test`** / `TestPassword123!`.
2. Navigate to **\"My Pickups\"**.
3. Select the claimed batch.

#### Scenario A: Successful Pickup

1. Click **\"Complete Pickup\"**.
2. Enter:
   - **Gross Weight (kg):** `152.00`
   - **Weighbridge / Scale Ticket Reference:** `WT-99482`
   - **Donor Handover Signature / Notes:** `Confirmed by site manager.`
3. Submit handover.
4. **Expected Result:** Batch status transitions to **`COLLECTED`**. Chain of custody is finalized.

#### Scenario B: Pickup Exception / Failure

1. If the premises are closed or materials mismatch: Click **\"Report Pickup Failure\"** .
2. Select reason: `PREMISES_UNREACHABLE` or `HAZARDOUS_MISMATCH`.
3. Submit report.
4. **Expected Result:** Batch status transitions to **`PICKUP_FAILED`**. Alert is triggered for donor re-coordination.

---

## 5. Diagnostics & Troubleshooting Runbook

### 1. Direct MySQL Query via Runner VM

Because MySQL Flexible Server has zero public IP, execute ad-hoc SQL checks via the runner host using `az vm run-command`:

```bash
az vm run-command invoke \
  --resource-group rg-ewastescalable-dev \
  --name vm-runner-ewastescalable-dev \
  --command-id RunShellScript \
  --scripts "docker run --rm mysql:8.0 mysql -h mysql-ewastescalable-dev.ewaste-dev.mysql.database.azure.com -u ewasteadmin -p'7vR9qL2wT8xN4bK1pM5yZ3jD' ewastedb -e 'SELECT id, status, category, estimated_weight_kg FROM ewaste_batches ORDER BY created_at DESC LIMIT 5;'" \
  --query "value[0].message" -o tsv
```

### 2. Inspecting Real-Time Container Logs

To monitor backend and analytics logs during live demonstrations:

```bash
# Backend API Request & Error Logs
az containerapp logs show \
  --name aca-ewastescalable-dev-api \
  --resource-group rg-ewastescalable-dev \
  --tail 50 \
  --follow

# Matcher Analytics Worker Event Processing Logs
az containerapp logs show \
  --name aca-ewastescalable-dev-analytic \
  --resource-group rg-ewastescalable-dev \
  --tail 50 \
  --follow
```

### 3. Common Error Signatures & Remediation

| Symptom                                            | Root Cause                                                 | Remediation Step                                                                             |
| :------------------------------------------------- | :--------------------------------------------------------- | :------------------------------------------------------------------------------------------- |
| `VALIDATION_ERROR: one or more fields are invalid` | Collection deadline < 48 hours or weight decimals > 2.     | Set collection date at least 3 days into the future; ensure weight has max 2 decimal places. |
| `503 UNSUPPORTED_RULE_SET`                         | `matching_rule_sets` table has no active records.          | Run seed changeset `105-seed-matching-fixtures.sql` via Liquibase.                           |
| Recycler sees 0 opportunities                      | Capabilities, pools, or zones not seeded for recycler org. | Verify recycler records in `recycler_category_capabilities` and `recycler_service_zones`.    |
| Concurrency test returns 500 instead of 409        | Transaction isolation or lock missing.                     | Verify `claim_epoch` conditional update in `store.go:ClaimOpportunity()`.                    |

---

## 6. Rollback & Disaster Recovery Overview

The platform implements an **Atomic Blue/Green Revision Gating & Smart Fast-Path Rollback** architecture designed for `< 2 minutes` MTTR, zero downtime, and zero cloud resource waste.

> [!IMPORTANT]
> **Dedicated Runbook:** For the comprehensive architectural trade-off analysis (Atomic Blue/Green vs. Canary), step-by-step dispatch walkthroughs, out-of-band Azure CLI commands, Liquibase schema rollback protocols, and the official rehearsal sign-off checklist, refer directly to the dedicated runbook:
> 📄 **[ROLLBACK_AND_DISASTER_RECOVERY_RUNBOOK.md](file:///C:/Users/laksh/Documents/NUS-ISS%20MTech%20SE/1%20-%20SWE5006%20-%20Designing%20Modern%20Software%20Systems/Practice%20Module/GitHub%20Codebase/team5-ewaste-platform/docs/ROLLBACK_AND_DISASTER_RECOVERY_RUNBOOK.md)**

### Key SLA & Operational Guarantees

- **MTTR Target:** `< 2 minutes` from regression detection to active stable baseline.
- **Smart Zero-Lookup Dispatch:** Operators enter `PREVIOUS` (or `N-1`) under `rollback_tag` in GitHub Actions. The pipeline autonomously queries ACR for the preceding verified image (`sed -n '2p'`), skipping manual catalog searches.
- **Build Bypass:** Docker build and image push steps are bypassed entirely; verified immutable images are pulled directly from ACR.
- **Atomic Zero-Downtime Cutover:** Azure Container Apps switches 100% traffic only after new revision readiness probes (`/healthz`, UI root, `/readyz`) return HTTP 200.
- **Retention Protection:** Historical container image pruning is automatically disabled during rollback operations (`is_rollback == 'true'`).

### Quick Reference: Fast-Path Rollback Trigger

1. Navigate to GitHub **Actions** $\rightarrow$ **`CD - Multi-Environment Deployment & Evidence Pipeline`**.
2. Click **Run workflow**:
   - **Target Azure Runtime Environment:** `dev`, `stg`, or `prod`.
   - **Optional: Image tag to rollback to:** Enter **`PREVIOUS`** (or an explicit tag e.g. `dev-1162d6b`).
3. Click **Run workflow** (Recovers in $\approx 90$ seconds).
