# Per-Service Build, Test, Scan & Deployment Pipeline (EWSS-19)

---

## 1. Architectural Overview

Per **EWSS-19 (AS-CN-I-S-01)**, each service runs through decoupled CI/CD stages:
**Test → Build & Scan (SBOM + Grype) → Deploy by Image Digest**.

Each service can build, test, scan, and deploy completely independently. One service can ship alone without requiring the rebuilding or redeployment of other services.

```
+-----------------------------------------------------------------------------+
|                               STAGE 0: RESOLVE                              |
|          Resolve Target Environment (dev / stg / prod) & Planned Services   |
+-----------------------------------------------------------------------------+
                                      |
         +----------------------------+----------------------------+
         |                            |                            |
+------------------+         +------------------+         +------------------+
|   SHIP (ui)      |         |   SHIP (auth)    |         |  SHIP (api) ...  |
| 1. Test (Vitest) |         | 1. Test (Go vet) |         | 1. Test (Go vet) |
| 2. Build & Scan  |         | 2. Build & Scan  |         | 2. Build & Scan  |
|    - Syft SBOM   |         |    - Syft SBOM   |         |    - Syft SBOM   |
|    - Grype Crit  |         |    - Grype Crit  |         |    - Grype Crit  |
| 3. Deploy (ACR)  |         | 3. Deploy (ACR)  |         | 3. Deploy (ACR)  |
|    - aca-..-ui   |         |    - aca-..-auth |         |    - aca-..-api  |
+------------------+         +------------------+         +------------------+
         |                            |                            |
         +----------------------------+----------------------------+
                                      |
+-----------------------------------------------------------------------------+
|                               STAGE 2: DAST                                 |
|          OWASP ZAP Dynamic Application Security Testing against UI Gateway  |
+-----------------------------------------------------------------------------+
                                      |
+-----------------------------------------------------------------------------+
|                               STAGE 3: VERIFY                               |
|          Post-Deployment Smoke Verification & Compliance Audit Report       |
+-----------------------------------------------------------------------------+
```

---

## 2. Service Catalog & Topology Mappings

| Service Key | Component Bounded Context           | Tech Stack              | Container App Name                   | ACR Image Repository       | Dockerfile Location                                       |
| :---------- | :---------------------------------- | :---------------------- | :----------------------------------- | :------------------------- | :-------------------------------------------------------- |
| `ui`        | Frontend Web & Single Entry Gateway | Node.js 22 / Next.js 16 | `aca-ewastescalable-${ENV}-ui`       | `workflow-ui`              | `src/frontend/Dockerfile`                                 |
| `auth`      | Identity & Access Management        | Go 1.26                 | `aca-ewastescalable-${ENV}-auth`     | `identity-access-svc`      | `src/backend/Dockerfile` (`SERVICE=identity-access`)      |
| `api`       | Batch Workflow Engine               | Go 1.26                 | `aca-ewastescalable-${ENV}-api`      | `batch-workflow-svc`       | `src/backend/Dockerfile` (`SERVICE=batch-workflow`)       |
| `claim`     | Claim Logistics Service             | Go 1.26                 | `aca-ewastescalable-${ENV}-claim`    | `claim-logistics-svc`      | `src/backend/Dockerfile` (`SERVICE=claim-logistics`)      |
| `custody`   | Custody & Verification Service      | Go 1.26                 | `aca-ewastescalable-${ENV}-custody`  | `custody-verification-svc` | `src/backend/Dockerfile` (`SERVICE=custody-verification`) |
| `matcher`   | Matching Worker (Headless)          | Python 3.12             | `aca-ewastescalable-${ENV}-matcher`  | `matching-worker-svc`      | `src/matcher/Dockerfile` (`target=matching-worker`)       |
| `analytics` | Analytics Worker                    | Python 3.12             | `aca-ewastescalable-${ENV}-analytic` | `analytics-worker-svc`     | `src/matcher/Dockerfile` (`target=analytics-worker`)      |

> **Note on Decoupled Worker Deployments:** In accordance with EWSS-19, `matcher` and `analytics` are deployed independently as headless Azure Container Apps using direct image digest updates (`az containerapp update --image <digest>`). They do not depend on legacy wrapper scripts or synchronous runtime health probes against the private `api` container app ingress.

---

## 3. Pipeline Stages per Service

Each service executes the reusable workflow `.github/workflows/service-pipeline.yml`:

### Stage 1: Stack-Specific Testing

- **Go Services (`auth`, `api`, `claim`, `custody`):** Runs `go vet ./...`, `go test -race ./...`, and validates the entrypoint binary compilation (`go build -o /dev/null ./cmd/<service>`).
- **Node.js Frontend (`ui`):** Runs `npm ci`, `npm run lint`, and Vitest test suite (`npm test`).
- **Python Workers (`matcher`, `analytics`):** Installs `requirements.txt` and executes Pytest suite (`pytest -q tests`).

### Stage 2: Packaging, SBOM & Grype Vulnerability Gate

- Builds the isolated Docker image labeled with commit revision and service metadata.
- Generates an SPDX JSON Software Bill of Materials (SBOM) using **Anchore Syft**.
- Scans container image using **Anchore Grype** (`fail-build: true`, `severity-cutoff: critical`). Builds containing critical vulnerabilities are halted immediately.
- Retains SBOM and Grype SARIF reports as persistent release artifacts (90-day retention).
- Packages verified container image into a transfer bundle.

### Stage 3: Private VNet Deployment by Immutable Digest

- Runs on VNet-injected self-hosted runner (`runs-on: [self-hosted, azure-vnet, "${{ inputs.environment }}"]`) with direct Private Link to Azure Container Registry (ACR).
- Authenticates via VM Managed Identity and pushes the verified image to ACR.
- Inspects and pins the immutable `@sha256:...` content digest.
- Deploys the container to the designated Azure Container App via `az containerapp update --image <digest>`.
- Confirms revision rollout readiness (`latestRevisionName == latestReadyRevisionName`).
- Automatically prunes older ACR tags (retaining the last 3 for instant rollback availability).
- Publishes structured release evidence record (`release-<service>.json`).

---

## 4. How to Ship Services

### Automated Triggers

- **Push to `dev` or `main`:** Inspects git diff to identify changed directories. If only `src/frontend/` was modified, only `ui` ships. If shared backend code (`src/backend/internal/`) was modified, all four Go microservices ship in parallel. If workflow or infrastructure configurations changed, all services ship.
- **Pull Request to `main` (Staging evaluation):** Deploys changed services to `stg` environment for validation.
- **Database Migration Cascade (`workflow_run`):** When Liquibase migrations complete, all application services deploy automatically to align with the new database schema.

### Manual Dispatch (`workflow_dispatch`)

To ship one service alone or trigger rollbacks on demand:

1. Navigate to **Actions** → **CD - Multi-Environment Deployment & Evidence Pipeline**.
2. Click **Run workflow**.
3. Select **Target Azure Runtime Environment** (`dev`, `stg`, or `prod`).
4. In **Services to ship**, enter the desired service:
   - `ui` — ships only Frontend UI.
   - `auth` — ships only Identity & Access.
   - `api` — ships only Batch Workflow.
   - `claim` — ships only Claim Logistics.
   - `custody` — ships only Custody & Verification.
   - `matcher` — ships only Matching Worker.
   - `analytics` — ships only Analytics Worker.
   - `all` — ships all services concurrently.
5. In **Optional: Image tag to rollback to**:
   - Leave empty for a standard build and deploy from the commit.
   - Enter `PREVIOUS` to perform an instant N-1 rollback to the prior verified release.
   - Enter an exact tag (e.g. `dev-a1b2c3d`) to rollback to a specific historical release.

---

## 5. Database Migration & Data Seeding Prerequisites

Before application services deploy, `.github/workflows/database-migration.yml` applies Liquibase changesets against the environment's Azure Database for MySQL Flexible Server (`ewastescalabledb`).

### Liquibase Changeset Catalog (42 Changesets Total)

| Changeset Category               | Source Location                         | Quantity | Liquibase Context        | Execution Behavior                                                                                |
| :------------------------------- | :-------------------------------------- | :------- | :----------------------- | :------------------------------------------------------------------------------------------------ |
| **Schema DDL & Constraints**     | `database/changes/001` - `031`          | **34**   | _(None / Unconditional)_ | Runs in all environments. Establishes tables, foreign keys, outbox, and performance indexes.      |
| **Synthetic Base Data & Config** | `database/seed/101-103`, `105-108`      | **7**    | `context:@seed`          | Seeds synthetic test organizations, roles, users, and baseline matching/collector configurations. |
| **Synthetic Batch Fixtures**     | `database/seed/104-seed-c1-batches.sql` | **1**    | `context:@c1-fixtures`   | Seeds synthetic C1 sample batches in `DRAFT` and `SUBMITTED` states.                              |

### Context Filtering Rules

Liquibase filters changesets based on the `--contexts` parameter configured in `.github/workflows/database-migration.yml`:

```bash
# To run schema changes only (default production safety):
CONTEXT_PARAM=""

# To include synthetic base seed data (users, orgs, rules):
CONTEXT_PARAM="--contexts=seed"

# To include complete test data (base seed + 104 synthetic batch fixtures):
CONTEXT_PARAM="--contexts=seed,c1-fixtures"
```

> **Warning:** If `--contexts=seed` is provided without `c1-fixtures`, Liquibase applies 41 changesets and marks `104-seed-c1-batches.sql` as `Context mismatch: 1`. To apply all 42 changesets, `--contexts=seed,c1-fixtures` must be specified.

---

## 6. Self-Hosted Runner Infrastructure & Label Matrix

Deployment jobs execute inside the private Azure VNet to reach private ACR and ACA control planes without public exposure.

### Required Runner Labels

Every deployment job requests the runner labels:

```yaml
runs-on: [self-hosted, azure-vnet, "${{ inputs.environment }}"]
```

The runner agent (`runner-ewastescalable`) hosted on VM `vm-runner-ewastescalable-dev` must have the following labels configured to pick up jobs across all environments:

```bash
--labels self-hosted,azure-vnet,dev,stg,prod
```

### Quick Verification & Registration Command

If the runner needs reconfiguration or re-registration, execute on the runner VM:

```bash
su - runner -c "/home/runner/actions-runner/config.sh \
  --url https://github.com/nus-iss-dmss-practice-module-team5/team5-ewaste-scalable-systems \
  --token <REGISTRATION_TOKEN> \
  --name runner-ewastescalable \
  --labels self-hosted,azure-vnet,dev,stg,prod \
  --unattended \
  --replace"
```
