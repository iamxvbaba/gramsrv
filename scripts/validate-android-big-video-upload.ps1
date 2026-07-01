<#
.SYNOPSIS
Validates Android big video upload and interruption recovery.

.DESCRIPTION
This helper covers the stable parts of the big-upload loop:
fixture generation/push, baseline snapshots, optional server restart when
upload.saveBigFilePart appears, post-send media assertions, and upload temp
cleanup checks. It intentionally does not automate Android media picker taps.
#>
[CmdletBinding()]
param(
    [ValidateSet("Preflight", "Prepare", "BeforeSend", "WatchRestart", "AfterSend", "All")]
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
    [int64]$MinBigFileBytes = 12MB,
    [int]$RestartAfterParts = 2,
    [int]$WatchTimeoutSeconds = 90,
    [string]$RestartScript,

    [switch]$SkipAdb,
    [switch]$AllowMissingThumb,
    [switch]$BuildOnRestart
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
    $StatePath = Join-Path $RepoRoot "logs\android-big-video-upload-state.json"
}
if (-not $FixtureDir) {
    $FixtureDir = Join-Path $RepoRoot "logs\media-fixtures"
}
if (-not $BlobDir) {
    $BlobDir = Join-Path $RepoRoot "data\blobs"
}
if (-not $RestartScript) {
    $RestartScript = Join-Path $RepoRoot "scripts\restart-local-server.ps1"
}

$Failures = New-Object System.Collections.Generic.List[string]

function Write-Step([string]$Message) {
    Write-Host ""
    Write-Host "== $Message =="
}

function Write-Ok([string]$Message) {
    Write-Host "[ok] $Message"
}

function Write-Warn([string]$Message) {
    Write-Host "[warn] $Message"
}

function Add-Failure([string]$Message) {
    $script:Failures.Add($Message) | Out-Null
    Write-Host "[fail] $Message"
}

function Assert-Check([bool]$Condition, [string]$Message) {
    if ($Condition) { Write-Ok $Message } else { Add-Failure $Message }
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
    [pscustomobject]@{ ExitCode = $exitCode; Output = $text }
}

function Invoke-PsqlRows([string]$Sql) {
    $result = Invoke-External "docker" @(
        "exec", $PostgresContainer,
        "psql", "-U", $DbUser, "-d", $Database,
        "-v", "ON_ERROR_STOP=1",
        "-At", "-F", "|",
        "-c", $Sql
    )
    if ([string]::IsNullOrWhiteSpace($result.Output)) {
        return @()
    }
    return @($result.Output -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
}

function Read-SharedLogLines([string]$Path = $ServerLogPath) {
    if (-not (Test-Path -LiteralPath $Path)) {
        return @()
    }
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
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

function Get-LogLineCount {
    return @(Read-SharedLogLines).Count
}

function Get-LogLinesSince([int]$Skip, [string]$Path = $ServerLogPath) {
    return @(Read-SharedLogLines $Path | Select-Object -Skip $Skip)
}

function Get-AdbArgs([string[]]$Arguments) {
    if ($DeviceSerial) { return @("-s", $DeviceSerial) + $Arguments }
    return $Arguments
}

function Invoke-Adb([string[]]$Arguments, [switch]$AllowFailure) {
    Invoke-External "adb" (Get-AdbArgs $Arguments) -AllowFailure:$AllowFailure
}

function Save-State([pscustomobject]$State) {
    $dir = Split-Path -Parent $StatePath
    if ($dir) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
    $State | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $StatePath -Encoding UTF8
    Write-Ok "state saved to $StatePath"
}

function Load-State {
    if (-not (Test-Path -LiteralPath $StatePath)) {
        throw "state file not found: $StatePath. Run -Phase BeforeSend first."
    }
    Get-Content -LiteralPath $StatePath -Raw | ConvertFrom-Json
}

function Get-PrivateMessageMaxId {
    $rows = @(Invoke-PsqlRows @"
SELECT COALESCE(MAX(id), 0)
FROM private_messages
WHERE (sender_user_id = $SenderUserId AND recipient_user_id = $RecipientUserId)
   OR (sender_user_id = $RecipientUserId AND recipient_user_id = $SenderUserId);
"@)
    if ($rows.Count -eq 0) { return 0 }
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
    return [pscustomobject]@{ Parts = [int]$parts[0]; Bytes = [long]$parts[1] }
}

function Get-UploadTempStats {
    $root = Join-Path (Join-Path $BlobDir "upload_parts") ([string]$SenderUserId)
    if (-not (Test-Path -LiteralPath $root)) {
        return [pscustomobject]@{ Files = 0; Bytes = 0L }
    }
    $files = @(Get-ChildItem -LiteralPath $root -Recurse -File -ErrorAction SilentlyContinue)
    $bytes = 0L
    foreach ($file in $files) { $bytes += [long]$file.Length }
    return [pscustomobject]@{ Files = $files.Count; Bytes = $bytes }
}

function Get-NewVideoMessages([long]$AfterMessageId) {
    $rows = @(Invoke-PsqlRows @"
SELECT
  id,
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
        $parts = $row -split "\|", 7
        $items += [pscustomobject]@{
            MessageId = [long]$parts[0]
            Kind = $parts[1]
            Video = $parts[2]
            DocumentId = [long]$parts[3]
            MimeType = $parts[4]
            Size = [long]$parts[5]
            ThumbCount = [int]$parts[6]
        }
    }
    return $items
}

function Get-DocumentRows([long[]]$DocumentIds) {
    if ($DocumentIds.Count -eq 0) { return @() }
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

function Get-FileBlobRows([long[]]$DocumentIds) {
    if ($DocumentIds.Count -eq 0) { return @() }
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

function Get-BlobFilePath([string]$ObjectKey) {
    if ($ObjectKey.Length -lt 4) {
        return Join-Path $BlobDir $ObjectKey
    }
    return Join-Path (Join-Path (Join-Path $BlobDir $ObjectKey.Substring(0, 2)) $ObjectKey.Substring(2, 2)) $ObjectKey
}

function New-BigVideoFixture([string]$Path) {
    $ffmpeg = Get-Command ffmpeg -ErrorAction SilentlyContinue
    if (-not $ffmpeg) {
        Add-Failure "ffmpeg is available or -VideoFixture points at an existing >10MB mp4"
        return
    }
    Invoke-External "ffmpeg" @(
        "-y",
        "-f", "lavfi",
        "-i", "testsrc2=size=1280x720:rate=30",
        "-f", "lavfi",
        "-i", "sine=frequency=660:sample_rate=44100",
        "-t", "16",
        "-pix_fmt", "yuv420p",
        "-c:v", "libx264",
        "-preset", "ultrafast",
        "-b:v", "8M",
        "-maxrate", "8M",
        "-bufsize", "16M",
        "-c:a", "aac",
        "-shortest",
        $Path
    ) | Out-Null
}

function Ensure-BigVideoFixture {
    New-Item -ItemType Directory -Force -Path $FixtureDir | Out-Null
    if (-not $VideoFixture) {
        $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
        $script:VideoFixture = Join-Path $FixtureDir "telesrv-android-big-video-$stamp.mp4"
        New-BigVideoFixture $script:VideoFixture
    }
    Assert-Check ($VideoFixture -and (Test-Path -LiteralPath $VideoFixture)) "video fixture exists: $VideoFixture"
    if ($VideoFixture -and (Test-Path -LiteralPath $VideoFixture)) {
        $size = (Get-Item -LiteralPath $VideoFixture).Length
        Write-Host "fixture_size=$size"
        Assert-Check ($size -ge $MinBigFileBytes) "fixture is large enough to trigger upload.saveBigFilePart"
    }
}

function Push-BigVideoFixture {
    if ($SkipAdb) {
        Write-Warn "adb push skipped"
        return
    }
    Invoke-Adb @("shell", "mkdir", "-p", $RemoteMovieDir) | Out-Null
    Invoke-Adb @("push", $VideoFixture, "$RemoteMovieDir/") | Out-Null
    $leaf = Split-Path -Leaf $VideoFixture
    Invoke-Adb @("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE", "-d", "file://$RemoteMovieDir/$leaf") | Out-Null
    Write-Ok "big video pushed to Android: $RemoteMovieDir/$leaf"
}

function Run-Preflight {
    Write-Step "Preflight"
    Assert-Check (Test-Path -LiteralPath $ServerLogPath) "server log exists: $ServerLogPath"
    Invoke-PsqlRows "SELECT 1;" | Out-Null
    Write-Ok "PostgreSQL is reachable"
    if (-not $SkipAdb) {
        Assert-Check ([bool](Get-Command adb -ErrorAction SilentlyContinue)) "adb is available"
        $devices = Invoke-Adb @("devices")
        $deviceLines = @($devices.Output -split "`r?`n" | Where-Object { $_ -match "\tdevice$" })
        Assert-Check ($deviceLines.Count -ge 1) "adb has a connected device"
        Assert-Check (($deviceLines.Count -eq 1) -or [bool]$DeviceSerial) "adb selects a single device or -DeviceSerial is set"
        if (($deviceLines.Count -eq 1) -or [bool]$DeviceSerial) {
            $pkg = Invoke-Adb @("shell", "dumpsys", "package", $AndroidPackage)
            Assert-Check ($pkg.Output -match "versionName=") "Android package $AndroidPackage is installed"
        }
    } else {
        Write-Warn "adb checks skipped"
    }
}

function Run-Prepare {
    Write-Step "Prepare big video"
    Run-Preflight
    Ensure-BigVideoFixture
    Push-BigVideoFixture
    Write-Host "Manual step: send the pushed >10MB video from Android/Alice to Bob."
}

function Run-BeforeSend {
    Write-Step "BeforeSend snapshot"
    $usage = Get-UploadPartUsage
    $temp = Get-UploadTempStats
    $state = [pscustomobject]@{
        SenderUserId = $SenderUserId
        RecipientUserId = $RecipientUserId
        BaselinePrivateMessageId = Get-PrivateMessageMaxId
        BaselineUploadParts = $usage.Parts
        BaselineUploadPartBytes = $usage.Bytes
        BaselineTempFiles = $temp.Files
        BaselineTempBytes = $temp.Bytes
        BaselineLogLineCount = Get-LogLineCount
        ServerLogPath = $ServerLogPath
        BlobDir = $BlobDir
        RestartTriggered = $false
        RestartedAt = ""
        ObservedBigPart = $false
        CreatedAt = (Get-Date -Format o)
        VideoFixture = $VideoFixture
    }
    Write-Host "private_messages max id before send: $($state.BaselinePrivateMessageId)"
    Write-Host "upload_parts before send: parts=$($usage.Parts) bytes=$($usage.Bytes)"
    Write-Host "temp upload files before send: files=$($temp.Files) bytes=$($temp.Bytes)"
    Save-State $state
}

function Run-WatchRestart {
    Write-Step "Watch upload.saveBigFilePart and restart"
    $state = Load-State
    $deadline = (Get-Date).AddSeconds($WatchTimeoutSeconds)
    $restartDone = $false
    while ((Get-Date) -lt $deadline) {
        $lines = @(Get-LogLinesSince ([int]$state.BaselineLogLineCount) ([string]$state.ServerLogPath))
        $hits = @($lines | Where-Object {
            $_ -like "*upload.saveBigFilePart*" -and $_ -like '*client_type": "android"*'
        })
        if ($hits.Count -ge $RestartAfterParts) {
            Write-Host "observed upload.saveBigFilePart lines=$($hits.Count); restarting server"
            $state.ObservedBigPart = $true
            $state.RestartTriggered = $true
            $state.RestartedAt = (Get-Date -Format o)
            Save-State $state
            $args = @()
            if (-not $BuildOnRestart) {
                $args += "-SkipBuild"
            }
            Invoke-External "powershell" (@("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $RestartScript) + $args) | Out-Null
            $restartDone = $true
            break
        }
        Start-Sleep -Milliseconds 500
    }
    Assert-Check $restartDone "server restarted after upload.saveBigFilePart was observed"
}

function Run-AfterSend {
    Write-Step "AfterSend assertions"
    $state = Load-State
    $messages = @(Get-NewVideoMessages ([long]$state.BaselinePrivateMessageId))
    foreach ($message in $messages) {
        Write-Host ("new private message id={0} kind={1} video={2} doc={3} mime={4} size={5} thumbs={6}" -f $message.MessageId, $message.Kind, $message.Video, $message.DocumentId, $message.MimeType, $message.Size, $message.ThumbCount)
    }
    $videos = @($messages | Where-Object {
        $_.Kind -eq "document" -and $_.Video -eq "true" -and $_.MimeType -eq "video/mp4" -and $_.DocumentId -gt 0 -and $_.Size -ge $MinBigFileBytes
    })
    Assert-Check ($videos.Count -ge 1) "new private message includes a >10MB uploaded video/mp4 document"

    $docIds = @($videos | Select-Object -ExpandProperty DocumentId -Unique)
    $documents = @(Get-DocumentRows $docIds)
    $blobs = @(Get-FileBlobRows $docIds)
    Assert-Check (@($documents | Where-Object { $_.MimeType -eq "video/mp4" -and $_.Size -ge $MinBigFileBytes }).Count -ge 1) "documents row persisted for big video"
    if (-not $AllowMissingThumb) {
        Assert-Check (@($documents | Where-Object { $_.ThumbCount -gt 0 }).Count -ge 1) "big video document has thumbnail metadata"
    }
    $bodyBlobs = @($blobs | Where-Object { $_.LocationKey -like "doc:*" -and $_.LocationKey -notlike "*:m" -and $_.Size -ge $MinBigFileBytes })
    Assert-Check ($bodyBlobs.Count -ge 1) "big video body file_blobs row exists"
    if (-not $AllowMissingThumb) {
        Assert-Check (@($blobs | Where-Object { $_.LocationKey -like "doc:*:m" -and $_.Size -gt 0 }).Count -ge 1) "big video thumbnail file_blobs row exists"
    }
    foreach ($blob in $blobs) {
        if ($blob.Backend -eq "localfs" -and $blob.ObjectKey) {
            Assert-Check (Test-Path -LiteralPath (Get-BlobFilePath $blob.ObjectKey)) "localfs blob exists: $($blob.LocationKey)"
        }
    }

    $usage = Get-UploadPartUsage
    $temp = Get-UploadTempStats
    Write-Host "upload_parts after send: parts=$($usage.Parts) bytes=$($usage.Bytes)"
    Write-Host "temp upload files after send: files=$($temp.Files) bytes=$($temp.Bytes)"
    Assert-Check ($usage.Parts -le [int]$state.BaselineUploadParts) "upload_parts metadata cleaned after successful big upload"
    Assert-Check ($temp.Files -le [int]$state.BaselineTempFiles) "upload temp files cleaned after successful big upload"

    $oldLines = @(Get-LogLinesSince ([int]$state.BaselineLogLineCount) ([string]$state.ServerLogPath))
    $newLines = @(Read-SharedLogLines)
    $combined = @($oldLines + $newLines)
    $bigHits = @($combined | Where-Object { $_ -like "*upload.saveBigFilePart*" -and $_ -like '*client_type": "android"*' })
    $sendMediaHits = @($combined | Where-Object { $_ -like "*messages.sendMedia*" -and $_ -like '*client_type": "android"*' })
    $bad = @($combined | Where-Object { $_ -cmatch "INTERNAL_SERVER_ERROR|rpc error|Unhandled RPC|NOT_IMPLEMENTED|bad_msg|panic|\tERROR\t" })
    Assert-Check (($bigHits.Count -ge 1) -or [bool]$state.ObservedBigPart) "server log has Android upload.saveBigFilePart"
    Assert-Check ($sendMediaHits.Count -ge 1) "server log has Android messages.sendMedia"
    Assert-Check ($bad.Count -eq 0) "server logs have no big-upload-era internal errors or unhandled RPCs"
}

function Finish-Run {
    if ($Failures.Count -gt 0) {
        Write-Host ""
        Write-Host "Validation failed:"
        foreach ($failure in $Failures) { Write-Host " - $failure" }
        exit 1
    }
    Write-Host ""
    Write-Host "Validation passed."
}

switch ($Phase) {
    "Preflight" { Run-Preflight }
    "Prepare" { Run-Prepare }
    "BeforeSend" { Run-BeforeSend }
    "WatchRestart" { Run-WatchRestart }
    "AfterSend" { Run-AfterSend }
    "All" {
        Run-Prepare
        Run-BeforeSend
        Write-Host "Start sending the pushed video from Android/Alice now."
        Run-WatchRestart
        Read-Host "After Android finishes sending the video, press Enter"
        Run-AfterSend
    }
}

Finish-Run
