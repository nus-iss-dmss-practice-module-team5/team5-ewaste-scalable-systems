[CmdletBinding()]
param(
    [string]$BaseUrl = "https://aca-ewaste-dev-api.kindflower-300f4866.malaysiawest.azurecontainerapps.io",
    [string]$Password = "TestOnly#2026!",
    [int]$MatchTimeoutSeconds = 180,
    [string]$ReportPath = ""
)

$ErrorActionPreference = "Stop"
$runId = [guid]::NewGuid().ToString("N")
$checks = [System.Collections.Generic.List[object]]::new()
$evidence = [System.Collections.Generic.List[string]]::new()
$base = $BaseUrl.TrimEnd("/")
$reportWritten = $false

if ([string]::IsNullOrWhiteSpace($ReportPath)) {
    $backendRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
    $ReportPath = Join-Path $backendRoot "test-results\s2-x-v-j-01-remote-report.md"
}

function Add-Check {
    param([string]$Name, [bool]$Passed, [string]$Details)
    [void]$checks.Add([pscustomobject]@{ Name = $Name; Passed = $Passed; Details = $Details })
    $mark = if ($Passed) { "PASS" } else { "FAIL" }
    Write-Host ("[{0}] {1} - {2}" -f $mark, $Name, $Details)
}

function Add-Evidence {
    param([string]$Text)
    [void]$evidence.Add($Text)
}

function UtcStamp {
    param([datetime]$Value)
    return $Value.ToUniversalTime().ToString("yyyy-MM-dd'T'HH:mm:ss.ffffff'Z'", [Globalization.CultureInfo]::InvariantCulture)
}

function New-Headers {
    param(
        [string]$Token = "",
        [string]$CorrelationId = "",
        [string]$IdempotencyKey = "",
        [string]$Version = ""
    )
    $headers = @{}
    if ($Token) { $headers["Authorization"] = "Bearer $Token" }
    if ($CorrelationId) { $headers["X-Correlation-ID"] = $CorrelationId }
    if ($IdempotencyKey) { $headers["Idempotency-Key"] = $IdempotencyKey }
    if ($Version) { $headers["If-Match-Version"] = $Version }
    return $headers
}

function Read-ResponseBody {
    param($Response)
    if ($null -eq $Response) { return "" }
    try {
        $stream = $Response.GetResponseStream()
        if ($null -eq $stream) { return "" }
        $reader = [IO.StreamReader]::new($stream)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose(); $stream.Dispose() }
    } catch { return "" }
}

function Invoke-Api {
    param(
        [ValidateSet("GET", "POST", "PATCH")][string]$Method,
        [string]$Path,
        [hashtable]$Headers = @{},
        [object]$Body = $null
    )
    $request = @{
        Method = $Method
        Uri = "$base$Path"
        Headers = $Headers
        UseBasicParsing = $true
        ErrorAction = "Stop"
    }
    if ($null -ne $Body) {
        $request.Body = ($Body | ConvertTo-Json -Depth 20 -Compress)
        $request.ContentType = "application/json"
    }
    try {
        $response = Invoke-WebRequest @request
        $content = [string]$response.Content
        $status = [int]$response.StatusCode
        $responseHeaders = $response.Headers
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response) { throw }
        $content = Read-ResponseBody $response
        $status = [int]$response.StatusCode
        $responseHeaders = $response.Headers
    }
    $json = $null
    if (-not [string]::IsNullOrWhiteSpace($content)) {
        try { $json = ConvertFrom-Json -InputObject ([string]$content) } catch { $json = $null }
    }
    return [pscustomobject]@{
        StatusCode = $status
        Content = $content
        Json = $json
        Headers = $responseHeaders
    }
}

function Check-Response {
    param([string]$Name, $Response, [int]$ExpectedStatus)
    $statusOK = $null -ne $Response -and $Response.StatusCode -eq $ExpectedStatus
    $code = if ($null -ne $Response.Json) { [string]$Response.Json.code } else { "" }
    $correlation = if ($null -ne $Response.Json) { [string]$Response.Json.correlation_id } else { "" }
    $headerCorrelation = if ($null -ne $Response.Headers) { [string]$Response.Headers["X-Correlation-ID"] } else { "" }
    Add-Check $Name $statusOK ("status={0}; code={1}; correlation_id={2}; response_header={3}" -f $Response.StatusCode, $code, $correlation, $headerCorrelation)
    if ($headerCorrelation) { Add-Evidence "${Name}: X-Correlation-ID=$headerCorrelation" }
    if ($correlation) { Add-Evidence "${Name}: body.correlation_id=$correlation" }
    return $statusOK
}

function Json-Value {
    param($Object, [string]$Property)
    if ($null -eq $Object) { return "" }
    return [string]$Object.$Property
}

function Get-DataRows {
    param($Json)
    if ($null -eq $Json) { return @() }
    if ($null -ne $Json.data -and $Json.data -is [array]) { return @($Json.data) }
    if ($null -ne $Json.items -and $Json.items -is [array]) { return @($Json.items) }
    return @()
}

function Get-Token {
    param([string]$Email)
    $correlation = "${runId}-login-$($Email.Split('@')[0])"
    $response = Invoke-Api -Method POST -Path "/api/v1/auth/login" -Headers (New-Headers -CorrelationId $correlation) -Body @{ email = $Email; password = $Password }
    $ok = Check-Response "Login $Email" $response 200
    $token = if ($null -ne $response.Json) { [string]$response.Json.access_token } else { "" }
    if (-not $token -and $null -ne $response.Json.data) { $token = [string]$response.Json.data.access_token }
    if (-not $token -and -not [string]::IsNullOrWhiteSpace($response.Content)) {
        try {
            $rawJson = $response.Content | ConvertFrom-Json
            $token = [string]$rawJson.access_token
            if (-not $token -and $null -ne $rawJson.data) { $token = [string]$rawJson.data.access_token }
        } catch { $token = "" }
    }
    if (-not $token -and -not [string]::IsNullOrWhiteSpace($response.Content)) {
        $tokenMatch = [regex]::Match([string]$response.Content, '"access_token"\s*:\s*"([^"]+)"')
        if ($tokenMatch.Success) { $token = $tokenMatch.Groups[1].Value }
    }
    $contentLength = ([string]$response.Content).Length
    Add-Check "Login token $Email" (-not [string]::IsNullOrWhiteSpace($token)) "access_token_present=$(-not [string]::IsNullOrWhiteSpace($token)); content_length=$contentLength"
    if (-not $ok -or [string]::IsNullOrWhiteSpace($token)) { throw "Login failed for $Email" }
    return $token
}

function Create-And-Submit {
    param([string]$Token, [int]$Index)
    $suffix = "$runId-$Index"
    $correlation = "${runId}-batch-$Index"
    $createKey = "${runId}-create-$Index"
    $body = @{
        category = "ICT_EQUIPMENT"
        quantity = 5
        estimated_weight_kg = 12.50
        condition_rating = "FUNCTIONAL"
        is_data_bearing = $false
        zone = "NORTH"
        collection_deadline = UtcStamp ((Get-Date).ToUniversalTime().AddHours(96))
        notes = "S2-X-V-J-01 remote verification $suffix"
    }
    $headers = New-Headers -Token $Token -CorrelationId $correlation -IdempotencyKey $createKey
    $created = Invoke-Api -Method POST -Path "/api/v1/batches" -Headers $headers -Body $body
    if (-not (Check-Response "Create batch $Index" $created 201)) { throw "Create failed for batch $Index" }
    $batchId = [string]$created.Json.data.batch_id
    $version = [int]$created.Json.data.version
    Add-Check "Create batch identity $Index" (-not [string]::IsNullOrWhiteSpace($batchId) -and $version -eq 1) "batch_id=$batchId; version=$version"
    Add-Evidence "batch[$Index]=$batchId; create_correlation=$correlation"

    if ($Index -eq 1) {
        $replay = Invoke-Api -Method POST -Path "/api/v1/batches" -Headers $headers -Body $body
        Check-Response "Create idempotency replay" $replay 201 | Out-Null
        $conflictBody = $body.Clone()
        $conflictBody.notes = "different request for same idempotency key"
        $conflict = Invoke-Api -Method POST -Path "/api/v1/batches" -Headers $headers -Body $conflictBody
        Check-Response "Create idempotency conflict" $conflict 409 | Out-Null
    }

    $editKey = "${runId}-edit-$Index"
    $editHeaders = New-Headers -Token $Token -CorrelationId "${runId}-edit-$Index" -IdempotencyKey $editKey -Version $version
    $edited = Invoke-Api -Method PATCH -Path "/api/v1/batches/$batchId" -Headers $editHeaders -Body @{ notes = "edited $suffix" }
    if (-not (Check-Response "Edit draft $Index" $edited 200)) { throw "Edit failed for batch $Index" }
    $version = [int]$edited.Json.data.version

    $submitKey = "${runId}-submit-$Index"
    $submitHeaders = New-Headers -Token $Token -CorrelationId "${runId}-submit-$Index" -IdempotencyKey $submitKey -Version $version
    $submitted = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/submit" -Headers $submitHeaders
    if (-not (Check-Response "Submit batch $Index" $submitted 200)) { throw "Submit failed for batch $Index" }
    $status = [string]$submitted.Json.data.status
    $eventState = [string]$submitted.Json.event_state
    Add-Check "Submit lifecycle $Index" ($status -eq "SUBMITTED" -and $eventState -eq "PENDING") "status=$status; event_state=$eventState; event_id=$($submitted.Json.event_id)"
    Add-Evidence "batch[$Index] submit event_id=$($submitted.Json.event_id); event_state=$eventState"

    $submitReplay = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/submit" -Headers $submitHeaders
    Check-Response "Submit idempotency replay $Index" $submitReplay 200 | Out-Null
    $postSubmitEdit = Invoke-Api -Method PATCH -Path "/api/v1/batches/$batchId" -Headers (New-Headers -Token $Token -CorrelationId "${runId}-stale-edit-$Index" -IdempotencyKey "${runId}-stale-edit-$Index" -Version 3) -Body @{ notes = "must be rejected" }
    Check-Response "Submitted batch rejects edit $Index" $postSubmitEdit 409 | Out-Null

    return [pscustomobject]@{ Id = $batchId; Version = 3; ClaimEpoch = "1"; SubmitResponse = $submitted }
}

function Find-Opportunity {
    param($Rows, [string]$BatchId)
    foreach ($row in @($Rows)) {
        if ([string]$row.batch_id -eq $BatchId) { return $row }
    }
    return $null
}

function Invoke-Claim {
    param([string]$Token, $Opportunity, [string]$Label)
    $batchId = [string]$Opportunity.batch_id
    $version = [int]$Opportunity.version
    $epoch = [string]$Opportunity.claim_epoch
    $correlation = "${runId}-claim-$Label"
    $headers = New-Headers -Token $Token -CorrelationId $correlation -IdempotencyKey "${runId}-claim-$Label" -Version $version
    $body = @{ expected_version = $version; claim_epoch = $epoch; notes = "S2-X-V-J-01 claim $Label" }
    $response = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/claim" -Headers $headers -Body $body
    if (-not (Check-Response "Claim $Label" $response 200)) { throw "Claim failed for $Label" }
    Add-Check "Claim state $Label" ([string]$response.Json.data.status -eq "APPROVED") "status=$($response.Json.data.status); version=$($response.Json.data.version); event_state=$($response.Json.data.event_state)"
    Add-Evidence "claim[$Label] batch_id=$batchId; correlation=$correlation; event_id=$($response.Json.data.event_id)"
    $replay = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/claim" -Headers $headers -Body $body
    Check-Response "Claim idempotency replay $Label" $replay 200 | Out-Null
    return [pscustomobject]@{ BatchId = $batchId; Version = [int]$response.Json.data.version; ClaimEpoch = [string]$response.Json.data.claim_epoch; Token = $Token; ClaimResponse = $response }
}

function Get-CollectorBatch {
    param([string]$CollectorToken, [string]$BatchId, [string]$Label)
    $response = Invoke-Api -Method GET -Path "/api/v1/batches?page=1&page_size=100" -Headers (New-Headers -Token $CollectorToken -CorrelationId "${runId}-collector-list-$Label")
    if (-not (Check-Response "Collector batch list $Label" $response 200)) { throw "Collector batch list failed" }
    $row = Find-Opportunity (Get-DataRows $response.Json) $BatchId
    if ($null -eq $row) {
        Add-Check "Collector sees claimed batch $Label" $false "batch_id=$BatchId"
        throw "Collector cannot see claimed batch $BatchId"
    }
    Add-Check "Collector sees claimed batch $Label" (-not [string]::IsNullOrWhiteSpace([string]$row.collector_scope_id)) "collector_scope_id=$($row.collector_scope_id); version=$($row.version)"
    return $row
}

function Select-Assignment {
    param([string]$CollectorToken, $BatchRow, [string]$Label)
    $batchId = [string]$BatchRow.batch_id
    $version = [int]$BatchRow.version
    $epoch = [string]$BatchRow.claim_epoch
    $scope = [string]$BatchRow.collector_scope_id
    $headers = New-Headers -Token $CollectorToken -CorrelationId "${runId}-select-$Label" -IdempotencyKey "${runId}-select-$Label" -Version $version
    $body = @{ expected_version = $version; claim_epoch = $epoch; collector_scope_id = $scope }
    $response = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/assignments" -Headers $headers -Body $body
    if (-not (Check-Response "Select assignment $Label" $response 201)) { throw "Assignment selection failed for $Label" }
    Add-Check "Assignment accepted $Label" ([string]$response.Json.data.assignment_status -eq "ACCEPTED") "assignment_id=$($response.Json.data.assignment_id); assignment_status=$($response.Json.data.assignment_status)"
    $replay = Invoke-Api -Method POST -Path "/api/v1/batches/$batchId/assignments" -Headers $headers -Body $body
    Check-Response "Assignment selection idempotency replay $Label" $replay 201 | Out-Null
    # Select increments the batch aggregate version from APPROVED to ASSIGNED.
    # The collector batch-read query intentionally hides ASSIGNED rows, so keep
    # the post-selection version for the next assignment mutation.
    return [pscustomobject]@{ AssignmentId = [string]$response.Json.data.assignment_id; BatchId = $batchId; Version = $version + 1; Token = $CollectorToken }
}

function Get-BatchVersion {
    param([string]$Token, [string]$BatchId, [string]$Label)
    $response = Invoke-Api -Method GET -Path "/api/v1/batches/$BatchId" -Headers (New-Headers -Token $Token -CorrelationId "${runId}-batch-read-$Label")
    if (-not (Check-Response "Read batch after assignment $Label" $response 200)) { throw "Batch read failed" }
    return $response.Json.data
}

function Invoke-RaceClaim {
    param([string]$BatchId, [int]$Version, [string]$Epoch, [string]$TokenA, [string]$TokenB)
    $jobScript = {
        param($ApiBase, $Batch, $ExpectedVersion, $ClaimEpoch, $Token, $Correlation, $Idempotency)
        $headers = @{ Authorization = "Bearer $Token"; "X-Correlation-ID" = $Correlation; "Idempotency-Key" = $Idempotency; "If-Match-Version" = [string]$ExpectedVersion }
        $body = @{ expected_version = $ExpectedVersion; claim_epoch = $ClaimEpoch; notes = "S2-X-V-J-01 race" } | ConvertTo-Json -Compress
        try {
            $response = Invoke-WebRequest -Method POST -Uri "$ApiBase/api/v1/batches/$Batch/claim" -Headers $headers -ContentType "application/json" -Body $body -UseBasicParsing -ErrorAction Stop
            return [pscustomobject]@{ StatusCode = [int]$response.StatusCode; Content = [string]$response.Content; Correlation = $Correlation }
        } catch {
            $errorResponse = $_.Exception.Response
            $content = ""
            if ($null -ne $errorResponse) {
                try {
                    $stream = $errorResponse.GetResponseStream()
                    if ($null -ne $stream) {
                        $reader = [IO.StreamReader]::new($stream)
                        try { $content = $reader.ReadToEnd() } finally { $reader.Dispose(); $stream.Dispose() }
                    }
                } catch { $content = "" }
            }
            return [pscustomobject]@{ StatusCode = [int]$errorResponse.StatusCode; Content = $content; Correlation = $Correlation }
        }
    }
    $jobA = Start-Job -ScriptBlock $jobScript -ArgumentList $base, $BatchId, $Version, $Epoch, $TokenA, "${runId}-race-a", "${runId}-race-a"
    $jobB = Start-Job -ScriptBlock $jobScript -ArgumentList $base, $BatchId, $Version, $Epoch, $TokenB, "${runId}-race-b", "${runId}-race-b"
    Wait-Job -Job $jobA, $jobB -Timeout 60 | Out-Null
    $result = @(Receive-Job -Job $jobA, $jobB)
    Remove-Job -Job $jobA, $jobB -Force
    return $result
}

try {
    $ready = Invoke-Api -Method GET -Path "/readyz"
    Check-Response "API readiness" $ready 200 | Out-Null
    $live = Invoke-Api -Method GET -Path "/healthz"
    Check-Response "API liveness" $live 200 | Out-Null

    $anonymous = Invoke-Api -Method GET -Path "/api/v1/batches" -Headers (New-Headers -CorrelationId "${runId}-anonymous")
    Check-Response "Anonymous request rejected" $anonymous 401 | Out-Null

    $donorToken = Get-Token "donor1@ewaste.test"
    $recyclerAToken = Get-Token "recycler1@ewaste.test"
    $recyclerBToken = Get-Token "recycler2@ewaste.test"
    $collectorToken = Get-Token "collector1@ewaste.test"

    $batches = @()
    foreach ($index in 1..4) { $batches += Create-And-Submit -Token $donorToken -Index $index }

    $forbiddenClaim = Invoke-Api -Method POST -Path "/api/v1/batches/$($batches[0].Id)/claim" -Headers (New-Headers -Token $donorToken -CorrelationId "${runId}-forbidden-claim" -IdempotencyKey "${runId}-forbidden-claim" -Version 3) -Body @{ expected_version = 3; claim_epoch = "1"; notes = "forbidden" }
    Check-Response "Donor claim forbidden" $forbiddenClaim 403 | Out-Null

    $opportunityA = $null
    $opportunityB = $null
    $opportunityRace = $null
    $opportunityReject = $null
    $deadline = (Get-Date).ToUniversalTime().AddSeconds($MatchTimeoutSeconds)
    while ((Get-Date).ToUniversalTime() -lt $deadline) {
        $listA = Invoke-Api -Method GET -Path "/api/v1/opportunities?page=1&page_size=100" -Headers (New-Headers -Token $recyclerAToken -CorrelationId "${runId}-opportunities-a")
        $listB = Invoke-Api -Method GET -Path "/api/v1/opportunities?page=1&page_size=100" -Headers (New-Headers -Token $recyclerBToken -CorrelationId "${runId}-opportunities-b")
        if ($listA.StatusCode -eq 200 -and $listB.StatusCode -eq 200) {
            $rowsA = Get-DataRows $listA.Json
            $rowsB = Get-DataRows $listB.Json
            $opportunityA = Find-Opportunity $rowsA $batches[0].Id
            $opportunityB = Find-Opportunity $rowsA $batches[1].Id
            $opportunityRace = Find-Opportunity $rowsA $batches[2].Id
            $opportunityReject = Find-Opportunity $rowsA $batches[3].Id
            if ($null -ne $opportunityA -and $null -ne $opportunityB -and $null -ne $opportunityRace -and $null -ne $opportunityReject) { break }
        }
        Start-Sleep -Seconds 3
    }
    Add-Check "Matcher produced opportunities" ($null -ne $opportunityA -and $null -ne $opportunityB -and $null -ne $opportunityRace -and $null -ne $opportunityReject) "batch_a=$($null -ne $opportunityA); batch_b=$($null -ne $opportunityB); batch_race=$($null -ne $opportunityRace); batch_reject=$($null -ne $opportunityReject)"
    if ($null -eq $opportunityA -or $null -eq $opportunityB -or $null -eq $opportunityRace -or $null -eq $opportunityReject) { throw "Matcher did not produce all expected opportunities" }

    $raceResults = Invoke-RaceClaim -BatchId $batches[2].Id -Version ([int]$opportunityRace.version) -Epoch ([string]$opportunityRace.claim_epoch) -TokenA $recyclerAToken -TokenB $recyclerBToken
    $raceStatuses = @($raceResults | ForEach-Object { $_.StatusCode })
    $racePass = (($raceStatuses | Where-Object { $_ -eq 200 }).Count -eq 1 -and ($raceStatuses | Where-Object { $_ -eq 409 }).Count -eq 1)
    Add-Check "Claim race has one winner" $racePass "statuses=$($raceStatuses -join ',')"

    $successClaim = Invoke-Claim -Token $recyclerAToken -Opportunity $opportunityA -Label "handoff"
    # Keep the failure lifecycle in the seeded PROC-001/NORTH collector scope.
    # recycler2 is reserved for the concurrent-claim race; its seeded scope is
    # EAST-only and must not be used for a NORTH collector workflow.
    $failureClaim = Invoke-Claim -Token $recyclerAToken -Opportunity $opportunityB -Label "failure"
    $rejectClaim = Invoke-Claim -Token $recyclerAToken -Opportunity $opportunityReject -Label "reject"

    $successRow = Get-CollectorBatch -CollectorToken $collectorToken -BatchId $successClaim.BatchId -Label "handoff"
    $failureRow = Get-CollectorBatch -CollectorToken $collectorToken -BatchId $failureClaim.BatchId -Label "failure"
    $rejectRow = Get-CollectorBatch -CollectorToken $collectorToken -BatchId $rejectClaim.BatchId -Label "reject"

    $successAssignment = Select-Assignment -CollectorToken $collectorToken -BatchRow $successRow -Label "handoff"
    $failureAssignment = Select-Assignment -CollectorToken $collectorToken -BatchRow $failureRow -Label "failure"
    $rejectAssignment = Select-Assignment -CollectorToken $collectorToken -BatchRow $rejectRow -Label "reject"

    $rejectVersion = [int]$rejectAssignment.Version
    $rejectHeaders = New-Headers -Token $collectorToken -CorrelationId "${runId}-reject" -IdempotencyKey "${runId}-reject" -Version $rejectVersion
    $rejectBody = @{ rejection_reason = "route_unavailable" }
    $rejectResponse = Invoke-Api -Method POST -Path "/api/v1/assignments/$($rejectAssignment.AssignmentId)/reject" -Headers $rejectHeaders -Body $rejectBody
    Check-Response "Collector rejects assignment" $rejectResponse 200 | Out-Null
    $rejectReplay = Invoke-Api -Method POST -Path "/api/v1/assignments/$($rejectAssignment.AssignmentId)/reject" -Headers $rejectHeaders -Body $rejectBody
    Check-Response "Assignment rejection idempotency replay" $rejectReplay 200 | Out-Null

    $successVersion = [int]$successAssignment.Version
    $handoffHeaders = New-Headers -Token $collectorToken -CorrelationId "${runId}-handoff" -IdempotencyKey "${runId}-handoff" -Version $successVersion
    $hash = ([Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($runId)) | ForEach-Object { $_.ToString("x2") }) -join ""
    # The API requires pickup_occurred_at >= assignment.assigned_at. Allow the
    # assignment timestamp to settle, then send a slightly-past timestamp.
    Start-Sleep -Seconds 2
    $handoffBody = @{ pickup_occurred_at = UtcStamp ((Get-Date).ToUniversalTime().AddSeconds(-1)); donor_representative_name = "Remote test representative"; actual_item_count = 5; verification_hash = $hash; notes = "S2-X-V-J-01 successful handoff" }
    $handoffResponse = Invoke-Api -Method POST -Path "/api/v1/assignments/$($successAssignment.AssignmentId)/handoff" -Headers $handoffHeaders -Body $handoffBody
    Check-Response "Collector records handoff" $handoffResponse 200 | Out-Null
    $handoffReplay = Invoke-Api -Method POST -Path "/api/v1/assignments/$($successAssignment.AssignmentId)/handoff" -Headers $handoffHeaders -Body $handoffBody
    Check-Response "Handoff idempotency replay" $handoffReplay 200 | Out-Null

    $failureVersion = [int]$failureAssignment.Version
    $failureHeaders = New-Headers -Token $collectorToken -CorrelationId "${runId}-failure" -IdempotencyKey "${runId}-failure" -Version $failureVersion
    $failureBody = @{ failure_reason = "DONOR_UNAVAILABLE"; observed_details = "S2-X-V-J-01 failure path" }
    $failureResponse = Invoke-Api -Method POST -Path "/api/v1/assignments/$($failureAssignment.AssignmentId)/fail" -Headers $failureHeaders -Body $failureBody
    Check-Response "Collector records failed pickup" $failureResponse 200 | Out-Null
    $failureReplay = Invoke-Api -Method POST -Path "/api/v1/assignments/$($failureAssignment.AssignmentId)/fail" -Headers $failureHeaders -Body $failureBody
    Check-Response "Failure idempotency replay" $failureReplay 200 | Out-Null

    $finalSuccess = Invoke-Api -Method GET -Path "/api/v1/batches/$($successAssignment.BatchId)" -Headers (New-Headers -Token $donorToken -CorrelationId "${runId}-final-success")
    $finalFailure = Invoke-Api -Method GET -Path "/api/v1/batches/$($failureAssignment.BatchId)" -Headers (New-Headers -Token $donorToken -CorrelationId "${runId}-final-failure")
    Check-Response "Final collected lifecycle" $finalSuccess 200 | Out-Null
    Check-Response "Final failed lifecycle" $finalFailure 200 | Out-Null
    Add-Check "Final lifecycle states" ([string]$finalSuccess.Json.data.status -eq "COLLECTED" -and [string]$finalFailure.Json.data.status -eq "FAILED_COLLECTION") "collected=$($finalSuccess.Json.data.status); failed=$($finalFailure.Json.data.status)"

    $failedChecks = @($checks | Where-Object { -not $_.Passed })
    $allPassed = $failedChecks.Count -eq 0
    if (-not $allPassed) { throw "Verification failed: $($failedChecks.Count) check(s) failed" }

    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ReportPath) | Out-Null
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add("# S2-X-V-J-01 Remote Cross-Story Verification")
    [void]$lines.Add("")
    [void]$lines.Add("- Run ID: $runId")
    [void]$lines.Add("- API: $base")
    [void]$lines.Add("- Result: PASS")
    [void]$lines.Add("- Note: direct production MySQL/Kafka query access was not used; API lifecycle and matcher evidence are retained below.")
    [void]$lines.Add("")
    [void]$lines.Add("## Checks")
    [void]$lines.Add("")
    [void]$lines.Add("| Check | Result | Details |")
    [void]$lines.Add("|---|---|---|")
    foreach ($check in $checks) { [void]$lines.Add("| $($check.Name) | PASS | $($check.Details.Replace('|','\\|')) |") }
    [void]$lines.Add("")
    [void]$lines.Add("## Evidence")
    [void]$lines.Add("")
    foreach ($item in $evidence) { [void]$lines.Add("- $item") }
    Set-Content -LiteralPath $ReportPath -Value $lines -Encoding UTF8
    $reportWritten = $true
    Write-Host "Report written to $ReportPath"
} catch {
    Write-Error $_
    Write-Host "No report was generated because the verification did not pass."
    exit 1
} finally {
    if (-not $reportWritten -and (Test-Path -LiteralPath $ReportPath)) {
        Remove-Item -LiteralPath $ReportPath -Force
    }
}
