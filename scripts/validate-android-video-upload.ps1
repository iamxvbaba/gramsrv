<#
.SYNOPSIS
Semi-automates Android video upload validation against local telesrv.

.DESCRIPTION
The script handles the stable parts of the Android upload regression loop:
preflight checks, optional video fixture generation/push, baseline snapshots,
server log scanning, PostgreSQL media/message assertions, upload_parts cleanup,
and local blob existence checks.

It intentionally does not drive the Android media picker. Send the prepared
video manually from Android/Alice, then run -Phase AfterSend.
#>
[CmdletBinding()]
param(
    [ValidateSet("Preflight", "Prepare", "BeforeSend", "AfterSend", "All")]
    [string]$Phase = "Preflight",

    [long]$SenderUserId = 1780269504,
    [long]$RecipientUserId = 1780269505,

    [string]$AndroidPackage = "org.telegram.messenger.beta",
    [string]$DeviceSerial,

    [string]$PostgresContainer = "telesrv-postgres",
    [string]$Database = "telesrv",
    [string]$DbUser = "telesrv",

    [string]$ServerLogPath,
    [string]$StatePath,
    [string]$FixtureDir,
    [string]$VideoFixture,
    [string]$BlobDir,
    [string]$RemoteMovieDir = "/sdcard/Movies/telesrv",

    [switch]$SkipAdb,
    [switch]$AllowMissingThumb
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
if (-not $ServerLogPath) {
    $latestLog = Get-ChildItem (Join-Path $RepoRoot "logs") -Filter "telesrv-*.err.log" -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if ($latestLog) {
        $ServerLogPath = $latestLog.FullName
    } else {
        $ServerLogPath = Join-Path $RepoRoot "logs\telesrv.err.log"
    }
}
if (-not $StatePath) {
    $StatePath = Join-Path $RepoRoot "logs\android-video-upload-state.json"
}
if (-not $FixtureDir) {
    $FixtureDir = Join-Path $RepoRoot "logs\media-fixtures"
}
if (-not $BlobDir) {
    $BlobDir = Join-Path $RepoRoot "data\blobs"
}

$Failures = New-Object System.Collections.Generic.List[string]

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "== $Message =="
}

function Write-Ok {
    param([string]$Message)
    Write-Host "[ok] $Message"
}

function Write-Warn {
    param([string]$Message)
    Write-Host "[warn] $Message"
}

function Add-Failure {
    param([string]$Message)
    $script:Failures.Add($Message) | Out-Null
    Write-Host "[fail] $Message"
}

function Assert-Check {
    param(
        [bool]$Condition,
        [string]$Message
    )
    if ($Condition) {
        Write-Ok $Message
    } else {
        Add-Failure $Message
    }
}

function Invoke-External {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [switch]$AllowFailure
    )
    $oldErrorActionPreference = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & $FilePath @Arguments 2>&1
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $oldErrorActionPreference
    }
    $text = ($output | ForEach-Object { $_.ToString() }) -join "`n"
    if ($exitCode -ne 0 -and -not $AllowFailure) {
        throw "$FilePath $($Arguments -join ' ') failed with exit code ${exitCode}:`n$text"
    }
    [pscustomobject]@{
        ExitCode = $exitCode
        Output = $text
    }
}

function Assert-Command {
    param([string]$Name)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    Assert-Check ([bool]$cmd) "$Name is available"
    return [bool]$cmd
}

function Get-AdbArgs {
    param([string[]]$Arguments)
    if ($DeviceSerial) {
        return @("-s", $DeviceSerial) + $Arguments
    }
    return $Arguments
}

function Invoke-Adb {
    param([string[]]$Arguments, [switch]$AllowFailure)
    Invoke-External "adb" (Get-AdbArgs $Arguments) -AllowFailure:$AllowFailure
}

function Invoke-PsqlRows {
    param([string]$Sql)
    $args = @(
        "exec", $PostgresContainer,
        "psql", "-U", $DbUser, "-d", $Database,
        "-v", "ON_ERROR_STOP=1",
        "-At", "-F", "|",
        "-c", $Sql
    )
    $result = Invoke-External "docker" $args
    if ([string]::IsNullOrWhiteSpace($result.Output)) {
        return @()
    }
    return @($result.Output -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
}

function Get-LogLineCount {
    if (-not (Test-Path -LiteralPath $ServerLogPath)) {
        return 0
    }
    return @(Read-SharedLogLines).Count
}

function Get-LogLinesSince {
    param([int]$Skip)
    if (-not (Test-Path -LiteralPath $ServerLogPath)) {
        Add-Failure "server log exists at $ServerLogPath"
        return @()
    }
    return @(Read-SharedLogLines | Select-Object -Skip $Skip)
}

function Read-SharedLogLines {
    $stream = [System.IO.File]::Open($ServerLogPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try {
            $lines = New-Object System.Collections.Generic.List[string]
            while (-not $reader.EndOfStream) {
                $lines.Add($reader.ReadLine()) | Out-Null
            }
            return $lines
        } finally {
            $reader.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function Get-PrivateMessageMaxId {
    $rows = @(Invoke-PsqlRows @"
SELECT COALESCE(MAX(id), 0)
FROM private_messages
WHERE (sender_user_id = $SenderUserId AND recipient_user_id = $RecipientUserId)
   OR (sender_user_id = $RecipientUserId AND recipient_user_id = $SenderUserId);
"@)
    if ($rows.Count -eq 0) {
        return 0
    }
    return [long]$rows[0]
}

function Get-UploadPartUsage {
    $rows = @(Invoke-PsqlRows @"
SELECT COUNT(*), COALESCE(SUM(size), 0)
FROM upload_parts
WHERE owner_user_id = $SenderUserId;
"@)
    if ($rows.Count -eq 0) {
        return [pscustomobject]@{ Parts = 0; Bytes = 0L }
    }
    $parts = $rows[0] -split "\|"
    return [pscustomobject]@{
        Parts = [int]$parts[0]
        Bytes = [long]$parts[1]
    }
}

function Get-NewVideoMessages {
    param([long]$AfterMessageId)
    $rows = @(Invoke-PsqlRows @"
SELECT
  id,
  sender_user_id,
  recipient_user_id,
  COALESCE(media->>'kind', ''),
  COALESCE(media->>'video', 'false'),
  COALESCE(media->'document'->>'id', '0'),
  COALESCE(media->'document'->>'mime_type', ''),
  COALESCE(media->'document'->>'size', '0'),
  COALESCE(jsonb_array_length(COALESCE(media->'document'->'thumbs', '[]'::jsonb)), 0)
FROM private_messages
WHERE id > $AfterMessageId
  AND sender_user_id = $SenderUserId
  AND recipient_user_id = $RecipientUserId
ORDER BY id;
"@)
    $items = @()
    foreach ($row in $rows) {
        $parts = $row -split "\|", 9
        $items += [pscustomobject]@{
            MessageId = [long]$parts[0]
            SenderUserId = [long]$parts[1]
            RecipientUserId = [long]$parts[2]
            Kind = $parts[3]
            Video = $parts[4]
            DocumentId = [long]$parts[5]
            MimeType = $parts[6]
            Size = [long]$parts[7]
            ThumbCount = [int]$parts[8]
        }
    }
    return $items
}

function Get-DocumentRows {
    param([long[]]$DocumentIds)
    if ($DocumentIds.Count -eq 0) {
        return @()
    }
    $ids = ($DocumentIds | ForEach-Object { $_.ToString() }) -join ","
    $rows = @(Invoke-PsqlRows @"
SELECT id, mime_type, size, jsonb_array_length(COALESCE(thumbs, '[]'::jsonb))
FROM documents
WHERE id IN ($ids)
ORDER BY id;
"@)
    $items = @()
    foreach ($row in $rows) {
        $parts = $row -split "\|", 4
        $items += [pscustomobject]@{
            DocumentId = [long]$parts[0]
            MimeType = $parts[1]
            Size = [long]$parts[2]
            ThumbCount = [int]$parts[3]
        }
    }
    return $items
}

function Get-FileBlobRows {
    param([long[]]$DocumentIds)
    if ($DocumentIds.Count -eq 0) {
        return @()
    }
    $keys = @()
    foreach ($docId in $DocumentIds) {
        $keys += "doc:$docId"
        $keys += "doc:${docId}:m"
    }
    $quoted = ($keys | ForEach-Object { "'" + $_.Replace("'", "''") + "'" }) -join ","
    $rows = @(Invoke-PsqlRows @"
SELECT location_key, backend, object_key, size, mime_type
FROM file_blobs
WHERE location_key IN ($quoted)
ORDER BY location_key;
"@)
    $items = @()
    foreach ($row in $rows) {
        $parts = $row -split "\|", 5
        $items += [pscustomobject]@{
            LocationKey = $parts[0]
            Backend = $parts[1]
            ObjectKey = $parts[2]
            Size = [long]$parts[3]
            MimeType = $parts[4]
        }
    }
    return $items
}

function Wait-FileBlobRows {
    param(
        [long[]]$DocumentIds,
        [int]$TimeoutSeconds = 10
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = @()
    while ($true) {
        $last = @(Get-FileBlobRows $DocumentIds)
        $hasBody = @($last | Where-Object { $_.LocationKey -like "doc:*" -and $_.LocationKey -notlike "*:m" -and $_.Size -gt 0 }).Count -ge 1
        $hasThumb = $AllowMissingThumb -or (@($last | Where-Object { $_.LocationKey -like "doc:*:m" -and $_.Size -gt 0 }).Count -ge 1)
        if ($hasBody -and $hasThumb) {
            return $last
        }
        if ((Get-Date) -ge $deadline) {
            return $last
        }
        Start-Sleep -Milliseconds 500
    }
}

function Get-EffectiveLogSkip {
    param([pscustomobject]$State)
    $stateLog = ""
    if ($State.PSObject.Properties.Name -contains "ServerLogPath") {
        $stateLog = [string]$State.ServerLogPath
    }
    if ($stateLog) {
        $stateFull = [System.IO.Path]::GetFullPath($stateLog)
        $currentFull = [System.IO.Path]::GetFullPath($ServerLogPath)
        if ($stateFull.Equals($currentFull, [System.StringComparison]::OrdinalIgnoreCase)) {
            return [int]$State.BaselineLogLineCount
        }
        Write-Warn "server log changed since baseline; scanning current log from the beginning"
        return 0
    }
    return [int]$State.BaselineLogLineCount
}

function Get-BlobFilePath {
    param([string]$ObjectKey)
    if ($ObjectKey.Length -lt 4) {
        return Join-Path $BlobDir $ObjectKey
    }
    return Join-Path (Join-Path (Join-Path $BlobDir $ObjectKey.Substring(0, 2)) $ObjectKey.Substring(2, 2)) $ObjectKey
}

function Save-State {
    param([pscustomobject]$State)
    $dir = Split-Path -Parent $StatePath
    if ($dir) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    $State | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $StatePath -Encoding UTF8
    Write-Ok "state saved to $StatePath"
}

function Load-State {
    if (-not (Test-Path -LiteralPath $StatePath)) {
        throw "state file not found: $StatePath. Run -Phase BeforeSend first."
    }
    Get-Content -LiteralPath $StatePath -Raw | ConvertFrom-Json
}

function New-VideoFixture {
    param([string]$Path)
    $ffmpeg = Get-Command ffmpeg -ErrorAction SilentlyContinue
    if (-not $ffmpeg) {
        Add-Failure "ffmpeg is available or -VideoFixture is supplied"
        return
    }
    Invoke-External "ffmpeg" @(
        "-y",
        "-f", "lavfi",
        "-i", "testsrc=size=568x1280:rate=30",
        "-f", "lavfi",
        "-i", "sine=frequency=880:sample_rate=44100",
        "-t", "3",
        "-pix_fmt", "yuv420p",
        "-c:v", "libx264",
        "-c:a", "aac",
        "-shortest",
        $Path
    ) | Out-Null
}

function Ensure-VideoFixture {
    New-Item -ItemType Directory -Force -Path $FixtureDir | Out-Null
    if (-not $VideoFixture) {
        $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
        $script:VideoFixture = Join-Path $FixtureDir "telesrv-android-video-upload-$stamp.mp4"
        New-VideoFixture $script:VideoFixture
    }
    Assert-Check ($VideoFixture -and (Test-Path -LiteralPath $VideoFixture)) "video fixture exists: $VideoFixture"
}

function Push-VideoFixture {
    if ($SkipAdb) {
        Write-Warn "adb push skipped"
        return
    }
    Invoke-Adb @("shell", "mkdir", "-p", $RemoteMovieDir) | Out-Null
    Invoke-Adb @("push", $VideoFixture, "$RemoteMovieDir/") | Out-Null
    $leaf = Split-Path -Leaf $VideoFixture
    Invoke-Adb @("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE", "-d", "file://$RemoteMovieDir/$leaf") | Out-Null
    Write-Ok "video pushed to Android: $RemoteMovieDir/$leaf"
}

function Run-Preflight {
    Write-Step "Preflight"
    if (-not $SkipAdb) {
        if (Assert-Command "adb") {
            $devices = Invoke-Adb @("devices")
            $deviceLines = @($devices.Output -split "`r?`n" | Where-Object { $_ -match "\tdevice$" })
            Assert-Check ($deviceLines.Count -ge 1) "adb has at least one connected device"
            Assert-Check (($deviceLines.Count -eq 1) -or [bool]$DeviceSerial) "adb selects a single device or -DeviceSerial is set"
            if (($deviceLines.Count -eq 1) -or [bool]$DeviceSerial) {
                $pkg = Invoke-Adb @("shell", "dumpsys", "package", $AndroidPackage)
                Assert-Check ($pkg.Output -match "versionName=") "Android package $AndroidPackage is installed"
                $model = (Invoke-Adb @("shell", "getprop", "ro.product.model")).Output.Trim()
                $sdk = (Invoke-Adb @("shell", "getprop", "ro.build.version.sdk")).Output.Trim()
                Write-Host "Android device: model=$model sdk=$sdk"
            }
        }
    } else {
        Write-Warn "adb checks skipped"
    }
    if (Assert-Command "docker") {
        Invoke-PsqlRows "SELECT 1;" | Out-Null
        Write-Ok "PostgreSQL is reachable through docker container $PostgresContainer"
    }
    Assert-Check (Test-Path -LiteralPath $ServerLogPath) "server log exists: $ServerLogPath"
    Write-Host "server log: $ServerLogPath"
}

function Run-Prepare {
    Write-Step "Prepare video fixture"
    Run-Preflight
    Ensure-VideoFixture
    Push-VideoFixture
    Write-Host "Manual step: send this video from Android/Alice to Bob:"
    Write-Host "  $VideoFixture"
}

function Run-BeforeSend {
    Write-Step "BeforeSend snapshot"
    $maxMessageId = Get-PrivateMessageMaxId
    $usage = Get-UploadPartUsage
    $lineCount = Get-LogLineCount
    Write-Host "private_messages max id before send: $maxMessageId"
    Write-Host "upload_parts before send: parts=$($usage.Parts) bytes=$($usage.Bytes)"
    $state = [pscustomobject]@{
        SenderUserId = $SenderUserId
        RecipientUserId = $RecipientUserId
        BaselinePrivateMessageId = $maxMessageId
        BaselineUploadParts = $usage.Parts
        BaselineUploadPartBytes = $usage.Bytes
        BaselineLogLineCount = $lineCount
        ServerLogPath = $ServerLogPath
        BlobDir = $BlobDir
        CreatedAt = (Get-Date -Format o)
        VideoFixture = $VideoFixture
    }
    Save-State $state
}

function Run-AfterSend {
    Write-Step "AfterSend assertions"
    $state = Load-State
    $messages = @(Get-NewVideoMessages ([long]$state.BaselinePrivateMessageId))
    foreach ($message in $messages) {
        Write-Host ("new private message id={0} kind={1} video={2} doc={3} mime={4} size={5} thumbs={6}" -f $message.MessageId, $message.Kind, $message.Video, $message.DocumentId, $message.MimeType, $message.Size, $message.ThumbCount)
    }
    $videos = @($messages | Where-Object {
        $_.Kind -eq "document" -and $_.Video -eq "true" -and $_.MimeType -eq "video/mp4" -and $_.DocumentId -gt 0
    })
    Assert-Check ($videos.Count -ge 1) "new private message includes uploaded video/mp4 document"

    $docIds = @($videos | Select-Object -ExpandProperty DocumentId -Unique)
    $documents = @(Get-DocumentRows $docIds)
    $blobs = @(Wait-FileBlobRows $docIds)
    foreach ($doc in $documents) {
        Write-Host ("document id={0} mime={1} size={2} thumbs={3}" -f $doc.DocumentId, $doc.MimeType, $doc.Size, $doc.ThumbCount)
    }
    foreach ($blob in $blobs) {
        Write-Host ("blob key={0} backend={1} object={2} size={3} mime={4}" -f $blob.LocationKey, $blob.Backend, $blob.ObjectKey, $blob.Size, $blob.MimeType)
    }

    Assert-Check ($documents.Count -ge $docIds.Count) "documents rows exist for uploaded video"
    Assert-Check (@($documents | Where-Object { $_.MimeType -eq "video/mp4" -and $_.Size -gt 0 }).Count -ge 1) "uploaded video document metadata is persisted"
    if (-not $AllowMissingThumb) {
        Assert-Check (@($documents | Where-Object { $_.ThumbCount -gt 0 }).Count -ge 1) "uploaded video document has thumbnail metadata"
    }
    Assert-Check (@($blobs | Where-Object { $_.LocationKey -like "doc:*" -and $_.LocationKey -notlike "*:m" -and $_.MimeType -eq "video/mp4" -and $_.Size -gt 0 }).Count -ge 1) "video body file_blobs row exists"
    if (-not $AllowMissingThumb) {
        Assert-Check (@($blobs | Where-Object { $_.LocationKey -like "doc:*:m" -and $_.Size -gt 0 }).Count -ge 1) "video thumbnail file_blobs row exists"
    }
    foreach ($blob in $blobs) {
        if ($blob.Backend -eq "localfs" -and $blob.ObjectKey) {
            $path = Get-BlobFilePath $blob.ObjectKey
            Assert-Check (Test-Path -LiteralPath $path) "localfs blob exists: $($blob.LocationKey)"
        }
    }

    $usage = Get-UploadPartUsage
    Write-Host "upload_parts after send: parts=$($usage.Parts) bytes=$($usage.Bytes)"
    if ([int]$state.BaselineUploadParts -eq 0) {
        Assert-Check ($usage.Parts -eq 0) "upload_parts cleaned after successful upload"
    } else {
        Assert-Check ($usage.Parts -le [int]$state.BaselineUploadParts) "upload_parts did not grow after successful upload"
    }

    $lines = @(Get-LogLinesSince (Get-EffectiveLogSkip $state))
    $savePartHits = @($lines | Where-Object {
        ($_ -like "*upload.saveFilePart*" -or $_ -like "*upload.saveBigFilePart*") -and $_ -like '*client_type": "android"*'
    })
    $sendMediaHits = @($lines | Where-Object {
        $_ -like "*messages.sendMedia*" -and $_ -like '*client_type": "android"*'
    })
    $bad = @($lines | Where-Object {
        $_ -cmatch "INTERNAL_SERVER_ERROR|rpc error|Unhandled RPC|NOT_IMPLEMENTED|bad_msg|panic|\tERROR\t"
    })
    Assert-Check ($savePartHits.Count -ge 1) "server log has Android upload.saveFilePart/saveBigFilePart"
    Assert-Check ($sendMediaHits.Count -ge 1) "server log has Android messages.sendMedia"
    Assert-Check ($bad.Count -eq 0) "server log has no upload-era internal errors or unhandled RPCs"

    if (-not $SkipAdb) {
        $logcat = Invoke-Adb @("logcat", "-d", "-t", "1200") -AllowFailure
        if ($logcat.ExitCode -eq 0) {
            $androidErrors = @($logcat.Output -split "`r?`n" | Where-Object {
                $_ -match "INTERNAL_SERVER_ERROR|rpc error 500|saveFilePart|saveBigFilePart|FileUploadOperation"
            })
            if ($androidErrors.Count -gt 0) {
                Write-Host "Recent Android upload log lines:"
                $androidErrors | Select-Object -Last 40 | ForEach-Object { Write-Host $_ }
            }
            $fatalAndroidErrors = @($androidErrors | Where-Object { $_ -match "INTERNAL_SERVER_ERROR|rpc error 500" })
            Assert-Check ($fatalAndroidErrors.Count -eq 0) "recent Android logcat has no upload 500"
        } else {
            Write-Warn "adb logcat scan failed: $($logcat.Output)"
        }
    }
}

function Finish-Run {
    if ($Failures.Count -gt 0) {
        Write-Host ""
        Write-Host "Validation failed:"
        foreach ($failure in $Failures) {
            Write-Host " - $failure"
        }
        exit 1
    }
    Write-Host ""
    Write-Host "Validation passed."
}

switch ($Phase) {
    "Preflight" { Run-Preflight }
    "Prepare" { Run-Prepare }
    "BeforeSend" { Run-BeforeSend }
    "AfterSend" { Run-AfterSend }
    "All" {
        Run-Prepare
        Run-BeforeSend
        Read-Host "Send the prepared video from Android/Alice to Bob, then press Enter"
        Run-AfterSend
    }
}

Finish-Run
