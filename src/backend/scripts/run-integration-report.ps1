[CmdletBinding()]
param(
    [string]$GoExecutable = "",
    [string]$MySqlHost = "127.0.0.1",
    [int]$MySqlPort = 3306,
    [string]$MySqlUser = "root",
    [string]$MySqlDatabase = "ewaste",
    [string]$MySqlPassword = "",
    [string]$RedisAddress = "127.0.0.1:6379",
    [int]$RedisDatabase = 0,
    [string]$RedisPassword = "",
    [string]$ApiAddress = "127.0.0.1:18080",
    [string]$TestUserEmail = "donor1@ewaste.test",
    [string]$TestUserPassword = "",
    [string]$MigrationSourceCommit = "847d243",
    [string]$ReportPath = "",
    [string]$ClaimUserEmail = "recycler1@ewaste.test",
    [string]$ClaimUserPassword = "",
    [string]$ClaimBatchID = "b1260000-0000-4000-8000-000000000003"
)

$ErrorActionPreference = "Stop"

$backendRoot = (Resolve-Path (Join-Path $PSScriptRoot ".." )).Path
$repoRoot = (Resolve-Path (Join-Path $backendRoot "..\.." )).Path
$runId = [guid]::NewGuid().ToString("N").Substring(0, 12)
$targetDatabase = "ewaste_it_$runId"

if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $ReportPath = Join-Path $backendRoot "test-results\integration-report.md"
}

if ([string]::IsNullOrWhiteSpace($GoExecutable)) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $goCommand) {
        $GoExecutable = $goCommand.Source
    }
}

if ([string]::IsNullOrWhiteSpace($GoExecutable)) {
    $goLandSdk = Join-Path $env:USERPROFILE "sdk\go1.26.1\bin\go.exe"
    if (Test-Path -LiteralPath $goLandSdk) {
        $GoExecutable = $goLandSdk
    }
}

if ([string]::IsNullOrWhiteSpace($GoExecutable) -or
    -not (Test-Path -LiteralPath $GoExecutable)) {
    throw "Go executable was not found. Pass -GoExecutable with the GoLand SDK path."
}

if ([string]::IsNullOrWhiteSpace($MySqlPassword)) {
    $MySqlPassword = $env:MYSQL_PASSWORD
    if ([string]::IsNullOrWhiteSpace($MySqlPassword)) {
        $MySqlPassword = $env:MYSQL_ROOT_PASSWORD
    }
}

if ([string]::IsNullOrWhiteSpace($RedisPassword)) {
    $RedisPassword = $env:REDIS_PASSWORD
}

if ([string]::IsNullOrWhiteSpace($TestUserPassword)) {
    $TestUserPassword = $env:EWASTE_TEST_USER_PASSWORD
}

if ([string]::IsNullOrWhiteSpace($ClaimUserPassword)) {
    $ClaimUserPassword = $env:EWASTE_CLAIM_USER_PASSWORD
}

$reportDirectory = Split-Path -Parent $ReportPath
New-Item -ItemType Directory -Force -Path $reportDirectory | Out-Null

$startedAt = Get-Date
$results = [System.Collections.Generic.List[object]]::new()

function Invoke-GoCheck {
    param(
        [string]$Name,
        [string[]]$Arguments
    )

    $output = @(& $GoExecutable @Arguments 2>&1 | ForEach-Object { [string]$_ })
    $exitCode = $LASTEXITCODE

    $results.Add([pscustomobject]@{
        Name = $Name
        ExitCode = $exitCode
        Output = $output
    })
}

function Protect-ApiContent {
    param([string]$Content)

    if ([string]::IsNullOrWhiteSpace($Content)) {
        return "(no response body)"
    }

    try {
        $json = $Content | ConvertFrom-Json
        foreach ($propertyName in @("access_token", "refresh_token")) {
            if ($null -ne $json.PSObject.Properties[$propertyName]) {
                $json.$propertyName = "<redacted>"
            }
        }
        return ($json | ConvertTo-Json -Compress -Depth 10)
    }
    catch {
        return $Content
    }
}

function Invoke-ApiRequest {
    param(
        [string]$Method,
        [string]$Uri,
        [hashtable]$Headers = @{},
        [string]$Body = ""
    )

    try {
        $requestParameters = @{
            UseBasicParsing = $true
            Uri = $Uri
            Method = $Method
            Headers = $Headers
            ErrorAction = "Stop"
        }
        if (-not [string]::IsNullOrWhiteSpace($Body)) {
            $requestParameters.Body = $Body
            $requestParameters.ContentType = "application/json"
        }

        $response = Invoke-WebRequest @requestParameters
        return [pscustomobject]@{
            StatusCode = [int]$response.StatusCode
            Headers = $response.Headers
            Content = [string]$response.Content
        }
    }
    catch {
        $errorResponse = $_.Exception.Response
        if ($null -eq $errorResponse) {
            return [pscustomobject]@{
                StatusCode = 0
                Headers = @{}
                Content = $_.Exception.Message
            }
        }

        $reader = New-Object System.IO.StreamReader($errorResponse.GetResponseStream())
        try {
            $content = $reader.ReadToEnd()
        }
        finally {
            $reader.Dispose()
        }

        return [pscustomobject]@{
            StatusCode = [int]$errorResponse.StatusCode
            Headers = $errorResponse.Headers
            Content = $content
        }
    }
}

function Add-ApiResult {
    param(
        [string]$Name,
        [pscustomobject]$Response,
        [int]$ExpectedStatus,
        [string]$ExpectedHeaderName = "",
        [string]$ExpectedHeaderValue = ""
    )

    $passed = $Response.StatusCode -eq $ExpectedStatus
    $details = @(
        "status_code=$($Response.StatusCode)"
        "expected_status=$ExpectedStatus"
    )

    if (-not [string]::IsNullOrWhiteSpace($ExpectedHeaderName)) {
        $actualHeaderValue = [string]$Response.Headers[$ExpectedHeaderName]
        $details += "header_${ExpectedHeaderName}=$actualHeaderValue"
        $passed = $passed -and ($actualHeaderValue -eq $ExpectedHeaderValue)
    }

    $details += "body=$(Protect-ApiContent $Response.Content)"
    [void]$results.Add([pscustomobject]@{
        Name = $Name
        ExitCode = if ($passed) { 0 } else { 1 }
        Output = $details
    })
}

function Add-AssertionResult {
    param(
        [string]$Name,
        [bool]$Passed,
        [string]$Details
    )

    [void]$results.Add([pscustomobject]@{
        Name = $Name
        ExitCode = if ($Passed) { 0 } else { 1 }
        Output = @($Details)
    })
}

function Get-SqlText {
    param([string]$RelativePath)

    $workingTreePath = Join-Path $repoRoot $RelativePath
    if (Test-Path -LiteralPath $workingTreePath) {
        $rawSql = Get-Content -LiteralPath $workingTreePath -Raw
    }
    else {
        $rawSql = & git -C $repoRoot show "$MigrationSourceCommit`:$RelativePath" 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "SQL source is unavailable: $RelativePath at commit $MigrationSourceCommit"
        }
        $rawSql = $rawSql -join [Environment]::NewLine
    }

    if ($RelativePath -eq "database/seed/104-seed-c1-batches.sql") {
        $rawSql = $rawSql -replace "REPEAT\('界',\s*500\)", "REPEAT('x', 500)"
    }

    return (($rawSql -split "`r?`n" |
        Where-Object { $_ -notmatch '^\s*--(liquibase|changeset|precondition|comment|rollback)' }) -join [Environment]::NewLine)
}

function Invoke-DockerMySqlText {
    param(
        [string]$Database,
        [string]$SqlText
    )

    $clientHost = if ($MySqlHost -in @("localhost", "127.0.0.1")) {
        "host.docker.internal"
    }
    else {
        $MySqlHost
    }

    $arguments = @(
        "run", "--rm", "-i",
        "-e", "MYSQL_PWD=$MySqlPassword",
        "mysql:8.4",
        "mysql", "--protocol=TCP", "-h", $clientHost,
        "-P", [string]$MySqlPort, "-u", $MySqlUser,
        "--default-character-set=utf8mb4"
    )
    if (-not [string]::IsNullOrWhiteSpace($Database)) {
        $arguments += @("-D", $Database)
    }

    $output = @($SqlText | & docker @arguments 2>&1 | ForEach-Object { [string]$_ })
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "MySQL command failed with exit code ${exitCode}: $($output -join ' ')"
    }

    return $output
}

function Invoke-DockerRedisCleanup {
    $clientHost = if ($RedisAddress.StartsWith("127.0.0.1") -or $RedisAddress.StartsWith("localhost")) {
        "host.docker.internal"
    }
    else {
        $RedisAddress.Split(":")[0]
    }

    $clientPort = if ($RedisAddress.Contains(":")) {
        $RedisAddress.Split(":")[-1]
    }
    else {
        "6379"
    }

    $arguments = @("run", "--rm")
    if (-not [string]::IsNullOrWhiteSpace($RedisPassword)) {
        $arguments += @("-e", "REDISCLI_AUTH=$RedisPassword")
    }
    $arguments += @(
        "redis:7.4-alpine", "redis-cli", "-h", $clientHost,
        "-p", $clientPort, "-n", [string]$RedisDatabase,
        "DEL", "ewaste:rate:127.0.0.1"
    )

    $output = @(& docker @arguments 2>&1 | ForEach-Object { [string]$_ })
    if ($LASTEXITCODE -ne 0) {
        throw "Redis cleanup failed: $($output -join ' ')"
    }

    return $output
}

function Initialize-IsolatedDatabase {
    try {
        $createDatabaseSql = "CREATE DATABASE ``$targetDatabase`` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
        Invoke-DockerMySqlText -Database "mysql" -SqlText $createDatabaseSql | Out-Null

        $sqlFiles = @(
            "database/changes/001-create-organisations.sql",
            "database/changes/002-create-roles.sql",
            "database/changes/003-create-users.sql",
            "database/changes/004-create-sessions.sql",
            "database/changes/005-create-login-audit.sql",
            "database/seed/101-seed-organisations.sql",
            "database/seed/102-seed-roles.sql",
            "database/seed/103-seed-users.sql",
            "database/changes/006-create-ewaste-batches.sql",
            "database/changes/007-add-ewaste-batch-constraints.sql",
            "database/changes/008-create-command-idempotency.sql",
            "database/changes/009-create-batch-audit-events.sql",
            "database/changes/010-create-event-outbox.sql",
            "database/changes/011-create-matching-rule-sets.sql",
            "database/changes/012-create-recycler-matching-profiles.sql",
            "database/changes/013-create-recycler-capacity-pools.sql",
            "database/changes/014-create-recycler-category-capabilities.sql",
            "database/changes/015-create-recycler-service-zones.sql",
            "database/changes/016-create-matching-decisions.sql",
            "database/changes/017-create-matched-results.sql",
            "database/changes/018-create-batch-claims.sql",
            "database/changes/019-create-capacity-reservations.sql",
            "database/changes/020-link-current-claim-and-audit.sql",
            "database/seed/104-seed-c1-batches.sql"
        )

        $applied = [System.Collections.Generic.List[string]]::new()
        foreach ($sqlFile in $sqlFiles) {
            Invoke-DockerMySqlText -Database $targetDatabase -SqlText (Get-SqlText $sqlFile) | Out-Null
            $applied.Add($sqlFile)
        }

        $localC3Fixture = @'
SET time_zone = '+00:00';

INSERT INTO matching_rule_sets (
    id, version, rules_json, effective_from, created_by, created_at
) VALUES (
    'r1260000-0000-4000-8000-000000000001',
    'local-c3-v1',
    JSON_OBJECT('fixture', TRUE),
    UTC_TIMESTAMP(6),
    'USR-001',
    UTC_TIMESTAMP(6)
);

INSERT INTO recycler_matching_profiles (
    recycler_org_id, is_active, version, created_at, updated_at
) VALUES (
    'PROC-001', TRUE, 1, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)
);

INSERT INTO recycler_capacity_pools (
    id, recycler_org_id, pool_code, total_kg, reserved_kg,
    is_active, version, updated_at
) VALUES (
    'p1260000-0000-4000-8000-000000000001',
    'PROC-001',
    'GENERAL',
    1000.00,
    0.00,
    TRUE,
    1,
    UTC_TIMESTAMP(6)
);

INSERT INTO recycler_category_capabilities (
    id, recycler_org_id, category, accepted_conditions_json,
    supports_data_bearing, is_active, capacity_pool_id, version, updated_at
) VALUES (
    'c1260000-0000-4000-8000-000000000001',
    'PROC-001',
    'ICT_EQUIPMENT',
    JSON_ARRAY('FUNCTIONAL', 'REPAIRABLE'),
    FALSE,
    TRUE,
    'p1260000-0000-4000-8000-000000000001',
    1,
    UTC_TIMESTAMP(6)
);

INSERT INTO recycler_service_zones (
    id, recycler_org_id, zone, minimum_lead_minutes,
    is_active, version, updated_at
) VALUES (
    'z1260000-0000-4000-8000-000000000001',
    'PROC-001',
    'NORTH',
    0,
    TRUE,
    1,
    UTC_TIMESTAMP(6)
);

UPDATE ewaste_batches
SET status = 'MATCHED',
    version = 3,
    collection_deadline = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 72 HOUR),
    updated_at = UTC_TIMESTAMP(6)
WHERE id = 'b1260000-0000-4000-8000-000000000003';

INSERT INTO matching_decisions (
    id, batch_id, trigger_id, trigger_type, batch_version, claim_epoch,
    rule_set_id, evaluation_at, input_hash, profile_snapshot_hash,
    input_snapshot_json, outcome, primary_reason, evaluated_count,
    eligible_count, correlation_id, created_at, completed_at
) VALUES (
    'd1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000003',
    't1260000-0000-4000-8000-000000000001',
    'REQUEST_SUBMITTED',
    2,
    1,
    'r1260000-0000-4000-8000-000000000001',
    UTC_TIMESTAMP(6),
    SHA2('local-c3-input', 256),
    SHA2('local-c3-profile', 256),
    JSON_OBJECT('fixture', TRUE),
    'MATCHED',
    'ELIGIBLE_EXISTS',
    1,
    1,
    'local-c3-matching',
    UTC_TIMESTAMP(6),
    UTC_TIMESTAMP(6)
);

INSERT INTO matched_results (
    id, decision_id, batch_id, recycler_org_id, profile_version,
    category_match, capability_match, capacity_available, zone_match,
    deadline_viable, is_matched, available_capacity_kg, capacity_pool_id,
    capacity_version, minimum_lead_minutes, feasible_at, reason_code,
    failed_rules_json, evidence_json, created_at
) VALUES (
    'm1260000-0000-4000-8000-000000000001',
    'd1260000-0000-4000-8000-000000000001',
    'b1260000-0000-4000-8000-000000000003',
    'PROC-001',
    1,
    TRUE,
    TRUE,
    TRUE,
    TRUE,
    TRUE,
    TRUE,
    1000.00,
    'p1260000-0000-4000-8000-000000000001',
    1,
    0,
    UTC_TIMESTAMP(6),
    'ELIGIBLE',
    JSON_ARRAY(),
    JSON_OBJECT('fixture', TRUE),
    UTC_TIMESTAMP(6)
);
'@
        Invoke-DockerMySqlText -Database $targetDatabase -SqlText $localC3Fixture | Out-Null
        $applied.Add("temporary inline C3 matching/claim fixture")

        [void]$results.Add([pscustomobject]@{
            Name = "database migration and seeding"
            ExitCode = 0
            Output = @(
                "database=$targetDatabase"
                "applied=$($applied -join ', ')"
            )
        })
        return $true
    }
    catch {
        [void]$results.Add([pscustomobject]@{
            Name = "database migration and seeding"
            ExitCode = 1
            Output = @($_.Exception.Message)
        })
        return $false
    }
}

$originalEnvironment = @{
    EWASTE_MODE = $env:EWASTE_MODE
    EWASTE_SERVER_PORT = $env:EWASTE_SERVER_PORT
    EWASTE_DATABASE_HOST = $env:EWASTE_DATABASE_HOST
    EWASTE_DATABASE_PORT = $env:EWASTE_DATABASE_PORT
    EWASTE_DATABASE_NAME = $env:EWASTE_DATABASE_NAME
    EWASTE_DATABASE_USER = $env:EWASTE_DATABASE_USER
    MYSQL_PASSWORD = $env:MYSQL_PASSWORD
    EWASTE_REDIS_ADDRESS = $env:EWASTE_REDIS_ADDRESS
    EWASTE_REDIS_DB = $env:EWASTE_REDIS_DB
    REDIS_PASSWORD = $env:REDIS_PASSWORD
    EWASTE_AUTH_ISSUER = $env:EWASTE_AUTH_ISSUER
    EWASTE_AUTH_ACCESS_SECRET = $env:EWASTE_AUTH_ACCESS_SECRET
    EWASTE_AUTH_REFRESH_SECRET = $env:EWASTE_AUTH_REFRESH_SECRET
    EWASTE_AUTH_REFRESH_HASH_SECRET = $env:EWASTE_AUTH_REFRESH_HASH_SECRET
    EWASTE_LOGGING_FILE_PATH = $env:EWASTE_LOGGING_FILE_PATH
}

$server = $null
$serverExecutable = Join-Path ([System.IO.Path]::GetTempPath()) "ewaste-workflow-api-$([guid]::NewGuid().ToString('N')).exe"
$serverStdout = "$serverExecutable.stdout.log"
$serverStderr = "$serverExecutable.stderr.log"
$databaseReady = Initialize-IsolatedDatabase

try {
    if (-not $databaseReady) {
        Add-AssertionResult -Name "integration setup" -Passed $false -Details "database migration and seeding failed"
    }
    else {
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
        $env:EWASTE_AUTH_ISSUER = "ewaste-integration-test"
        $env:EWASTE_AUTH_ACCESS_SECRET = "integration-access-secret"
        $env:EWASTE_AUTH_REFRESH_SECRET = "integration-refresh-secret"
        $env:EWASTE_AUTH_REFRESH_HASH_SECRET = "integration-refresh-hash-secret"
        $env:EWASTE_LOGGING_FILE_PATH = Join-Path ([System.IO.Path]::GetTempPath()) "ewaste-workflow-api-integration.log"

        Push-Location $backendRoot
        try {
        Invoke-GoCheck -Name "live dependency check" -Arguments @(
            "run", "./cmd/server", "-mode", "test", "-check-dependencies"
        )
        Invoke-GoCheck -Name "go test ./..." -Arguments @("test", "./...")
        Invoke-GoCheck -Name "go vet ./..." -Arguments @("vet", "./...")

        Invoke-GoCheck -Name "build integration server" -Arguments @(
            "build", "-o", $serverExecutable, "./cmd/server"
        )

        $buildResult = $results | Where-Object { $_.Name -eq "build integration server" } | Select-Object -Last 1
        if ($buildResult.ExitCode -eq 0) {
            $server = Start-Process -FilePath $serverExecutable -WorkingDirectory $backendRoot -PassThru -WindowStyle Hidden -RedirectStandardOutput $serverStdout -RedirectStandardError $serverStderr
            try {
                $baseUri = "http://$ApiAddress"
                $lastReadiness = $null

                for ($attempt = 1; $attempt -le 30; $attempt++) {
                    $lastReadiness = Invoke-ApiRequest -Method "GET" -Uri "$baseUri/readyz"
                    if ($lastReadiness.StatusCode -eq 200) {
                        break
                    }
                    Start-Sleep -Milliseconds 500
                }

                Add-ApiResult -Name "API readiness" -Response $lastReadiness -ExpectedStatus 200

                if ($lastReadiness.StatusCode -eq 200) {
                Add-ApiResult -Name "API liveness" -Response (Invoke-ApiRequest -Method "GET" -Uri "$baseUri/healthz") -ExpectedStatus 200
                Add-ApiResult -Name "Hello endpoint" -Response (Invoke-ApiRequest -Method "GET" -Uri "$baseUri/api/v1/hello") -ExpectedStatus 200

                $corsHeaders = @{
                    Origin = "http://localhost:3000"
                    "Access-Control-Request-Method" = "POST"
                }
                Add-ApiResult -Name "CORS preflight for batch API" -Response (Invoke-ApiRequest -Method "OPTIONS" -Uri "$baseUri/api/v1/batches" -Headers $corsHeaders) -ExpectedStatus 204 -ExpectedHeaderName "Access-Control-Allow-Origin" -ExpectedHeaderValue "http://localhost:3000"

                $unauthorizedHeaders = @{ "X-Correlation-ID" = "integration-unauthorized" }
                Add-ApiResult -Name "Protected batch rejects anonymous request" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches" -Headers $unauthorizedHeaders -Body "{}") -ExpectedStatus 401

                $loginBody = @{ email = $TestUserEmail; password = $TestUserPassword } | ConvertTo-Json -Compress
                $loginResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/auth/login" -Headers @{ "X-Correlation-ID" = "integration-login" } -Body $loginBody
                Add-ApiResult -Name "Donor login" -Response $loginResponse -ExpectedStatus 200

                $loginJson = $null
                if ($loginResponse.StatusCode -eq 200) {
                    try { $loginJson = $loginResponse.Content | ConvertFrom-Json } catch { $loginJson = $null }
                }
                $accessToken = if ($null -ne $loginJson) { [string]$loginJson.access_token } else { "" }
                Add-AssertionResult -Name "Login returns access token" -Passed (-not [string]::IsNullOrWhiteSpace($accessToken)) -Details "access_token_present=$(-not [string]::IsNullOrWhiteSpace($accessToken))"

                if (-not [string]::IsNullOrWhiteSpace($accessToken)) {
                    $runId = [guid]::NewGuid().ToString("N").Substring(0, 12)
                    $authHeaders = @{
                        Authorization = "Bearer $accessToken"
                        "X-Correlation-ID" = "integration-$runId"
                    }
                    $deadline = (Get-Date).ToUniversalTime().AddHours(72).ToString("o")
                    $draftBody = @{
                        category = "ICT_EQUIPMENT"
                        quantity = 3
                        estimated_weight_kg = 2.50
                        condition_rating = "FUNCTIONAL"
                        is_data_bearing = $false
                        zone = "CENTRAL"
                        collection_deadline = $deadline
                        notes = "integration test batch"
                    } | ConvertTo-Json -Compress

                    $createHeaders = $authHeaders.Clone()
                    $createHeaders["Idempotency-Key"] = "create-$runId"
                    $createResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches" -Headers $createHeaders -Body $draftBody
                    Add-ApiResult -Name "Create batch draft" -Response $createResponse -ExpectedStatus 201

                    $createJson = $null
                    if ($createResponse.StatusCode -eq 201) {
                        try { $createJson = $createResponse.Content | ConvertFrom-Json } catch { $createJson = $null }
                    }
                    $batchId = if ($null -ne $createJson) { [string]$createJson.data.batch_id } else { "" }
                    $version = if ($null -ne $createJson) { [int]$createJson.data.version } else { 0 }
                    Add-AssertionResult -Name "Create response contains draft identity" -Passed (-not [string]::IsNullOrWhiteSpace($batchId) -and $version -eq 1) -Details "batch_id_present=$(-not [string]::IsNullOrWhiteSpace($batchId)); version=$version"

                    if (-not [string]::IsNullOrWhiteSpace($batchId)) {
                        $replayResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches" -Headers $createHeaders -Body $draftBody
                        Add-ApiResult -Name "Create idempotency replay" -Response $replayResponse -ExpectedStatus 201

                        $editHeaders = $authHeaders.Clone()
                        $editHeaders["Idempotency-Key"] = "edit-$runId"
                        $editHeaders["If-Match-Version"] = [string]$version
                        $editBody = @{ notes = "integration test batch edited" } | ConvertTo-Json -Compress
                        $editResponse = Invoke-ApiRequest -Method "PATCH" -Uri "$baseUri/api/v1/batches/$batchId" -Headers $editHeaders -Body $editBody
                        Add-ApiResult -Name "Edit draft" -Response $editResponse -ExpectedStatus 200

                        $submitHeaders = $authHeaders.Clone()
                        $submitHeaders["Idempotency-Key"] = "submit-$runId"
                        $submitHeaders["If-Match-Version"] = "2"
                        $submitResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches/$batchId/submit" -Headers $submitHeaders
                        Add-ApiResult -Name "Validate and submit batch" -Response $submitResponse -ExpectedStatus 200

                        $submitJson = $null
                        if ($submitResponse.StatusCode -eq 200) {
                            try { $submitJson = $submitResponse.Content | ConvertFrom-Json } catch { $submitJson = $null }
                        }
                        $submittedStatus = if ($null -ne $submitJson) { [string]$submitJson.data.status } else { "" }
                        $eventState = if ($null -ne $submitJson) { [string]$submitJson.event_state } else { "" }
                        Add-AssertionResult -Name "Submit response records lifecycle and outbox state" -Passed ($submittedStatus -eq "SUBMITTED" -and $eventState -eq "PENDING") -Details "status=$submittedStatus; event_state=$eventState"

                        Add-ApiResult -Name "Submit idempotency replay" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches/$batchId/submit" -Headers $submitHeaders) -ExpectedStatus 200

                        $postSubmitEditHeaders = $authHeaders.Clone()
                        $postSubmitEditHeaders["Idempotency-Key"] = "edit-submitted-$runId"
                        $postSubmitEditHeaders["If-Match-Version"] = "3"
                        Add-ApiResult -Name "Submitted batch rejects edit" -Response (Invoke-ApiRequest -Method "PATCH" -Uri "$baseUri/api/v1/batches/$batchId" -Headers $postSubmitEditHeaders -Body $editBody) -ExpectedStatus 409
                    }

                    $forbiddenClaimHeaders = $authHeaders.Clone()
                    $forbiddenClaimHeaders["Idempotency-Key"] = "forbidden-claim-$runId"
                    $forbiddenClaimHeaders["If-Match-Version"] = "3"
                    $claimBody = @{
                        expected_version = 3
                        claim_epoch = "1"
                        notes = "local C3 integration claim"
                    } | ConvertTo-Json -Compress
                    Add-ApiResult -Name "Donor claim is forbidden" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches/$ClaimBatchID/claim" -Headers $forbiddenClaimHeaders -Body $claimBody) -ExpectedStatus 403

                    $claimLoginBody = @{ email = $ClaimUserEmail; password = $ClaimUserPassword } | ConvertTo-Json -Compress
                    $claimLoginResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/auth/login" -Headers @{ "X-Correlation-ID" = "integration-claim-login" } -Body $claimLoginBody
                    Add-ApiResult -Name "Recycler login" -Response $claimLoginResponse -ExpectedStatus 200

                    $claimLoginJson = $null
                    if ($claimLoginResponse.StatusCode -eq 200) {
                        try { $claimLoginJson = $claimLoginResponse.Content | ConvertFrom-Json } catch { $claimLoginJson = $null }
                    }
                    $claimAccessToken = if ($null -ne $claimLoginJson) { [string]$claimLoginJson.access_token } else { "" }
                    Add-AssertionResult -Name "Recycler login returns access token" -Passed (-not [string]::IsNullOrWhiteSpace($claimAccessToken)) -Details "access_token_present=$(-not [string]::IsNullOrWhiteSpace($claimAccessToken))"

                    if (-not [string]::IsNullOrWhiteSpace($claimAccessToken)) {
                        $claimCorrelationID = "integration-claim-$runId"
                        $claimHeaders = @{
                            Authorization = "Bearer $claimAccessToken"
                            "X-Correlation-ID" = $claimCorrelationID
                            "Idempotency-Key" = "claim-success-$runId"
                            "If-Match-Version" = "3"
                        }
                        $claimResponse = Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches/$ClaimBatchID/claim" -Headers $claimHeaders -Body $claimBody
                        Add-ApiResult -Name "Recycler claims matched batch" -Response $claimResponse -ExpectedStatus 200

                        $claimJson = $null
                        if ($claimResponse.StatusCode -eq 200) {
                            try { $claimJson = $claimResponse.Content | ConvertFrom-Json } catch { $claimJson = $null }
                        }
                        $claimStatus = if ($null -ne $claimJson) { [string]$claimJson.data.status } else { "" }
                        $claimEventState = if ($null -ne $claimJson) { [string]$claimJson.data.event_state } else { "" }
                        Add-AssertionResult -Name "Claim response records approval and outbox state" -Passed ($claimStatus -eq "APPROVED" -and $claimEventState -eq "PENDING") -Details "status=$claimStatus; event_state=$claimEventState"

                        Add-ApiResult -Name "Claim idempotency replay" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches/$ClaimBatchID/claim" -Headers $claimHeaders -Body $claimBody) -ExpectedStatus 200

                        $claimEvidenceSQL = @"
SELECT IF(
    (SELECT COUNT(*) FROM ewaste_batches WHERE id = '$ClaimBatchID' AND status = 'APPROVED' AND version = 4) = 1
    AND (SELECT COUNT(*) FROM batch_claims WHERE batch_id = '$ClaimBatchID' AND claim_status = 'ACCEPTED') = 1
    AND (SELECT COUNT(*) FROM capacity_reservations WHERE batch_id = '$ClaimBatchID' AND status = 'RESERVED') = 1
    AND (SELECT COUNT(*) FROM batch_audit_events WHERE batch_id = '$ClaimBatchID' AND event_type = 'ClaimConfirmed' AND correlation_id = '$claimCorrelationID') = 1
    AND (SELECT COUNT(*) FROM event_outbox WHERE batch_id = '$ClaimBatchID' AND event_type = 'ClaimConfirmed' AND publish_state = 'PENDING') = 1,
    'PASS',
    'FAIL'
) AS result;
"@
                        $claimEvidence = Invoke-DockerMySqlText -Database $targetDatabase -SqlText $claimEvidenceSQL
                        Add-AssertionResult -Name "Claim audit and outbox evidence" -Passed (($claimEvidence -join " ") -match "PASS") -Details ($claimEvidence -join " ")
                    }

                    Add-ApiResult -Name "Logout revokes session" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/auth/logout" -Headers $authHeaders) -ExpectedStatus 204
                    Add-ApiResult -Name "Revoked access token is rejected" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/batches" -Headers $authHeaders -Body "{}") -ExpectedStatus 401

                    if (-not [string]::IsNullOrWhiteSpace($claimAccessToken)) {
                        Add-ApiResult -Name "Recycler logout revokes session" -Response (Invoke-ApiRequest -Method "POST" -Uri "$baseUri/api/v1/auth/logout" -Headers @{ Authorization = "Bearer $claimAccessToken"; "X-Correlation-ID" = "integration-claim-logout" }) -ExpectedStatus 204
                    }
                }
            }
            }
            finally {
                if ($null -ne $server -and -not $server.HasExited) {
                    Stop-Process -Id $server.Id -Force
                    $server.WaitForExit()
                }
            }
        }
        else {
            Add-AssertionResult -Name "API functionality checks" -Passed $false -Details "integration server build failed"
        }
    }
        finally {
            Pop-Location
        }
    }
}
finally {
    foreach ($name in $originalEnvironment.Keys) {
        Set-Item -Path "Env:$name" -Value $originalEnvironment[$name]
    }

    try {
        $dropDatabaseSql = "DROP DATABASE IF EXISTS ``$targetDatabase``;"
        Invoke-DockerMySqlText -Database "mysql" -SqlText $dropDatabaseSql | Out-Null
        Add-AssertionResult -Name "database cleanup" -Passed $true -Details "dropped=$targetDatabase"
    }
    catch {
        Add-AssertionResult -Name "database cleanup" -Passed $false -Details $_.Exception.Message
    }

    try {
        $redisCleanupOutput = Invoke-DockerRedisCleanup
        Add-AssertionResult -Name "Redis cleanup" -Passed $true -Details "deleted_rate_limit_key=$($redisCleanupOutput -join ' ')"
    }
    catch {
        Add-AssertionResult -Name "Redis cleanup" -Passed $false -Details $_.Exception.Message
    }

    Remove-Item -LiteralPath $serverExecutable, $serverStdout, $serverStderr -Force -ErrorAction SilentlyContinue
}

$finishedAt = Get-Date
$overallSuccess = ($results | Where-Object { $_.ExitCode -ne 0 }).Count -eq 0

$report = [System.Collections.Generic.List[string]]::new()
$report.Add("# Backend integration report")
$report.Add("")
$report.Add("- Started: $($startedAt.ToString('o'))")
$report.Add("- Finished: $($finishedAt.ToString('o'))")
$report.Add("- Go: $GoExecutable")
$report.Add("- MySQL endpoint: $MySqlHost`:$MySqlPort/$targetDatabase as $MySqlUser")
$report.Add("- Redis endpoint: $RedisAddress, database $RedisDatabase")
$report.Add("- Migration source: canonical auth migrations/seeds plus design commit $MigrationSourceCommit batch migrations/fixtures")
$report.Add("- Credentials: supplied at runtime and omitted from this report")
$report.Add("- Overall result: $(if ($overallSuccess) { 'PASS' } else { 'FAIL' })")
$report.Add("")

foreach ($result in $results) {
    $status = if ($result.ExitCode -eq 0) { "PASS" } else { "FAIL" }
    $report.Add("## $($result.Name) - $status (exit $($result.ExitCode))")
    $report.Add("")
    $report.Add('```text')
    if ($result.Output.Count -eq 0) {
        $report.Add("(no output)")
    }
    else {
        $result.Output | ForEach-Object { $report.Add($_) }
    }
    $report.Add('```')
    $report.Add("")
}

[System.IO.File]::WriteAllText(
    $ReportPath,
    ($report -join [Environment]::NewLine),
    [System.Text.UTF8Encoding]::new($false)
)

Get-Content -LiteralPath $ReportPath

if (-not $overallSuccess) {
    exit 1
}
