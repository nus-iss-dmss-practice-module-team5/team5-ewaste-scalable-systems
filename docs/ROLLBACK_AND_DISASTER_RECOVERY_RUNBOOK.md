# Operational Runbook: Rollback Strategy & Disaster Recovery Protocol

**System:** Enterprise E-Waste Recycling & Chain-of-Custody Tracking Platform
**Target Environments:** Azure Malaysia West (`rg-ewastescalable-dev`, `rg-ewastescalable-stg`, `rg-ewastescalable-prod`)
**Target SLA:** Mean Time to Recovery (MTTR) `< 2 minutes` | Zero Downtime | 100% Traffic Determinism
**Document Version:** 1.0 (Production & Academic Baseline)

### Active Service Endpoints (Development Environment)

- **Frontend Portal (UI):** `https://aca-ewastescalable-dev-ui.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
- **Backend API Gateway:** `https://aca-ewastescalable-dev-api.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
  - Health Endpoint: `/healthz`
  - Login Endpoint: `/api/v1/auth/login`
- **Analytics & Matching Worker:** `https://aca-ewastescalable-dev-analytics.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io`
  - Readiness Endpoint: `/readyz`

---

## Table of Contents

1. [Executive Summary & Operational SLA](#1-executive-summary--operational-sla)
2. [Architectural Trade-Off Analysis: Atomic Blue/Green vs. Canary Delivery](#2-architectural-trade-off-analysis-atomic-bluegreen-vs-canary-delivery)
3. [Rollback Architecture & Mechanics](#3-rollback-architecture--mechanics)
   - [Supported Rollback Trigger Modes](#supported-rollback-trigger-modes)
   - [Automated Pipeline Execution Flow](#automated-pipeline-execution-flow)
   - [Immutable Artifact Retention Policy](#immutable-artifact-retention-policy)
4. [Step-by-Step Operational Runbook](#4-step-by-step-operational-runbook)
   - [Procedure A: One-Click Smart Rollback via GitHub Actions (Recommended)](#procedure-a-one-click-smart-rollback-via-github-actions-recommended)
   - [Procedure B: Explicit Tag Rollback](#procedure-b-explicit-tag-rollback)
   - [Procedure C: Emergency Out-of-Band Rollback via Azure CLI](#procedure-c-emergency-out-of-band-rollback-via-azure-cli)
5. [Data Tier Rollback Protocol (Liquibase)](#5-data-tier-rollback-protocol-liquibase)
6. [Post-Rollback Verification & Smoke Testing](#6-post-rollback-verification--smoke-testing)
7. [Rollback Rehearsal Audit Checklist](#7-rollback-rehearsal-audit-checklist)

---

## 1. Executive Summary & Operational SLA

In modern microservices architectures, the measure of deployment reliability is not the complete absence of bugs, but the **speed and predictability with which the platform can recover from an unpredicted regression**.

This runbook establishes the disaster recovery standard for the E-Waste platform across all runtime components:

- **Backend API Gateway:** Go 1.26 HTTP service running on Azure Container Apps.
- **Frontend Portal:** Next.js 14 SSR web application running on Azure Container Apps.
- **Matching Engine Worker:** Python 3.11 Kafka stream consumer and rules evaluator running on Azure Container Apps.
- **Relational Database:** Azure Database for MySQL Flexible Server (100% private VNet isolation).
- **Distributed Event Bus:** Azure Event Hubs (Kafka protocol surface).

### Recovery SLA Targets

| Metric                           | Target SLA               | Implementation Mechanism                                                                                                           |
| :------------------------------- | :----------------------- | :--------------------------------------------------------------------------------------------------------------------------------- |
| **Mean Time to Recovery (MTTR)** | `< 2 minutes`            | Pre-built immutable container digests pulled directly from Azure Container Registry (ACR), bypassing image build & test pipelines. |
| **Availability During Rollback** | `100% (Zero Downtime)`   | Azure Container Apps atomic revision traffic cutover executed only after readiness probes pass.                                    |
| **Operator Friction**            | `Zero-Lookup Input`      | Operator enters `PREVIOUS` or `N-1`; pipeline dynamically queries and resolves the prior stable release tag.                       |
| **Data Consistency**             | `Zero Schema Corruption` | Forward-only, additive schema migrations (Expand/Contract pattern) guarantee database compatibility across $N$ and $N-1$ builds.   |

---

## 2. Architectural Trade-Off Analysis: Atomic Blue/Green vs. Canary Delivery

When designing the progressive delivery architecture, our team evaluated two major deployment patterns:

1. **Progressive Canary Traffic Splitting (e.g., 10% Canary / 90% Baseline Traffic Split)**
2. **Atomic Blue/Green Revision Gating with Smart Rollback (Chosen Architecture)**

### Trade-Off Evaluation Matrix

| Architectural Dimension                     | Progressive Canary (10% / 90% Split)                                                                                                             | Atomic Blue/Green + Smart Rollback (Selected)                                                                              | Operational & Academic Rationale                                                                                                                                           |
| :------------------------------------------ | :----------------------------------------------------------------------------------------------------------------------------------------------- | :------------------------------------------------------------------------------------------------------------------------- | :------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Telemetry Statistical Validity**          | Requires high-volume production traffic ($>10^4$ requests/min) to compute statistically sound error rate and latency regressions.                | Relies on comprehensive, deterministic synthetic health gates (`/`, `/api/v1/auth/login`, `/readyz`) before cutover.       | For bounded academic demonstrations and scheduled evaluation reviews, traffic volume is discrete. Canary anomaly detection is statistically invalid on small sample sizes. |
| **Evaluation & Grading Consistency**        | Stochastic (probabilistic) routing. 90% of requests hit the legacy baseline; examiners have only a 10% probability of observing the new release. | Deterministic cutover. 100% of user traffic routes to the validated release immediately upon passing health gates.         | Eliminates user confusion where testers or grading examiners intermittently experience old bugs already fixed in the latest build.                                         |
| **Database Schema Drift**                   | Demands multi-stage Expand/Contract migrations. Baseline revision crashes if canary requires a newly added non-null column.                      | Liquibase migrations run as a synchronized precursor; atomic cutover ensures all traffic runs against a compatible schema. | Prevents runtime SQL column mismatch exceptions without requiring complex dual-version ORM compatibility layers.                                                           |
| **Event Stream / Consumer Group Integrity** | Running parallel revisions on Kafka topic `ewaste.batch.events` causes consumer group rebalances and split-brain message consumption.            | Exactly one active worker revision maintains a clean lease on `matching-worker-v1`, preserving message order.              | Eliminates race conditions and duplicate opportunity allocations across competing worker revisions.                                                                        |
| **Cloud Quota & Cost Control**              | Running concurrent revisions across 3 Container Apps doubles active vCPU/Memory allocations, burning Azure credits twice as fast.                | Keeps exactly one active revision per app (`revision_mode = "Single"`), maximizing Consumption scale-to-zero efficiency.   | Strictly bounds cloud expenditures within the allocated student Azure sponsorship credits ($100 budget).                                                                   |
| **Recovery Speed (MTTR)**                   | Requires automated traffic snapback and revision deactivation logic.                                                                             | Sub-2-minute rollback triggered via a single parameter input (`rollback_tag: PREVIOUS`), bypassing image rebuilds.         | Smart rollback reuses verified, immutable ACR digests directly, achieving low MTTR during live operations or demonstrations.                                               |

> **Academic Conclusion:** For an enterprise-grade academic platform operating on bounded cloud credits and subject to live presentation audits, **Atomic Blue/Green Revision Gating with Smart Rollback provides the optimal balance of zero-downtime resiliency, data consistency, and deterministic verification.**

---

## 3. Rollback Architecture & Mechanics

```
┌────────────────────────────────────────────────────────────────────────┐
│                      OPERATOR DISPATCH TRIGGER                         │
│               Input: rollback_tag = "PREVIOUS" or "N-1"                │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│ STAGE 1.0: DYNAMIC RESOLUTION (Runs on Self-Hosted VNet Runner)        │
│ 1. Connect to Azure Container Registry via Managed Identity            │
│ 2. Query repository tags ordered newest-first:                          │
│    az acr repository show-tags --orderby time_desc                     │
│ 3. Filter by environment prefix ($TARGET_ENV) and select 2nd entry     │
│ 4. Output: RESOLVED_TAG="dev-1162d6b" | IS_ROLLBACK=true               │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
         ┌──────────────────────────┴──────────────────────────┐
         ▼                                                     ▼
┌────────────────────────────────┐                    ┌────────────────────────────────┐
│ DOCKER BUILD BYPASS            │                    │ IMMUTABLE ARTIFACT PULL        │
│ Skip code compilation, unit    │                    │ Pull verified digests:         │
│ tests, and container packaging │                    │  - workflow-api:<tag>          │
│ (Saves 3 to 5 minutes)         │                    │  - workflow-ui:<tag>           │
│                                │                    │  - workflow-analytics:<tag>    │
└────────────────┬───────────────┘                    └────────────────┬───────────────┘
                 │                                                     │
                 └──────────────────────────┬──────────────────────────┘
                                            │
                                            ▼
┌────────────────────────────────────────────────────────────────────────┐
│ STAGE 1.1 - 1.3: CONTAINER APPS REVISION PROVISIONING                  │
│ az containerapp update --name aca-ewastescalable-$ENV-$SVC --image <digest>   │
│  - Azure Container Apps boots new container replica                    │
│  - Warm-up & healthcheck verification                                  │
│  - Atomic 100% traffic shift to rolled-back revision                   │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│ STAGE 1.4: LIVE HEALTH READINESS PROBES                                │
│ Verify live HTTP response codes across all tiers:                      │
│  - API Gateway:    GET https://$API_FQDN/ (HTTP < 500)                 │
│  - Frontend UI:    GET https://$UI_FQDN/  (HTTP < 500)                 │
│  - Matcher Worker: GET https://$WORKER_FQDN/readyz (HTTP 200)          │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│ STAGE 1.5: ACR RETENTION SAFEGUARD                                    │
│  - Pruning step SKIPPED (is_rollback == true)                          │
│  - Preserves candidate image history for future drills                 │
└────────────────────────────────────────────────────────────────────────┘
```

### Supported Rollback Trigger Modes

The pipeline (`.github/workflows/cd-pipeline.yml`) accepts the `rollback_tag` input under three operational modes:

| Mode                              | Input Value         | Pipeline Behavior                                                                                                                   | Use Case                                                               |
| :-------------------------------- | :------------------ | :---------------------------------------------------------------------------------------------------------------------------------- | :--------------------------------------------------------------------- |
| **Smart Zero-Lookup** _(Default)_ | `PREVIOUS` or `N-1` | Dynamically queries ACR and selects the second-newest image matching the target environment prefix (`dev-*`, `stg-*`, or `prod-*`). | Rapid incident recovery during live demos without inspecting ACR tags. |
| **Explicit Tag**                  | `dev-a1b2c3d`       | Deploys the exact tag requested by the operator.                                                                                    | Rolling back to a known-stable historical release or baseline tag.     |
| **Standard Build**                | _(Leave empty)_     | Compiles code, executes unit tests, and builds a fresh container tagged `${TARGET_ENV}-${GITHUB_SHA::7}`.                           | Normal CI/CD continuous delivery.                                      |

### Immutable Artifact Retention Policy

To guarantee that rollback candidates are never deleted:

1. **Automated Pruning:** In standard builds, the pipeline retains the **last 3 immutable tags** per repository (`workflow-api`, `workflow-ui`, `workflow-analytics`), deleting older revisions via `tail -n +4`.
2. **Rollback Guard:** During any rollback run (`is_rollback == 'true'`), the pruning step is **automatically bypassed**, ensuring that active recovery operations cannot delete rollback targets.

---

## 4. Step-by-Step Operational Runbook

### Procedure A: One-Click Smart Rollback via GitHub Actions (Recommended)

When an unexpected regression or demonstration issue occurs in the live environment:

1. **Open GitHub Actions:**
   - Navigate to the GitHub repository $\rightarrow$ Click on the **Actions** tab.
2. **Select CD Workflow:**
   - In the left workflow navigation pane, select **`CD - Multi-Environment Deployment & Evidence Pipeline`** (file: `.github/workflows/cd-pipeline.yml`).
3. **Trigger Workflow Dispatch:**
   - Click the **Run workflow** dropdown on the right side.
4. **Enter Inputs:**
   - **Use workflow from:** Select the branch deployed to the target environment (`main` for prod, `dev` for dev/stg).
   - **Target Azure Runtime Environment:** Select `dev`, `stg`, or `prod`.
   - **Optional: Image tag to rollback to:** Type **`PREVIOUS`**.
5. **Click `Run workflow`:**
   - The pipeline starts immediately.

#### What Happens in the Execution Logs:

- **Tag Resolution Step (`Resolve Effective Deployment Image Tags`):**
  ```text
  Smart Rollback requested: Querying ACR for previous (N-1) verified image tag...
  Successfully resolved N-1 rollback tag: dev-1162d6b
  is_rollback=true
  effective_tag=dev-1162d6b
  ```
- **Build Bypass Steps:**
  ```text
  Rolling back to existing API image: acrewasteplatform.azurecr.io/workflow-api:dev-1162d6b
  Rolling back to existing UI image: acrewasteplatform.azurecr.io/workflow-ui:dev-1162d6b
  Rolling back to existing Matcher image: acrewasteplatform.azurecr.io/workflow-analytics:dev-1162d6b
  ```
- **Traffic Cutover:** New revisions are spun up, probed for readiness, and receive 100% of live ingress traffic. Total elapsed time: **$\approx 90$ seconds**.

---

### Procedure B: Explicit Tag Rollback

If you need to roll back to a specific tagged milestone rather than the immediately preceding build:

1. **Identify the Candidate Tag:**
   - View the GitHub Actions Job Summary of the last successful deployment, which prints the **Verified Available Image Tags in ACR** catalog:
     | Service        | Latest Available Tags (Newest First)    | Target Environment |
     | :------------- | :-------------------------------------- | :----------------: |
     | `workflow-api` | `dev-1162d6b, dev-9859ea7, dev-4f81c20` |       `dev`        |
2. **Trigger Dispatch with Exact Tag:**
   - Enter the chosen tag (e.g., `dev-9859ea7`) in the `rollback_tag` input field.
   - Run the workflow.

---

### Procedure C: Emergency Out-of-Band Rollback via Azure CLI

If GitHub Actions is unavailable or inaccessible, the platform can be rolled back directly via the Azure CLI or Azure Cloud Shell:

```bash
# Set environment variables
TARGET_ENV="dev"
RG="rg-ewastescalable-${TARGET_ENV}"
ACR_NAME="acrewasteplatform"

# 1. Query the previous stable image tag for API
PREV_API_TAG=$(az acr repository show-tags \
  --name "$ACR_NAME" \
  --repository workflow-api \
  --orderby time_desc \
  --output tsv | grep "^${TARGET_ENV}-" | sed -n '2p')

echo "Rolling back to tag: $PREV_API_TAG"

# 2. Update Azure Container Apps revisions directly
az containerapp update \
  --name "aca-ewastescalable-${TARGET_ENV}-api" \
  --resource-group "$RG" \
  --image "${ACR_NAME}.azurecr.io/workflow-api:${PREV_API_TAG}"

az containerapp update \
  --name "aca-ewastescalable-${TARGET_ENV}-ui" \
  --resource-group "$RG" \
  --image "${ACR_NAME}.azurecr.io/workflow-ui:${PREV_API_TAG}"

az containerapp update \
  --name "aca-ewastescalable-${TARGET_ENV}-analytics" \
  --resource-group "$RG" \
  --image "${ACR_NAME}.azurecr.io/workflow-analytics:${PREV_API_TAG}"

# 3. Verify active revisions
az containerapp revision list \
  --name "aca-ewastescalable-${TARGET_ENV}-api" \
  --resource-group "$RG" \
  --query "[?properties.active].{Name:name, Created:properties.createdTime, Image:properties.template.containers[0].image}" \
  --output table
```

---

## 5. Data Tier Rollback Protocol (Liquibase)

Database migrations must adhere to strict backward-compatibility rules so that application rollbacks do not cause data tier failures:

### The Expand / Contract Migration Principle

1. **Never rename or drop columns** in a release that also modifies application code.
2. **All newly added columns must be nullable** or provide a safe database default value.
3. This guarantees that if the application layer rolls back from release $N$ to release $N-1$, the older application code simply ignores the new column and continues running without SQL errors.

### Rolling Back a Breaking Migration

If a database schema change itself introduced a critical regression and must be rolled back:

1. **Trigger via Liquibase Pipeline:**
   - Run `.github/workflows/database-migration.yml` with the rollback parameters.
2. **Emergency Rollback via Self-Hosted Runner VM:**
   Because MySQL Flexible Server has zero public IP and is strictly isolated inside `snet-mysql`, database commands must be executed from within the VNet:
   ```bash
   # Execute rollback of the last applied changeset using the Liquibase runner
   az vm run-command invoke \
     --resource-group rg-ewastescalable-dev \
     --name vm-runner-ewastescalable-dev \
     --command-id RunShellScript \
     --scripts 'docker run --rm \
       -v /home/azureuser/database:/liquibase/changelog \
       liquibase/liquibase \
       --changelog-file=changelog-master.yaml \
       --url="jdbc:mysql://mysql-ewastescalable-dev.ewaste-dev.mysql.database.azure.com:3306/ewastedb?useSSL=true&requireSSL=true" \
       --username="ewasteadmin" \
       --password="$DB_PASSWORD" \
       rollbackCount 1' \
     --query "value[0].message" -o tsv
   ```

---

## 6. Post-Rollback Verification & Smoke Testing

Immediately following a rollback, execute the standard verification checklist against the active environment endpoints:

### 1. Ingress & Probe Verification

```bash
API_FQDN="aca-ewastescalable-dev-api.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io"
UI_FQDN="aca-ewastescalable-dev-ui.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io"
WORKER_FQDN="aca-ewastescalable-dev-analytics.salmoncoast-1b1c8372.malaysiawest.azurecontainerapps.io"

# API Gateway check
curl -s -o /dev/null -w "API Status: %{http_code}\n" "https://${API_FQDN}/healthz"

# Frontend UI check
curl -s -o /dev/null -w "UI Status: %{http_code}\n" "https://${UI_FQDN}/"

# Analytics Worker readiness check
curl -s -o /dev/null -w "Matcher Status: %{http_code}\n" "https://${WORKER_FQDN}/readyz"
```

_Expected Result: All endpoints return HTTP 200._

### 2. Functional Authentication Smoke Test

```bash
curl -s -X POST "https://${API_FQDN}/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@ewaste.local","password":"Password123!"}'
```

_Expected Result: Returns HTTP 200 with JWT bearer token and user metadata._

### 3. Log Stream Verification

Ensure that containers are not crash-looping or generating 5xx errors:

```bash
az containerapp logs show \
  --name aca-ewastescalable-dev-api \
  --resource-group rg-ewastescalable-dev \
  --tail 30
```

---

## 7. Rollback Rehearsal Audit Checklist

To maintain operational readiness, a rollback drill should be rehearsed prior to major evaluation milestones. Record the audit results in this table:

| Verification Item            | Success Criteria                                          | Drill Status | Timestamp & Notes             |
| :--------------------------- | :-------------------------------------------------------- | :----------: | :---------------------------- |
| **1. Trigger Execution**     | Workflow triggered with `rollback_tag: PREVIOUS`          |   [ ] Pass   |                               |
| **2. Autonomous Resolution** | Pipeline dynamically identifies $N-1$ image from ACR      |   [ ] Pass   | Resolved Tag: `___________`   |
| **3. Build Bypass**          | Docker builds skipped; images pulled directly from ACR    |   [ ] Pass   | Build Duration: `0 min`       |
| **4. Zero-Downtime Shift**   | Traffic cuts over to rolled-back revision without 502/504 |   [ ] Pass   |                               |
| **5. Health Gates Pass**     | `/healthz`, UI root, and `/readyz` return HTTP 200        |   [ ] Pass   |                               |
| **6. Database Integrity**    | Liquibase changelog and business tables remain intact     |   [ ] Pass   |                               |
| **7. ACR Safety Guard**      | Candidate images preserved; pruning step skipped          |   [ ] Pass   |                               |
| **8. MTTR Threshold**        | Total recovery time strictly under 2 minutes              |   [ ] Pass   | Duration: `____ min ____ sec` |

**Drill Concluded By:** _________________________
**Role:** DevOps & Architecture Lead
**Evaluation Status:** Approved for Academic Demonstration
