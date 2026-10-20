[CmdletBinding()]
param(
    [string]$GoExecutable = "",
    [string]$MySqlHost = "127.0.0.1",
    [int]$MySqlPort = 3306,
    [string]$MySqlUser = "root",
    [string]$MySqlPassword = "",
    [string]$RedisAddress = "127.0.0.1:6379",
    [int]$RedisDatabase = 0,
    [string]$RedisPassword = "",
    [string]$ApiAddress = "127.0.0.1:18081",
    [string]$ReportPath = "",
    [string]$MigrationSourceCommit = "847d243"
)

# This script intentionally owns an isolated database and a dedicated API port.
# It exercises the complete HTTP workflow and drops all temporary state in the
# finally block, so it is safe to run repeatedly against local dependencies.
$ErrorActionPreference = "Stop"
$backendRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$repoRoot = (Resolve-Path (Join-Path $backendRoot "....")).Path
$runID = [guid]::NewGuid().ToString("N").Substring(0, 12)
$targetDatabase = "ewaste_full_it_$runID"
$serverExecutable = Join-Path ([IO.Path]::GetTempPath()) "ewaste-full-it-$runID.exe"
$serverStdout = "$serverExecutable.stdout.log"
$serverStderr = "$serverExecutable.stderr.log"
$server = $null
$baseURI = "http://$ApiAddress"
$results = [System.Collections.Generic.List[object]]::new()

if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $ReportPath = Join-Path $backendRoot "test-results\full-integration-report.md"
}

if ([string]::IsNullOrWhiteSpace($GoExecutable)) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $goCommand) {
        $GoExecutable = $goCommand.Source
    }
}
if ([string]::IsNullOrWhiteSpace($GoExecutable)) {
    $goLandGo = Join-Path $env:USERPROFILE "sdk\go1.26.1\bin\go.exe"
    if (Test-Path -LiteralPath $goLandGo) {
        $GoExecutable = $goLandGo
    }
}
if ([string]::IsNullOrWhiteSpace($GoExecutable) -or -not (Test-Path -LiteralPath $GoExecutable)) {
    throw "Go executable was not found. Pass -GoExecutable with the selected SDK path."
}

if ([string]::IsNullOrWhiteSpace($MySqlPassword)) { $MySqlPassword = $env:MYSQL_PASSWORD }
if ([string]::IsNullOrWhiteSpace($MySqlPassword)) { $MySqlPassword = $env:MYSQL_ROOT_PASSWORD }
if ([string]::IsNullOrWhiteSpace($RedisPassword)) { $RedisPassword = $env:REDIS_PASSWORD }

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ReportPath) | Out-Null

function Add-Check {
    param([string]$Name, [bool]$Passed, [string]$Details)
    [void]$results.Add([pscustomobject]@{
        Name = $Name
        ExitCode = if ($Passed) { 0 } else { 1 }
        Output = @($Details)
    })
}

function Invoke-GoCheck {
    param([string]$Name, [string[]]$Arguments)
    $output = @(& $GoExecutable @Arguments 2>&1 | ForEach-Object { [string]$_ })
    Add-Check -Name $Name -Passed ($LASTEXITCODE -eq 0) -Details (($output -join [Environment]::NewLine).Trim())
}

function Get-SqlText {
    param([string]$RelativePath)
    $workingTreePath = Join-Path $repoRoot $RelativePath
    if (Test-Path -LiteralPath $workingTreePath) {
        $rawSql = Get-Content -LiteralPath $workingTreePath -Raw
    }
    else {
        $rawSql = & git -C $repoRoot show "$MigrationSourceCommit`:$RelativePath" 2>&1
        if ($LASTEXITCODE -ne 0) { throw "SQL source is unavailable: $RelativePath" }
        $rawSql = $rawSql -join [Environment]::NewLine
    }
    return (($rawSql -split "`r?`n" | Where-Object {
        $_ -notmatch '^\s*--(liquibase|changeset|precondition|comment|rollback)'
    }) -join [Environment]::NewLine)
}

function Invoke-DockerMySqlText {
    param([string]$Database, [string]$SqlText)
    $clientHost = if ($MySqlHost -in @("localhost", "127.0.0.1")) { "host.docker.internal" } else { $MySqlHost }
    $arguments = @(
        "run", "--rm", "-i", "-e", "MYSQL_PWD=$MySqlPassword", "mysql:8.4",
        "mysql", "--protocol=TCP", "-h", $clientHost, "-P", [string]$MySqlPort,
        "-u", $MySqlUser, "--default-character-set=utf8mb4"
    )
    if (-not [string]::IsNullOrWhiteSpace($Database)) { $arguments += @("-D", $Database) }
    $output = @($SqlText | & docker @arguments 2>&1 | ForEach-Object { [string]$_ })
    if ($LASTEXITCODE -ne 0) { throw "MySQL command failed: $($output -join ' ')" }
    return $output
}

function Invoke-DockerRedisCleanup {
    $hostPart = $RedisAddress.Split(":")[0]
    $portPart = if ($RedisAddress.Contains(":")) { $RedisAddress.Split(":")[-1] } else { "6379" }
    $arguments = @("run", "--rm")
    if (-not [string]::IsNullOrWhiteSpace($RedisPassword)) { $arguments += @("-e", "REDISCLI_AUTH=$RedisPassword") }
    $arguments += @("redis:7.4-alpine", "redis-cli", "-h", $(if ($hostPart -in @("localhost", "127.0.0.1")) { "host.docker.internal" } else { $hostPart }), "-p", $portPart, "-n", [string]$RedisDatabase, "DEL", "ewaste:rate:127.0.0.1")
    $output = @(& docker @arguments 2>&1 | ForEach-Object { [string]$_ })
    if ($LASTEXITCODE -ne 0) { throw "Redis cleanup failed: $($output -join ' ')" }
    return $output
}

function Initialize-FullDatabase {
    Invoke-DockerMySqlText -Database "mysql" -SqlText "CREATE DATABASE ``$targetDatabase`` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;" | Out-Null
    $files = @(
        "database/changes/001-create-organisations.sql", "database/changes/002-create-roles.sql",
        "database/changes/003-create-users.sql", "database/changes/004-create-sessions.sql",
        "database/changes/005-create-login-audit.sql", "database/changes/006-create-ewaste-batches.sql",
        "database/changes/007-add-ewaste-batch-constraints.sql", "database/changes/008-create-command-idempotency.sql",
        "database/changes/009-create-batch-audit-events.sql", "database/changes/010-create-event-outbox.sql",
        "database/changes/011-create-matching-rule-sets.sql", "database/changes/012-create-recycler-matching-profiles.sql",
        "database/changes/013-create-recycler-capacity-pools.sql", "database/changes/014-create-recycler-category-capabilities.sql",
        "database/changes/015-create-recycler-service-zones.sql", "database/changes/016-create-matching-decisions.sql",
        "database/changes/017-create-matched-results.sql", "database/changes/018-create-batch-claims.sql",
        "database/changes/019-create-capacity-reservations.sql", "database/changes/020-link-current-claim-and-audit.sql",
        "database/changes/021-create-recycler-collector-scopes.sql", "database/changes/022-create-batch-assignments.sql",
        "database/changes/023-create-batch-handoffs.sql", "database/changes/024-create-assignment-actions.sql",
        "database/changes/025-link-assignment-pointers-and-history.sql", "database/seed/101-seed-organisations.sql",
        "database/seed/102-seed-roles.sql", "database/seed/103-seed-users.sql"
    )
    foreach ($file in $files) { Invoke-DockerMySqlText -Database $targetDatabase -SqlText (Get-SqlText $file) | Out-Null }

    $fixtureSql = @'
SET time_zone = '+00:00';
INSERT INTO matching_rule_sets (id, version, rules_json, effective_from, created_by, created_at)
VALUES ('r2260000-0000-4000-8000-000000000001', 'full-it-v1', JSON_OBJECT('fixture', TRUE), UTC_TIMESTAMP(6), 'USR-001', UTC_TIMESTAMP(6));
INSERT INTO recycler_matching_profiles (recycler_org_id, is_active, version, created_at, updated_at)
VALUES ('PROC-001', TRUE, 1, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6));
INSERT INTO recycler_capacity_pools (id, recycler_org_id, pool_code, total_kg, reserved_kg, is_active, version, updated_at)
VALUES ('p2260000-0000-4000-8000-000000000001', 'PROC-001', 'FULL-IT', 1000.00, 0.00, TRUE, 1, UTC_TIMESTAMP(6));
INSERT INTO recycler_category_capabilities (id, recycler_org_id, category, accepted_conditions_json, supports_data_bearing, is_active, capacity_pool_id, version, updated_at)
VALUES ('c2260000-0000-4000-8000-000000000001', 'PROC-001', 'ICT_EQUIPMENT', JSON_ARRAY('FUNCTIONAL','REPAIRABLE','END_OF_LIFE'), TRUE, TRUE, 'p2260000-0000-4000-8000-000000000001', 1, UTC_TIMESTAMP(6));
INSERT INTO recycler_service_zones (id, recycler_org_id, zone, minimum_lead_minutes, is_active, version, updated_at)
VALUES ('z2260000-0000-4000-8000-000000000001', 'PROC-001', 'NORTH', 0, TRUE, 1, UTC_TIMESTAMP(6));
INSERT INTO recycler_collector_scopes (id, recycler_org_id, collector_org_id, zone, is_active, version, valid_from, created_at, updated_at)
VALUES ('s2260000-0000-4000-8000-000000000001', 'PROC-001', 'COL-001', 'NORTH', TRUE, 1, DATE_SUB(UTC_TIMESTAMP(6), INTERVAL 1 HOUR), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6));
INSERT INTO ewaste_batches (id, organization_id, created_by, status, category, quantity, estimated_weight_kg, condition_rating, is_data_bearing, zone, collection_deadline, claim_epoch, version, submitted_at, created_at, updated_at)
VALUES
('b2260000-0000-4000-8000-000000000001', 'DON-001', 'USR-003', 'MATCHED', 'ICT_EQUIPMENT', 1, 1.00, 'FUNCTIONAL', FALSE, 'NORTH', DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 72 HOUR), 1, 3, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)),
('b2260000-0000-4000-8000-000000000002', 'DON-001', 'USR-003', 'MATCHED', 'ICT_EQUIPMENT', 2, 2.00, 'FUNCTIONAL', FALSE, 'NORTH', DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 72 HOUR), 1, 3, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)),
('b2260000-0000-4000-8000-000000000003', 'DON-001', 'USR-003', 'MATCHED', 'ICT_EQUIPMENT', 3, 3.00, 'FUNCTIONAL', FALSE, 'NORTH', DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 72 HOUR), 1, 3, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6));
INSERT INTO matching_decisions (id, batch_id, trigger_id, trigger_type, batch_version, claim_epoch, rule_set_id, evaluation_at, input_hash, profile_snapshot_hash, input_snapshot_json, outcome, primary_reason, evaluated_count, eligible_count, correlation_id, created_at, completed_at)
VALUES
('d2260000-0000-4000-8000-000000000001','b2260000-0000-4000-8000-000000000001','t2260000-0000-4000-8000-000000000001','REQUEST_SUBMITTED',2,1,'r2260000-0000-4000-8000-000000000001',UTC_TIMESTAMP(6),SHA2('full-it-1',256),SHA2('full-it-profile',256),JSON_OBJECT('fixture',TRUE),'MATCHED','ELIGIBLE_EXISTS',1,1,'full-it-1',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6)),
('d2260000-0000-4000-8000-000000000002','b2260000-0000-4000-8000-000000000002','t2260000-0000-4000-8000-000000000002','REQUEST_SUBMITTED',2,1,'r2260000-0000-4000-8000-000000000001',UTC_TIMESTAMP(6),SHA2('full-it-2',256),SHA2('full-it-profile',256),JSON_OBJECT('fixture',TRUE),'MATCHED','ELIGIBLE_EXISTS',1,1,'full-it-2',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6)),
('d2260000-0000-4000-8000-000000000003','b2260000-0000-4000-8000-000000000003','t2260000-0000-4000-8000-000000000003','REQUEST_SUBMITTED',2,1,'r2260000-0000-4000-8000-000000000001',UTC_TIMESTAMP(6),SHA2('full-it-3',256),SHA2('full-it-profile',256),JSON_OBJECT('fixture',TRUE),'MATCHED','ELIGIBLE_EXISTS',1,1,'full-it-3',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6));
INSERT INTO matched_results (id, decision_id, batch_id, recycler_org_id, profile_version, category_match, capability_match, capacity_available, zone_match, deadline_viable, is_matched, available_capacity_kg, capacity_pool_id, capacity_version, minimum_lead_minutes, feasible_at, reason_code, failed_rules_json, evidence_json, created_at)
VALUES
('m2260000-0000-4000-8000-000000000001','d2260000-0000-4000-8000-000000000001','b2260000-0000-4000-8000-000000000001','PROC-001',1,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,1000.00,'p2260000-0000-4000-8000-000000000001',1,0,UTC_TIMESTAMP(6),'ELIGIBLE',JSON_ARRAY(),JSON_OBJECT('fixture',TRUE),UTC_TIMESTAMP(6)),
('m2260000-0000-4000-8000-000000000002','d2260000-0000-4000-8000-000000000002','b2260000-0000-4000-8000-000000000002','PROC-001',1,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,1000.00,'p2260000-0000-4000-8000-000000000001',1,0,UTC_TIMESTAMP(6),'ELIGIBLE',JSON_ARRAY(),JSON_OBJECT('fixture',TRUE),UTC_TIMESTAMP(6)),
('m2260000-0000-4000-8000-000000000003','d2260000-0000-4000-8000-000000000003','b2260000-0000-4000-8000-000000000003','PROC-001',1,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,1000.00,'p2260000-0000-4000-8000-000000000001',1,0,UTC_TIMESTAMP(6),'ELIGIBLE',JSON_ARRAY(),JSON_OBJECT('fixture',TRUE),UTC_TIMESTAMP(6));
'@
    Invoke-DockerMySqlText -Database $targetDatabase -SqlText $fixtureSql | Out-Null
}

function Invoke-Api {
    param([string]$Method, [string]$Path, [hashtable]$Headers = @{}, [object]$Body = $null)
    $parameters = @{ UseBasicParsing = $true; Uri = "$baseURI$Path"; Method = $Method; Headers = $Headers; ErrorAction = "Stop" }
    if ($null -ne $Body) { $parameters.Body = ($Body | ConvertTo-Json -Compress -Depth 10); $parameters.ContentType = "application/json" }
    try {
        $response = Invoke-WebRequest @parameters
        return [pscustomobject]@{ StatusCode = [int]$response.StatusCode; Content = [string]$response.Content; Headers = $response.Headers }
    }
    catch {
        $errorResponse = $_.Exception.Response
        if ($null -eq $errorResponse) { return [pscustomobject]@{ StatusCode = 0; Content = $_.Exception.Message; Headers = @{} } }
        $reader = New-Object IO.StreamReader($errorResponse.GetResponseStream())
        try { $content = $reader.ReadToEnd() } finally { $reader.Dispose() }
        return [pscustomobject]@{ StatusCode = [int]$errorResponse.StatusCode; Content = $content; Headers = $errorResponse.Headers }
    }
}

function Read-Json { param($Response) try { return ($Response.Content | ConvertFrom-Json) } catch { return $null } }
function Expect-Status { param([string]$Name, $Response, [int]$Expected)
    $body = if ([string]::IsNullOrWhiteSpace($Response.Content)) { "(empty)" } else { $Response.Content }
    $body = $body -replace '("(?:access|refresh)_token"\s*:\s*")[^"]+(")', '$1<redacted>$2'
    Add-Check -Name $Name -Passed ($Response.StatusCode -eq $Expected) -Details "status=$($Response.StatusCode); expected=$Expected; body=$body"
}
function Expect-Value { param([string]$Name, [bool]$Passed, [string]$Details) Add-Check -Name $Name -Passed $Passed -Details $Details }
function Headers-For { param([string]$Token, [string]$Correlation, [string]$Idempotency = "", [string]$Version = "")
    $headers = @{ Authorization = "Bearer $Token"; "X-Correlation-ID" = $Correlation }
    if ($Idempotency) { $headers["Idempotency-Key"] = $Idempotency }
    if ($Version) { $headers["If-Match-Version"] = $Version }
    return $headers
}
function Login { param([string]$Email)
    $response = Invoke-Api -Method POST -Path "/api/v1/auth/login" -Headers @{ "X-Correlation-ID" = "full-it-login-$Email" } -Body @{ email = $Email; password = "TestOnly#2026!" }
    Expect-Status -Name "login $Email" -Response $response -Expected 200
    return Read-Json $response
}

$originalEnvironment = @{}
foreach ($name in @("EWASTE_MODE","EWASTE_SERVER_PORT","EWASTE_DATABASE_HOST","EWASTE_DATABASE_PORT","EWASTE_DATABASE_NAME","EWASTE_DATABASE_USER","MYSQL_PASSWORD","EWASTE_REDIS_ADDRESS","EWASTE_REDIS_DB","REDIS_PASSWORD","EWASTE_AUTH_ISSUER","EWASTE_AUTH_ACCESS_SECRET","EWASTE_AUTH_REFRESH_SECRET","EWASTE_AUTH_REFRESH_HASH_SECRET","EWASTE_LOGGING_FILE_PATH","GOROOT","GOTOOLCHAIN")) {
    $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

$startedAt = Get-Date
try {
    try {
        Initialize-FullDatabase
        Add-Check -Name "full schema migration and fixture seeding" -Passed $true -Details "database=$targetDatabase; migrations=001..025; fixtures=3 matched batches"

        $env:GOROOT = $null
        $env:GOTOOLCHAIN = "local"
        $env:EWASTE_MODE = "test"
        $env:EWASTE_SERVER_PORT = ":$($ApiAddress.Split(':')[-1])"
        $env:EWASTE_DATABASE_HOST = $MySqlHost
        $env:EWASTE_DATABASE_PORT = [string]$MySqlPort
        $env:EWASTE_DATABASE_NAME = $targetDatabase
        $env:EWASTE_DATABASE_USER = $MySqlUser
        $env:MYSQL_PASSWORD = $MySqlPassword
        $env:EWASTE_REDIS_ADDRESS = $RedisAddress
        $env:EWASTE_REDIS_DB = [string]$RedisDatabase
        $env:REDIS_PASSWORD = $RedisPassword
        $env:EWASTE_AUTH_ISSUER = "ewaste-full-integration"
        $env:EWASTE_AUTH_ACCESS_SECRET = "full-integration-access-secret"
        $env:EWASTE_AUTH_REFRESH_SECRET = "full-integration-refresh-secret"
        $env:EWASTE_AUTH_REFRESH_HASH_SECRET = "full-integration-refresh-hash-secret"
        $env:EWASTE_LOGGING_FILE_PATH = Join-Path ([IO.Path]::GetTempPath()) "ewaste-full-integration.log"

        Push-Location $backendRoot
        try {
            Invoke-GoCheck -Name "go test ./..." -Arguments @("test", "./...")
            Invoke-GoCheck -Name "go vet ./..." -Arguments @("vet", "./...")
            $buildOutput = @(& $GoExecutable build -o $serverExecutable ./cmd/server 2>&1 | ForEach-Object { [string]$_ })
            Add-Check -Name "build integration server" -Passed ($LASTEXITCODE -eq 0) -Details (($buildOutput -join " ").Trim())
            if ($LASTEXITCODE -ne 0) { throw "integration server build failed" }

            $server = Start-Process -FilePath $serverExecutable -WorkingDirectory $backendRoot -PassThru -WindowStyle Hidden -RedirectStandardOutput $serverStdout -RedirectStandardError $serverStderr
            $ready = $null
            for ($attempt = 1; $attempt -le 40; $attempt++) {
                $ready = Invoke-Api -Method GET -Path "/readyz"
                if ($ready.StatusCode -eq 200) { break }
                Start-Sleep -Milliseconds 500
            }
            Expect-Status -Name "readiness" -Response $ready -Expected 200
            Expect-Status -Name "liveness" -Response (Invoke-Api -Method GET -Path "/healthz") -Expected 200
            Expect-Status -Name "hello" -Response (Invoke-Api -Method GET -Path "/api/v1/hello") -Expected 200
            $cors = Invoke-Api -Method OPTIONS -Path "/api/v1/batches" -Headers @{ Origin = "http://localhost:3000"; "Access-Control-Request-Method" = "POST" }
            Expect-Value -Name "CORS preflight" -Passed ($cors.StatusCode -eq 204 -and $cors.Headers["Access-Control-Allow-Origin"] -eq "http://localhost:3000") -Details "status=$($cors.StatusCode); allow_origin=$($cors.Headers['Access-Control-Allow-Origin'])"
            Expect-Status -Name "anonymous request rejected" -Response (Invoke-Api -Method GET -Path "/api/v1/batches") -Expected 401

            $donorToken = Login "donor1@ewaste.test"
            $donorAccess = [string]$donorToken.access_token
            $donorRefresh = Invoke-Api -Method POST -Path "/api/v1/auth/refresh" -Headers @{ "X-Correlation-ID" = "full-it-refresh" } -Body @{ refresh_token = $donorToken.refresh_token }
            Expect-Status -Name "refresh token rotation" -Response $donorRefresh -Expected 200
            $donorToken = Read-Json $donorRefresh
            $donorAccess = [string]$donorToken.access_token
            $donorHeaders = Headers-For $donorAccess "full-it-donor"

            $listBatches = Invoke-Api -Method GET -Path "/api/v1/batches?page=1&page_size=100" -Headers $donorHeaders
            $listBatchesJson = Read-Json $listBatches
            Expect-Status -Name "donor list batches" -Response $listBatches -Expected 200
            Expect-Value -Name "donor batch list envelope" -Passed ($null -ne $listBatchesJson -and $null -ne $listBatchesJson.data -and $null -ne $listBatchesJson.page) -Details "paginated batch response present"

            $createKey = "full-create-$runID"
            $draft = @{ category = "ICT_EQUIPMENT"; quantity = 3; estimated_weight_kg = 2.50; condition_rating = "FUNCTIONAL"; is_data_bearing = $false; zone = "NORTH"; collection_deadline = (Get-Date).ToUniversalTime().AddHours(72).ToString("o"); notes = "full integration draft" }
            $created = Invoke-Api -Method POST -Path "/api/v1/batches" -Headers (Headers-For $donorAccess "full-create" $createKey) -Body $draft
            $createdJson = Read-Json $created
            Expect-Status -Name "create draft" -Response $created -Expected 201
            $newBatchID = [string]$createdJson.data.batch_id
            Expect-Value -Name "created draft identity" -Passed (-not [string]::IsNullOrWhiteSpace($newBatchID) -and [int]$createdJson.data.version -eq 1) -Details "batch_id=$newBatchID; version=$($createdJson.data.version)"
            Expect-Status -Name "create idempotency replay" -Response (Invoke-Api -Method POST -Path "/api/v1/batches" -Headers (Headers-For $donorAccess "full-create" $createKey) -Body $draft) -Expected 201

            $batchListAfterCreate = Read-Json (Invoke-Api -Method GET -Path "/api/v1/batches?status=DRAFT" -Headers $donorHeaders)
            Expect-Value -Name "draft appears in list" -Passed (@($batchListAfterCreate.data | Where-Object { $_.batch_id -eq $newBatchID }).Count -eq 1) -Details "batch_id=$newBatchID"
            Expect-Status -Name "get scoped draft" -Response (Invoke-Api -Method GET -Path "/api/v1/batches/$newBatchID" -Headers $donorHeaders) -Expected 200

            $edited = Invoke-Api -Method PATCH -Path "/api/v1/batches/$newBatchID" -Headers (Headers-For $donorAccess "full-edit" "full-edit-$runID" "1") -Body @{ notes = "full integration edited" }
            Expect-Status -Name "edit draft" -Response $edited -Expected 200
            $submitHeaders = Headers-For $donorAccess "full-submit" "full-submit-$runID" "2"
            $submitted = Invoke-Api -Method POST -Path "/api/v1/batches/$newBatchID/submit" -Headers $submitHeaders
            $submittedJson = Read-Json $submitted
            Expect-Status -Name "validate and submit" -Response $submitted -Expected 200
            Expect-Value -Name "submit lifecycle and outbox state" -Passed ($submittedJson.data.status -eq "SUBMITTED" -and $submittedJson.event_state -eq "PENDING") -Details "status=$($submittedJson.data.status); event_state=$($submittedJson.event_state)"
            Expect-Status -Name "submit idempotency replay" -Response (Invoke-Api -Method POST -Path "/api/v1/batches/$newBatchID/submit" -Headers $submitHeaders) -Expected 200
            Expect-Status -Name "submitted draft cannot be edited" -Response (Invoke-Api -Method PATCH -Path "/api/v1/batches/$newBatchID" -Headers (Headers-For $donorAccess "full-edit-after-submit" "full-edit-after-$runID" "3") -Body @{ notes = "must fail" }) -Expected 409

            $recyclerToken = Login "recycler1@ewaste.test"
            $recyclerAccess = [string]$recyclerToken.access_token
            $recyclerHeaders = Headers-For $recyclerAccess "full-recycler"
            $opportunities = Invoke-Api -Method GET -Path "/api/v1/opportunities?page=1&page_size=100" -Headers $recyclerHeaders
            $opportunitiesJson = Read-Json $opportunities
            Expect-Status -Name "recycler list opportunities" -Response $opportunities -Expected 200
            Expect-Value -Name "opportunity list contains three matches" -Passed (@($opportunitiesJson.data).Count -eq 3) -Details "count=$(@($opportunitiesJson.data).Count)"
            Expect-Status -Name "recycler get opportunity" -Response (Invoke-Api -Method GET -Path "/api/v1/opportunities/b2260000-0000-4000-8000-000000000001" -Headers $recyclerHeaders) -Expected 200
            $otherRecycler = Login "recycler2@ewaste.test"
            $otherRecyclerAccess = [string]$otherRecycler.access_token
            $otherOpportunities = Read-Json (Invoke-Api -Method GET -Path "/api/v1/opportunities?page=1&page_size=100" -Headers (Headers-For $otherRecyclerAccess "full-other-recycler"))
            Expect-Value -Name "opportunity organisation scope" -Passed (@($otherOpportunities.data).Count -eq 0) -Details "other recycler count=$(@($otherOpportunities.data).Count)"
            Expect-Status -Name "cross-organisation opportunity concealed" -Response (Invoke-Api -Method GET -Path "/api/v1/opportunities/b2260000-0000-4000-8000-000000000001" -Headers (Headers-For $otherRecyclerAccess "full-other-detail")) -Expected 404

            $batchIDs = @("b2260000-0000-4000-8000-000000000001", "b2260000-0000-4000-8000-000000000002", "b2260000-0000-4000-8000-000000000003")
            $claimResults = @{}
            foreach ($batchID in $batchIDs) {
                $claimBody = @{ expected_version = 3; claim_epoch = "1"; notes = "full integration claim" }
                $claimHeaders = Headers-For $recyclerAccess "full-claim-$batchID" "full-claim-$runID-$batchID" "3"
                $claim = Invoke-Api -Method POST -Path "/api/v1/batches/$batchID/claim" -Headers $claimHeaders -Body $claimBody
                $claimJson = Read-Json $claim
                Expect-Status -Name "claim $batchID" -Response $claim -Expected 200
                Expect-Value -Name "claim $batchID approved" -Passed ($claimJson.data.status -eq "APPROVED" -and $claimJson.data.event_state -eq "PENDING") -Details "status=$($claimJson.data.status); event_state=$($claimJson.data.event_state)"
                $claimResults[$batchID] = $claimJson
                Expect-Status -Name "claim replay $batchID" -Response (Invoke-Api -Method POST -Path "/api/v1/batches/$batchID/claim" -Headers $claimHeaders -Body $claimBody) -Expected 200
            }
            Expect-Status -Name "donor cannot claim" -Response (Invoke-Api -Method POST -Path "/api/v1/batches/$($batchIDs[0])/claim" -Headers (Headers-For $donorAccess "full-forbidden-claim" "full-forbidden-$runID" "3") -Body @{ expected_version = 3; claim_epoch = "1" }) -Expected 403

            $collectorToken = Login "collector1@ewaste.test"
            $collectorAccess = [string]$collectorToken.access_token
            $collectorHeaders = Headers-For $collectorAccess "full-collector"
            $availableBatches = Read-Json (Invoke-Api -Method GET -Path "/api/v1/batches?status=APPROVED&page=1&page_size=100" -Headers $collectorHeaders)
            Expect-Value -Name "collector approved batch scope" -Passed (@($availableBatches.data).Count -eq 3) -Details "approved_count=$(@($availableBatches.data).Count)"
            Expect-Status -Name "collector get approved batch" -Response (Invoke-Api -Method GET -Path "/api/v1/batches/$($batchIDs[0])" -Headers $collectorHeaders) -Expected 200
            $assignmentList = Read-Json (Invoke-Api -Method GET -Path "/api/v1/assignments?page=1&page_size=100" -Headers $collectorHeaders)
            Expect-Value -Name "collector assignment list initially empty" -Passed (@($assignmentList.data).Count -eq 0) -Details "assignment_count=$(@($assignmentList.data).Count)"

            $selectionResults = @{}
            foreach ($batchID in $batchIDs) {
                $selection = Invoke-Api -Method POST -Path "/api/v1/batches/$batchID/assignments" -Headers (Headers-For $collectorAccess "full-select-$batchID" "full-select-$runID-$batchID" "4") -Body @{ expected_version = 4; claim_epoch = "1"; collector_scope_id = "s2260000-0000-4000-8000-000000000001" }
                $selectionJson = Read-Json $selection
                Expect-Status -Name "select assignment $batchID" -Response $selection -Expected 201
                Expect-Value -Name "assignment $batchID accepted" -Passed ($selectionJson.data.assignment_status -eq "ACCEPTED") -Details "assignment_status=$($selectionJson.data.assignment_status)"
                $selectionResults[$batchID] = $selectionJson
                Expect-Status -Name "select replay $batchID" -Response (Invoke-Api -Method POST -Path "/api/v1/batches/$batchID/assignments" -Headers (Headers-For $collectorAccess "full-select-$batchID" "full-select-$runID-$batchID" "4") -Body @{ expected_version = 4; claim_epoch = "1"; collector_scope_id = "s2260000-0000-4000-8000-000000000001" }) -Expected 201
            }

            $rejectID = [string]$selectionResults[$batchIDs[0]].data.assignment_id
            $reject = Invoke-Api -Method POST -Path "/api/v1/assignments/$rejectID/reject" -Headers (Headers-For $collectorAccess "full-reject" "full-reject-$runID" "5") -Body @{ rejection_reason = "collector unavailable" }
            $rejectJson = Read-Json $reject
            Expect-Status -Name "reject assignment" -Response $reject -Expected 200
            Expect-Value -Name "rejected assignment superseded" -Passed ($rejectJson.data.assignment_status -eq "SUPERSEDED") -Details "assignment_status=$($rejectJson.data.assignment_status)"
            Expect-Status -Name "reject idempotency replay" -Response (Invoke-Api -Method POST -Path "/api/v1/assignments/$rejectID/reject" -Headers (Headers-For $collectorAccess "full-reject" "full-reject-$runID" "5") -Body @{ rejection_reason = "collector unavailable" }) -Expected 200
            Expect-Status -Name "legacy accept rejects non-pending assignment" -Response (Invoke-Api -Method POST -Path "/api/v1/assignments/$rejectID/accept" -Headers (Headers-For $collectorAccess "full-legacy-accept" "full-legacy-accept-$runID" "5")) -Expected 409

            $handoffID = [string]$selectionResults[$batchIDs[1]].data.assignment_id
            # The API requires assigned_at <= pickup_occurred_at <= server_now.
            # Keep only a small clock margin so this remains after assignment
            # creation while avoiding a future timestamp at request handling.
            $handoffBody = @{ pickup_occurred_at = (Get-Date).ToUniversalTime().AddMilliseconds(-50).ToString("o"); donor_representative_name = "Integration Donor"; actual_item_count = 2; verification_hash = ("a" * 64); notes = "full integration handoff" }
            $handoff = Invoke-Api -Method POST -Path "/api/v1/assignments/$handoffID/handoff" -Headers (Headers-For $collectorAccess "full-handoff" "full-handoff-$runID" "5") -Body $handoffBody
            $handoffJson = Read-Json $handoff
            Expect-Status -Name "record collection handoff" -Response $handoff -Expected 200
            Expect-Value -Name "handoff completes assignment" -Passed ($handoffJson.data.assignment_status -eq "COMPLETED") -Details "assignment_status=$($handoffJson.data.assignment_status)"
            Expect-Status -Name "handoff idempotency replay" -Response (Invoke-Api -Method POST -Path "/api/v1/assignments/$handoffID/handoff" -Headers (Headers-For $collectorAccess "full-handoff" "full-handoff-$runID" "5") -Body $handoffBody) -Expected 200

            $failID = [string]$selectionResults[$batchIDs[2]].data.assignment_id
            $failBody = @{ failure_reason = "DONOR_UNAVAILABLE"; observed_details = "No representative at pickup location" }
            $failed = Invoke-Api -Method POST -Path "/api/v1/assignments/$failID/fail" -Headers (Headers-For $collectorAccess "full-fail" "full-fail-$runID" "5") -Body $failBody
            $failedJson = Read-Json $failed
            Expect-Status -Name "report failed pickup" -Response $failed -Expected 200
            Expect-Value -Name "failed pickup closes assignment" -Passed ($failedJson.data.assignment_status -eq "FAILED") -Details "assignment_status=$($failedJson.data.assignment_status)"
            Expect-Status -Name "failed pickup idempotency replay" -Response (Invoke-Api -Method POST -Path "/api/v1/assignments/$failID/fail" -Headers (Headers-For $collectorAccess "full-fail" "full-fail-$runID" "5") -Body $failBody) -Expected 200

            $visibleAssignments = Read-Json (Invoke-Api -Method GET -Path "/api/v1/assignments?page=1&page_size=100" -Headers $collectorHeaders)
            Expect-Value -Name "collector assignment list contains lifecycle history" -Passed (@($visibleAssignments.data).Count -eq 3) -Details "assignment_count=$(@($visibleAssignments.data).Count)"
            Expect-Status -Name "collector get completed assignment" -Response (Invoke-Api -Method GET -Path "/api/v1/assignments/$handoffID" -Headers $collectorHeaders) -Expected 200
            Expect-Status -Name "donor cannot read assignment" -Response (Invoke-Api -Method GET -Path "/api/v1/assignments/$handoffID" -Headers $donorHeaders) -Expected 403

            $evidenceSQL = @"
SELECT IF(
    (SELECT COUNT(*) FROM batch_audit_events WHERE batch_id IN ('$($batchIDs[0])','$($batchIDs[1])','$($batchIDs[2])') AND event_type IN ('ClaimConfirmed','AssignmentRejected','CollectionCompleted','CollectionFailed')) >= 6
    AND (SELECT COUNT(*) FROM event_outbox WHERE batch_id IN ('$($batchIDs[0])','$($batchIDs[1])','$($batchIDs[2])') AND event_type IN ('ClaimConfirmed','CollectionCompleted','CollectionFailed') AND publish_state = 'PENDING') >= 5,
    'PASS', 'FAIL') AS result;
"@
            $evidence = Invoke-DockerMySqlText -Database $targetDatabase -SqlText $evidenceSQL
            Expect-Value -Name "audit and outbox evidence" -Passed (($evidence -join " ") -match "PASS") -Details ($evidence -join " ")

            Expect-Status -Name "donor logout" -Response (Invoke-Api -Method POST -Path "/api/v1/auth/logout" -Headers $donorHeaders) -Expected 204
            Expect-Status -Name "revoked donor token rejected" -Response (Invoke-Api -Method GET -Path "/api/v1/batches" -Headers $donorHeaders) -Expected 401
            Expect-Status -Name "collector logout" -Response (Invoke-Api -Method POST -Path "/api/v1/auth/logout" -Headers $collectorHeaders) -Expected 204
            Expect-Status -Name "recycler logout" -Response (Invoke-Api -Method POST -Path "/api/v1/auth/logout" -Headers $recyclerHeaders) -Expected 204
        }
        finally {
            Pop-Location
        }
    }
    catch {
        Add-Check -Name "full integration execution" -Passed $false -Details $_.Exception.Message
    }
}
finally {
    if ($null -ne $server -and -not $server.HasExited) {
        Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
        $server.WaitForExit()
    }
    try {
        Invoke-DockerMySqlText -Database "mysql" -SqlText "DROP DATABASE IF EXISTS ``$targetDatabase``;" | Out-Null
        Add-Check -Name "database cleanup" -Passed $true -Details "dropped=$targetDatabase"
    }
    catch { Add-Check -Name "database cleanup" -Passed $false -Details $_.Exception.Message }
    try {
        $redisResult = Invoke-DockerRedisCleanup
        Add-Check -Name "Redis cleanup" -Passed $true -Details ($redisResult -join " ")
    }
    catch { Add-Check -Name "Redis cleanup" -Passed $false -Details $_.Exception.Message }
    Remove-Item -LiteralPath $serverExecutable, $serverStdout, $serverStderr -Force -ErrorAction SilentlyContinue
    foreach ($name in $originalEnvironment.Keys) {
        if ($null -eq $originalEnvironment[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$name" $originalEnvironment[$name] }
    }
}

$finishedAt = Get-Date
$overallSuccess = ($results | Where-Object { $_.ExitCode -ne 0 }).Count -eq 0
$report = [System.Collections.Generic.List[string]]::new()
$report.Add("# Full backend integration report")
$report.Add("")
$report.Add("- Started: $($startedAt.ToString('o'))")
$report.Add("- Finished: $($finishedAt.ToString('o'))")
$report.Add("- Database: $targetDatabase")
$report.Add("- API: $baseURI")
$report.Add("- Overall result: $(if ($overallSuccess) { 'PASS' } else { 'FAIL' })")
$report.Add("")
foreach ($result in $results) {
    $state = if ($result.ExitCode -eq 0) { "PASS" } else { "FAIL" }
    $report.Add("## $($result.Name) - $state (exit $($result.ExitCode))")
    $report.Add("")
    $report.Add('```text')
    $report.Add(($result.Output -join [Environment]::NewLine))
    $report.Add('```')
    $report.Add("")
}
Set-Content -LiteralPath $ReportPath -Value ($report -join [Environment]::NewLine) -Encoding utf8
Get-Content -LiteralPath $ReportPath
if (-not $overallSuccess) { exit 1 }
