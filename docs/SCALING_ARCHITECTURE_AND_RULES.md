# Autoscaling Architecture, KEDA Rules, and Live Demo Playbook

**Tracking:** `EWSS-30` (`AS-CN-T-L-01`: Verify scale-out, scale-in and replica health)
**Parent Epic:** `AS-SC` / `EWSS-11` (Scaling Targets & Acceptance Analysis)
**Track:** SWE5001 Practice Module (Architecting Scalable Systems)
**Author:** Team 5 DevSecOps & Cloud Platform
**Target Environment:** Azure Container Apps (`rg-ewastescalable-prod` in `indonesiacentral` / `malaysiawest`)
**Log Analytics Workspace:** `log-ewastescalable-centralized` (`7bb23c6a-7eaa-4662-a9ab-5617d36f450e`)

---

## 1. Executive Summary

This document serves as the authoritative reference for horizontal autoscaling behavior, KEDA scale rules, trigger thresholds, and timing windows across the **team5-ewaste-scalable-systems** platform. It provides the exact technical and architectural justifications required for project presentations, viva evaluations, and live scale demonstrations.

---

## 2. Autoscaling Controls Matrix (All 7 Workloads)

Each microservice runs in Azure Container Apps (Consumption profile) with dedicated horizontal pod autoscaling rules defined via Terraform.

| Microservice Component          | Azure Resource Name (`target_env=prod`) | Port & Ingress  | Baseline Replicas (`min`) | Peak Replicas (`max`) |   CPU / Memory   | Scaler Type | Trigger Metric & Threshold                              | Scale-Out Response | Scale-In Window |
| :------------------------------ | :-------------------------------------- | :-------------: | :-----------------------: | :-------------------: | :--------------: | :---------: | :------------------------------------------------------ | :----------------: | :-------------: |
| **PWA Gateway & Reverse Proxy** | `aca-ewastescalable-prod-ui`            | 3000 (External) |           **1**           |         **2**         | 0.5 vCPU / 1 GiB |   `http`    | **50 concurrent requests**                              |       ~15 s        |    **300 s**    |
| **Identity & Access API**       | `aca-ewastescalable-prod-auth`          | 8080 (Internal) |           **1**           |         **2**         | 0.5 vCPU / 1 GiB |   `http`    | **30 concurrent requests**                              |       ~15 s        |    **300 s**    |
| **Batch Workflow API**          | `aca-ewastescalable-prod-api`           | 8080 (Internal) |           **1**           |         **3**         | 0.5 vCPU / 1 GiB |   `http`    | **30 concurrent requests**                              |       ~15 s        |    **300 s**    |
| **Claim Logistics API**         | `aca-ewastescalable-prod-claim`         | 8080 (Internal) |           **1**           |         **3**         | 0.5 vCPU / 1 GiB |   `http`    | **30 concurrent requests**                              |       ~15 s        |    **300 s**    |
| **Custody Verification API**    | `aca-ewastescalable-prod-custody`       | 8080 (Internal) |           **1**           |         **3**         | 0.5 vCPU / 1 GiB |   `http`    | **30 concurrent requests**                              |       ~15 s        |    **300 s**    |
| **Matching Event Worker**       | `aca-ewastescalable-prod-matcher`       | None (Internal) |           **1**           |         **3**         | 0.5 vCPU / 1 GiB |   `kafka`   | **50 uncommitted messages lag** (`ewaste.batch.events`) |       ~20 s        |    **300 s**    |
| **Analytics Event Worker**      | `aca-ewastescalable-prod-analytic`      | 8000 (Internal) |           **1**           |         **3**         | 0.5 vCPU / 1 GiB |   `kafka`   | **50 uncommitted messages lag** (`ewaste.batch.events`) |       ~20 s        |    **300 s**    |

---

## 3. Scale Rule Implementation in Terraform

### 3.1 HTTP Ingress Concurrency Scaler

Applied to all synchronous APIs (`batch-workflow-svc`, `claim-logistics-svc`, `custody-verification-svc`, `identity-access-svc`) and UI reverse proxy:

```hcl
template {
  min_replicas = 1
  max_replicas = 3

  container {
    name   = "service"
    image  = "${var.shared_acr_name}.azurecr.io/ewaste-batch-workflow-svc:${var.image_digest}"
    cpu    = 0.5
    memory = "1Gi"
  }

  custom_scale_rule {
    name             = "http-concurrency-scaler"
    custom_rule_type = "http"
    metadata = {
      concurrentRequests = "30"
    }
  }
}
```

### 3.2 Kafka Consumer Lag Scaler (Event Hubs Surface)

Applied to asynchronous workers consuming partition events from Azure Event Hubs:

```hcl
template {
  min_replicas = 1
  max_replicas = 3

  container {
    name   = "worker"
    image  = "${var.shared_acr_name}.azurecr.io/ewaste-matching-worker-svc:${var.image_digest}"
    cpu    = 0.5
    memory = "1Gi"
  }

  custom_scale_rule {
    name             = "kafka-consumer-lag-scaler"
    custom_rule_type = "kafka"
    metadata = {
      bootstrapServers = "${azurerm_eventhub_namespace.kafka.name}.servicebus.windows.net:9093"
      consumerGroup    = "matching-worker"
      topic            = "ewaste.batch.events"
      lagThreshold     = "50"
      tls              = "enable"
      sasl             = "plaintext"
    }
    authentication {
      trigger_parameter = "username"
      secret_name       = "kafka-username"
    }
    authentication {
      trigger_parameter = "password"
      secret_name       = "kafka-conn"
    }
  }
}
```

---

## 4. Scaling Dynamics & Timing Architecture

![img_5.png](img_5.png)

### 4.1 Why Scale-Out is Fast (~15s) while Scale-In Takes 300s

1. **Scale-Out Latency (~15 seconds):**
   - KEDA polls active Envoy ingress metrics every 15–30 seconds.
   - When concurrent requests exceed the target threshold (`30`), KEDA computes:

     $$\text{Target Replicas} = \min\left(\text{maxReplicas}, \left\lceil \frac{\text{Active Concurrent Requests}}{\text{Target Concurrency}} \right\rceil\right)$$

   - Pod provisioning starts immediately in parallel.

2. **Scale-In Stabilization Window ($300\text{ seconds}$):**
   - In Azure Container Apps, active replica scale-in ($N \to 1$) is managed by the underlying **Kubernetes Horizontal Pod Autoscaler (HPA)**.
   - Microsoft **hardcodes the HPA scale-down stabilization window to 300 seconds (5 minutes)**.
   - Microsoft does not expose `scaleDown.stabilizationWindowSeconds` or HPA behavior policies in ARM, Bicep, Terraform, or Azure CLI. Passing `cooldownPeriod` at the ARM level for multi-replica apps causes an ARM schema validation error: `400 Bad Request: Unknown properties cooldownPeriod, pollingInterval in ContainerAppScale`.
3. **Difference from `cooldownPeriod` (Scale-to-Zero):**
   - The `cooldownPeriod` setting in KEDA applies exclusively to **Scale-to-Zero ($1 \to 0$ replicas)** when `minReplicas = 0`.
   - Because our services enforce `min_replicas = 1` across all microservices (to eliminate cold-start latency for donors, recyclers, and collectors), scale-to-zero is intentionally inactive. Downscaling from $3 \to 1$ is governed entirely by the 300-second HPA stabilization window.

### 4.2 Flap Damping Architectural Justification (Viva / Evaluation Defense)

> _"In production cloud environments, scaling down too rapidly (e.g., within 30 seconds) triggers **autoscaler flapping**—a destructive cycle where temporary dips in user traffic cause containers to be destroyed and immediately re-created. Azure Container Apps enforces a 300-second stabilization window to prevent flapping, preserving database connection pools, warm application caches, and avoiding container startup CPU penalties."_

---

## 5. Live Demonstration Playbook (PowerShell)

### 5.1 Continuous Replica Watcher (PowerShell Loop)

Run this command in a dedicated terminal during the demo to watch active instance counts change in real time:

```powershell
while ($true) {
    $replicas = (az containerapp replica list `
      --name aca-ewastescalable-prod-api `
      --resource-group rg-ewastescalable-prod `
      --query "[].{Name:name, State:properties.runningState}" -o json | ConvertFrom-Json)
    $ts = Get-Date -Format "HH:mm:ss"
    Write-Host "[$ts] Active Replicas: $($replicas.Count)" -ForegroundColor Cyan
    $replicas | ForEach-Object { Write-Host "   -> Pod: $($_.Name) | State: $($_.State)" -ForegroundColor Gray }
    Start-Sleep -Seconds 5
}
```

### 5.2 Stream Application Logs Live

```powershell
az containerapp logs show `
  --name aca-ewastescalable-prod-api `
  --resource-group rg-ewastescalable-prod `
  --follow `
  --format text
```

### 5.3 Driving Concurrency Surge to Trigger Scale-Out

```powershell
# Drive 60 concurrent requests through the public single entry reverse proxy
$targetUrl = "https://aca-ewastescalable-prod-ui.gentlestone-a085f952.indonesiacentral.azurecontainerapps.io/api/v1/batches?status=COMPLETED"
1..60 | ForEach-Object -Parallel {
    param($url)
    1..25 | ForEach-Object {
        try { Invoke-RestMethod -Uri $url -Method Get -TimeoutSec 10 | Out-Null } catch {}
    }
} -ArgumentList $targetUrl
```

---

## 6. Azure Monitor Workbook & Manual KQL Query Playbook (Observation & Evidence Capture)

Follow these steps to import the interactive dashboard and run copy-pasteable KQL queries to capture screenshots for your project report and presentation slides.

### 6.1 Step-by-Step: Importing the Azure Monitor Workbook

1. Open the [Azure Portal](https://portal.azure.com) in your browser.
2. Ensure your active directory is set to **National University of Singapore** and subscription is set to **`adbeb191-a535-49b4-a660-fb8d484e74ee`**.
3. Search for **Log Analytics workspaces** in the top search bar and open **`log-ewastescalable-centralized`** (Resource Group: `rg-ewastescalable-shared`).
4. In the left navigation menu under **Monitoring**, click **Workbooks**.
5. Click **`+ New`** at the top toolbar to create a blank workbook.
6. Click the **Advanced Editor** icon: **`</>`** (located on the far right of the top toolbar).
7. Replace the default JSON with the full content of [`docs/monitoring/azure-monitor-workbook.json`](./monitoring/azure-monitor-workbook.json).
8. Click **Apply** at the bottom, then click **Done Editing** and **Save** as `AS-CN-T-L-01: Scale & Replica Health Dashboard`.

---

### 6.2 Recommended KQL Queries for Screenshot Capture

Open **Logs** under your Log Analytics workspace (`log-ewastescalable-centralized`) and run these curated queries:

#### Query 1: Chronological Scale-Out and Scale-In Transitions

_Use this query to screenshot the automated rescale decisions from KEDA._

```kql
// Capture automated scale-out to 3 and scale-down to 1
ContainerAppSystemLogs_CL
| where TimeGenerated >= ago(24h)
| where Reason_s == "SuccessfulRescale"
| project TimeGenerated, ContainerAppName_s, Reason_s, Log_s
| order by TimeGenerated desc
```

> **Sample Capture:** The results table showing `aca-ewastescalable-prod-api has been scaled to 3 by the scaler: http-scaler.` followed by `Scale down to 1 as all metrics below target`.

![img.png](img.png)

---

#### Query 2: Active Replicas Time Series Area Chart ($1 \to 3 \to 1$)

```kql
// Visualizing the replica scaling lifecycle curve
ContainerAppSystemLogs_CL
| where TimeGenerated between (datetime('2026-10-19T07:15:00Z') .. datetime('2026-10-19T07:55:00Z'))
| where ContainerAppName_s in ('aca-ewastescalable-prod-api', 'aca-ewastescalable-prod-ui', 'aca-ewastescalable-prod-matcher')
| extend TargetReplicas = case(
    Log_s contains 'scaled to 3', 3,
    Log_s contains 'scaled to 2', 2,
    Log_s contains 'Scale down to 1', 1,
    Log_s contains 'New size: 2', 2,
    1)
| summarize MaxReplicas = max(TargetReplicas) by bin(TimeGenerated, 1m), ContainerAppName_s
| render timechart
```

> **Sample Capture:** Switch the display dropdown to **Chart** $\to$ **Area chart**. Capture the plateau where replicas expand to 3, hold for the 300s window, and drop back to 1.

![img_1.png](img_1.png)

---

#### Query 3: Container Replica Health, Node Assignments & Zero Crashes

```kql
// Proving 100% health, 0 crashes, and clean pod scheduling
ContainerAppSystemLogs_CL
| where TimeGenerated >= ago(24h)
| where ContainerAppName_s in ('aca-ewastescalable-prod-api', 'aca-ewastescalable-prod-ui', 'aca-ewastescalable-prod-matcher')
| summarize
    TotalSystemEvents = count(),
    SuccessfulRescales = countif(Reason_s == 'SuccessfulRescale'),
    ReplicasAssigned = countif(Reason_s == 'AssigningReplica'),
    ContainersStarted = countif(Reason_s == 'ContainerStarted'),
    ContainersReady = countif(Reason_s == 'ContainerAppReady'),
    ContainerCrashes = countif(Reason_s == 'ContainerCrashing')
    by ContainerAppName_s
```

> **Sample Capture:** The table showing `ContainerCrashes = 0` across all services with multiple rescales and assigned replicas.

![img_2.png](img_2.png)

---

#### Query 4: API Request Throughput, Latency Percentiles (P50/P90/P95) & Zero 5xx Errors

```kql
// Ingress traffic volume, latency percentiles and error rate
ContainerAppConsoleLogs_CL
| where TimeGenerated between (datetime('2026-10-19T07:15:00Z') .. datetime('2026-10-19T07:55:00Z'))
| where ContainerAppName_s == 'aca-ewastescalable-prod-api'
| extend log = parse_json(Log_s)
| extend LatencyMs = toreal(log.latency_ms), Status = toint(log.status)
| summarize
    TotalRequests = count(),
    P50_Ms = round(percentile(LatencyMs, 50), 2),
    P90_Ms = round(percentile(LatencyMs, 90), 2),
    P95_Ms = round(percentile(LatencyMs, 95), 2),
    Errors5xx = countif(Status >= 500)
    by bin(TimeGenerated, 5m)
| order by TimeGenerated asc
```

> **Sample Capture:** The row at peak traffic (14,336 requests) showing `P95_Ms = 2449.28 ms` (< 3,000 ms target) and `Errors5xx = 0`.

![img_3.png](img_3.png)

---

#### Query 5: Kafka Worker Partition Lag & Consumer Rebalance

```kql
// Kafka consumer worker scale-out on ewaste.batch.events
ContainerAppSystemLogs_CL
| where TimeGenerated >= ago(24h)
| where ContainerAppName_s == 'aca-ewastescalable-prod-matcher'
| where Reason_s in ('SuccessfulRescale', 'AssigningReplica', 'ContainerStarted')
| project TimeGenerated, ContainerAppName_s, Reason_s, Log_s
| order by TimeGenerated desc
```

> **Sample Capture:** The log entry: `aca-ewastescalable-prod-matcher has been scaled to 3 by the scaler: kafka-ewaste-batch-events.`

![img_4.png](img_4.png)

---

#### Query 6: Kafka Event Hubs Consumer Lag per Partition (Live Matrix)

_Use this query to show message backlog across Partition 0, Partition 1, and Partition 2._

```kql
// View exact uncommitted message lag per partition on ewaste.batch.events
ContainerAppConsoleLogs_CL
| where TimeGenerated >= ago(2h)
| extend log = parse_json(Log_s)
| where log.observation == "consumer_lag"
| extend
    ConsumerGroup = tostring(log.consumer_group),
    Topic = tostring(log.topic),
    PartitionNum = toint(log.partition),
    CommittedOffset = tolong(log.committed_offset),
    EndOffset = tolong(log.end_offset),
    ConsumerLag = tolong(log.consumer_lag)
| summarize arg_max(TimeGenerated, *) by Topic, ConsumerGroup, PartitionNum
| extend Partition = strcat("Partition ", PartitionNum)
| extend BacklogStatus = case(
    ConsumerLag == 0, "Drained (0 Lag)",
    ConsumerLag <= 50, "Below Scale Threshold (<= 50)",
    "Scale Threshold Breached (> 50)")
| project Partition, ConsumerGroup, Topic, CommittedOffset, EndOffset, ConsumerLag, BacklogStatus, LastSampled=TimeGenerated
| order by ConsumerGroup asc, Partition asc
```

---

#### Query 7: All Applications Scaling Visualizer (Full Fleet Timechart)

_Use this query to visualize the scaling curves of all 7 microservices simultaneously._

```kql
// Visualizing replica expansion and scale-down across all 7 platform container apps
ContainerAppSystemLogs_CL
| where TimeGenerated >= ago(12h)
| where ContainerAppName_s startswith "aca-ewastescalable-prod-"
| extend App = replace_string(ContainerAppName_s, "aca-ewastescalable-prod-", "")
| extend TargetReplicas = case(
    Log_s contains "scaled to 3", 3,
    Log_s contains "scaled to 2", 2,
    Log_s contains "New size: 2", 2,
    Log_s contains "Scale down to 1", 1,
    1)
| summarize MaxReplicas = max(TargetReplicas) by bin(TimeGenerated, 2m), App
| render timechart
```

---

### 6.3 Screenshot Capture & Viva Presentation Guide

1. **Slide 1: Scale-Out Elasticity**
   - **Picture:** Screenshot of **Query 1** (`SuccessfulRescale`) highlighting `scaled to 3 by the scaler: http-scaler`.
   - **Talking Point:** _"KEDA monitored active HTTP concurrency through Envoy ingress. As concurrent requests exceeded our threshold of 30, the platform automatically provisioned two additional replicas in 16.4 seconds."_

2. **Slide 2: Flap-Damping Stabilization Window (Scale-In)**
   - **Picture:** Screenshot of **Query 2** (Area chart) or **Query 1** highlighting `Scale down to 1 as all metrics below target`.
   - **Talking Point:** _"When load ceased, Azure Container Apps' Kubernetes HPA held instances for the 300-second stabilization window to dampen potential traffic flapping, preventing severe CPU churn before cleanly terminating surplus pods."_

3. **Slide 3: High Availability & SLO Compliance**
   - **Picture:** Screenshot of **Query 3** (`ContainerCrashes = 0`) and **Query 4** (`P95 = 2449.28ms`).
   - **Talking Point:** _"Across all 21 scaling transitions, the platform maintained 100% availability with 0 container crashes and absorbed 14,000+ requests with P95 latency well within our 3,000ms SLO."_

4. **Slide 4: Event-Driven Autoscaling & Partition Lag**
   - **Picture:** Screenshot of **Query 6** (Partition lag matrix) and **Query 7** (All apps scaling visualizer).
   - **Talking Point:** _"For asynchronous background processing, KEDA monitors consumer lag across all 3 Event Hubs partitions. When uncommitted lag exceeded 50 messages, the matcher scaled to 3 replicas—assigning one worker per partition to drain backlog in parallel."_
