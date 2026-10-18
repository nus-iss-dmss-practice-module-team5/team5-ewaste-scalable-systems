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

- Runs on VNet-injected self-hosted runner with direct Private Link to Azure Container Registry (ACR).
- Authenticates via VM Managed Identity and pushes the verified image to ACR.
- Inspects and pins the immutable `@sha256:...` content digest.
- Deploys the container to the designated Azure Container App via `az containerapp update --image <digest>`.
- Confirms revision rollout readiness.
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
