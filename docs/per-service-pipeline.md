# Per-service build, test, scan and deploy (EWSS-19)

Each service now goes through its own pipeline stages, so one service can ship
without the others.

## How a run works

`cd-pipeline.yml` keeps its environment rules. Its push trigger also watches
every planner input (the catalogue, the pipeline files, `scripts/ci/` and the
matcher contracts), so a change to any of them reaches the planner. After it
resolves the target environment:

1. **Plan Services** lists the container apps in `rg-ewastescalable-<env>` and runs
   `scripts/ci/plan_services.py` against `.github/service-catalog.json`. The
   job summary shows each service, whether it ships and why.
2. **Ship** runs `service-pipeline.yml` once per planned service, in parallel.
   One service failing does not stop the others.
   - **Test**: only that service's stack (Go, Python or Node).
   - **Build & Scan**: builds the image, generates an SPDX SBOM with Syft and
     scans it with Grype (fails on critical). Both reports are kept for 90 days.
   - **Deploy**: loads the scanned image, checks its image ID, pushes it to
     ACR and updates only that service's container app by `@sha256` digest. It
     then waits until the new revision is ready on that digest and keeps a
     release record (service, tag, digest, commit, reason) for 90 days.
3. **DAST** and **smoke** checks run against the environment once the planned
   services have shipped.

## Which services ship

| Trigger                      | Services                                      |
| ---------------------------- | --------------------------------------------- |
| Push to `dev` or `main`      | Those whose paths changed in the push         |
| Pull request to `main` (stg) | Those whose paths changed in the pull request |
| Database migration cascade   | All available services                        |
| Manual run                   | The `services` input: `all` or a list         |
| Change to a pipeline file    | All available services                        |

When you add a path to `global_paths` or a service's `paths`, add it to
`on.push.paths` in `cd-pipeline.yml` too. `test_plan_services.py` fails until
you do.

Changes to `src/backend/cmd/<service>/` ship that Go service alone. Other
`src/backend/` changes ship all four Go services, because they share code.

A service is skipped, with the reason shown in the plan, when its entrypoint or
Dockerfile build target does not exist yet or its container app does not exist
in the target environment.

The four Go services (`identity-access`, `batch-workflow`, `claim-logistics`,
`custody-verification`) ship only together. Until all four container apps
exist, the existing combined API (`workflow-api`) keeps shipping to the `api`
app. Likewise the combined matcher image (`workflow-analytics`) keeps shipping
until the `matching-worker` and `analytics-worker` build targets and apps
exist. The UI is built with split API routing only when the four Go services
are live.

## Ship one service

Run **CD - Multi-Environment Deployment & Evidence Pipeline** manually with
`target_env` and `services` set, for example `services: ui`. To roll a single
service back, also set `rollback_tag` to `PREVIOUS` or an exact tag such as
`dev-a1b2c3d`. A rollback redeploys the existing image by digest without
rebuilding it.

## Add a service

Add an entry to `.github/service-catalog.json` (image, Dockerfile, context,
build target or arguments, container app suffix and paths). Then add a test
case in `scripts/ci/test_plan_services.py` and run:

```bash
python3 -m unittest discover -s scripts/ci
```
