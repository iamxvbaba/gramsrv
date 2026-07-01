<#
.SYNOPSIS
Semi-automates the Android -> TDesktop offline channel media recovery check.

.DESCRIPTION
This helper covers the stable parts of the local validation loop:
preflight checks, fixture generation/push, PostgreSQL state snapshots, server
log checks, and pass/fail assertions. It intentionally does not drive the
Android media picker or Telegram Desktop UI; those remain manual steps because
their coordinates and cached state are device/client dependent.

Typical flow:
  1. Run -Phase Prepare to generate and push fixtures to Android.
  2. Close DebugBob/TDesktop, then run -Phase BeforeSend.
  3. Send photo, video, and document from Android/Alice.
  4. Run -Phase AfterSend.
  5. Start DebugBob/TDesktop, open the channel, confirm media is visible.
  6. Run -Phase AfterBobOpen.

Use -Phase All for the same flow with interactive pauses.
#>
[CmdletBinding()]
param(
    [ValidateSet("Preflight", "Prepare", "BeforeSend", "AfterSend", "AfterBobOpen", "All")]
    [string]$Phase = "Preflight",

    [long]$ChannelId = 24,
    [long]$AndroidUserId = 1780269504,
    [long]$BobUserId = 1780269505,

    [string]$AndroidPackage = "org.telegram.messenger.beta",
    [string]$DeviceSerial,

    [string]$PostgresContainer = "telesrv-postgres",
    [string]$Database = "telesrv",
    [string]$DbUser = "telesrv",

    [string]$ServerLogPath,
    [string]$StatePath,
    [string]$FixtureDir,

    [string]$PhotoFixture,
    [string]$VideoFixture,
    [string]$DocumentFixture,

    [string]$RemotePictureDir = "/sdcard/Pictures/telesrv",
    [string]$RemoteMovieDir = "/sdcard/Movies/telesrv",
    [string]$RemoteDocumentDir = "/sdcard/Download/telesrv",

    [int]$ExpectedNewMessages = 3,

    [switch]$AllowMissingVideo,
    [switch]$AllowMissingGenericDocument,
    [switch]$SkipAdb
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
if (-not $ServerLogPath) {
    $ServerLogPath = Join-Path $RepoRoot "logs\app-version-observe-20260609-165537.err.log"
}
if (-not $StatePath) {
    $StatePath = Join-Path $RepoRoot "logs\android-offline-media-recovery-state.json"
}
if (-not $FixtureDir) {
    $FixtureDir = Join-Path $RepoRoot "logs\media-fixtures"
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
    param([string[]]$Arguments)
    Invoke-External "adb" (Get-AdbArgs $Arguments)
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
    return @((Get-Content -LiteralPath $ServerLogPath)).Count
}

function Get-LogLinesSince {
    param([int]$Skip)
    if (-not (Test-Path -LiteralPath $ServerLogPath)) {
        Add-Failure "server log exists at $ServerLogPath"
        return @()
    }
    return @(Get-Content -LiteralPath $ServerLogPath | Select-Object -Skip $Skip)
}

function Get-DialogState {
    param([long]$UserId)
    $rows = @(Invoke-PsqlRows @"
SELECT top_message_id, read_inbox_max_id, unread_count
FROM channel_dialogs
WHERE channel_id = $ChannelId AND user_id = $UserId;
"@)
    if ($rows.Count -ne 1) {
        Add-Failure "channel_dialogs has one row for channel_id=$ChannelId user_id=$UserId"
        return $null
    }
    $parts = $rows[0] -split "\|"
    [pscustomobject]@{
        UserId = $UserId
        TopMessageId = [int]$parts[0]
        ReadInboxMaxId = [int]$parts[1]
        UnreadCount = [int]$parts[2]
    }
}

function Get-ChannelPts {
    $rows = @(Invoke-PsqlRows "SELECT COALESCE(MAX(pts), 0) FROM channel_update_events WHERE channel_id = $ChannelId;")
    if ($rows.Count -eq 0) {
        return 0
    }
    return [int]$rows[0]
}

function Get-NewMessages {
    param([int]$AfterMessageId)
    $rows = @(Invoke-PsqlRows @"
SELECT
  id,
  sender_user_id,
  media->>'kind',
  COALESCE(media->'document'->>'mime_type', ''),
  COALESCE((
    SELECT attr->>'file_name'
    FROM jsonb_array_elements(COALESCE(media->'document'->'attributes', '[]'::jsonb)) attr
    WHERE attr->>'kind' = 'filename'
    LIMIT 1
  ), '')
FROM channel_messages
WHERE channel_id = $ChannelId AND id > $AfterMessageId
ORDER BY id;
"@)
    $items = @()
    foreach ($row in $rows) {
        $parts = $row -split "\|", 5
        $items += [pscustomobject]@{
            Id = [int]$parts[0]
            SenderUserId = [long]$parts[1]
            Kind = $parts[2]
            MimeType = $parts[3]
            FileName = $parts[4]
        }
    }
    return $items
}

function Get-NewEventSummary {
    param([int]$AfterPts)
    $rows = @(Invoke-PsqlRows @"
SELECT COUNT(*), COALESCE(MAX(pts), 0)
FROM channel_update_events
WHERE channel_id = $ChannelId
  AND pts > $AfterPts
  AND event_type = 'new_channel_message';
"@)
    if ($rows.Count -eq 0) {
        return [pscustomobject]@{ Count = 0; MaxPts = 0 }
    }
    $parts = $rows[0] -split "\|"
    [pscustomobject]@{
        Count = [int]$parts[0]
        MaxPts = [int]$parts[1]
    }
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

function New-PhotoFixture {
    param([string]$Path)
    Add-Type -AssemblyName System.Drawing
    $bitmap = New-Object System.Drawing.Bitmap 900, 520
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.Clear([System.Drawing.Color]::FromArgb(34, 137, 116))
    $fontLarge = New-Object System.Drawing.Font("Arial", 36, [System.Drawing.FontStyle]::Bold)
    $fontSmall = New-Object System.Drawing.Font("Arial", 22, [System.Drawing.FontStyle]::Regular)
    $brushWhite = [System.Drawing.Brushes]::White
    $brushYellow = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(245, 184, 60))
    $graphics.DrawString("telesrv offline photo", $fontLarge, $brushWhite, 55, 85)
    $graphics.DrawString((Get-Date -Format "yyyyMMdd-HHmmss"), $fontSmall, $brushWhite, 58, 150)
    $graphics.FillRectangle($brushYellow, 58, 300, 780, 42)
    $bitmap.Save($Path, [System.Drawing.Imaging.ImageFormat]::Jpeg)
    $graphics.Dispose()
    $bitmap.Dispose()
}

function New-DocumentFixture {
    param([string]$Path)
    $content = @"
<?xml version="1.0" encoding="utf-8"?>
<telesrv-offline-media-recovery generated_at="$(Get-Date -Format o)">
  <purpose>Android to TDesktop offline media recovery validation</purpose>
</telesrv-offline-media-recovery>
"@
    Set-Content -LiteralPath $Path -Value $content -Encoding UTF8
}

function New-VideoFixture {
    param([string]$Path)
    $ffmpeg = Get-Command ffmpeg -ErrorAction SilentlyContinue
    if (-not $ffmpeg) {
        Write-Warn "ffmpeg not found; video fixture was not generated"
        return $false
    }
    Invoke-External "ffmpeg" @(
        "-y",
        "-f", "lavfi",
        "-i", "testsrc=size=640x360:rate=30",
        "-t", "1",
        "-pix_fmt", "yuv420p",
        $Path
    ) | Out-Null
    return $true
}

function Ensure-Fixtures {
    New-Item -ItemType Directory -Force -Path $FixtureDir | Out-Null
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    if (-not $PhotoFixture) {
        $script:PhotoFixture = Join-Path $FixtureDir "telesrv-offline-photo-$stamp.jpg"
        New-PhotoFixture $script:PhotoFixture
    }
    if (-not $DocumentFixture) {
        $script:DocumentFixture = Join-Path $FixtureDir "telesrv-offline-doc-$stamp.xml"
        New-DocumentFixture $script:DocumentFixture
    }
    if (-not $VideoFixture) {
        $candidate = Join-Path $FixtureDir "telesrv-offline-video-$stamp.mp4"
        if (New-VideoFixture $candidate) {
            $script:VideoFixture = $candidate
        }
    }
    Assert-Check (Test-Path -LiteralPath $PhotoFixture) "photo fixture exists: $PhotoFixture"
    Assert-Check (Test-Path -LiteralPath $DocumentFixture) "document fixture exists: $DocumentFixture"
    if (-not $AllowMissingVideo) {
        Assert-Check ($VideoFixture -and (Test-Path -LiteralPath $VideoFixture)) "video fixture exists: $VideoFixture"
    } elseif ($VideoFixture) {
        Assert-Check (Test-Path -LiteralPath $VideoFixture) "video fixture exists: $VideoFixture"
    }
}

function Push-Fixtures {
    if ($SkipAdb) {
        Write-Warn "adb push skipped"
        return
    }
    Invoke-Adb @("shell", "mkdir", "-p", $RemotePictureDir, $RemoteMovieDir, $RemoteDocumentDir) | Out-Null
    Invoke-Adb @("push", $PhotoFixture, "$RemotePictureDir/") | Out-Null
    Invoke-Adb @("push", $DocumentFixture, "$RemoteDocumentDir/") | Out-Null
    Invoke-Adb @("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE", "-d", "file://$RemotePictureDir/$(Split-Path -Leaf $PhotoFixture)") | Out-Null
    Invoke-Adb @("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE", "-d", "file://$RemoteDocumentDir/$(Split-Path -Leaf $DocumentFixture)") | Out-Null
    if ($VideoFixture -and (Test-Path -LiteralPath $VideoFixture)) {
        Invoke-Adb @("push", $VideoFixture, "$RemoteMovieDir/") | Out-Null
        Invoke-Adb @("shell", "am", "broadcast", "-a", "android.intent.action.MEDIA_SCANNER_SCAN_FILE", "-d", "file://$RemoteMovieDir/$(Split-Path -Leaf $VideoFixture)") | Out-Null
    }
    Write-Ok "fixtures pushed to Android media folders"
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
}

function Run-Prepare {
    Write-Step "Prepare fixtures"
    Run-Preflight
    Ensure-Fixtures
    Push-Fixtures
    Write-Host "Manual step: close DebugBob/TDesktop, then send these files from Android/Alice:"
    Write-Host "  Photo:    $PhotoFixture"
    if ($VideoFixture) {
        Write-Host "  Video:    $VideoFixture"
    }
    Write-Host "  Document: $DocumentFixture"
}

function Run-BeforeSend {
    Write-Step "BeforeSend snapshot"
    $bob = Get-DialogState $BobUserId
    $alice = Get-DialogState $AndroidUserId
    $pts = Get-ChannelPts
    $lineCount = Get-LogLineCount
    Assert-Check ($null -ne $bob) "Bob dialog state can be read"
    Assert-Check ($null -ne $alice) "Android/Alice dialog state can be read"
    if ($bob) {
        Write-Host "Bob before send: top=$($bob.TopMessageId) read=$($bob.ReadInboxMaxId) unread=$($bob.UnreadCount)"
    }
    if ($alice) {
        Write-Host "Alice before send: top=$($alice.TopMessageId) read=$($alice.ReadInboxMaxId) unread=$($alice.UnreadCount)"
    }
    Write-Host "Channel pts before send: $pts"
    $state = [pscustomobject]@{
        ChannelId = $ChannelId
        AndroidUserId = $AndroidUserId
        BobUserId = $BobUserId
        BaselineTopMessageId = if ($bob) { $bob.TopMessageId } else { 0 }
        BaselineBobReadInboxMaxId = if ($bob) { $bob.ReadInboxMaxId } else { 0 }
        BaselineBobUnreadCount = if ($bob) { $bob.UnreadCount } else { 0 }
        BaselineChannelPts = $pts
        BaselineLogLineCount = $lineCount
        ServerLogPath = $ServerLogPath
        CreatedAt = (Get-Date -Format o)
        PhotoFixture = $PhotoFixture
        VideoFixture = $VideoFixture
        DocumentFixture = $DocumentFixture
    }
    Save-State $state
}

function Run-AfterSend {
    Write-Step "AfterSend assertions"
    $state = Load-State
    $bob = Get-DialogState $BobUserId
    $messages = @(Get-NewMessages ([int]$state.BaselineTopMessageId))
    $events = Get-NewEventSummary ([int]$state.BaselineChannelPts)
    foreach ($message in $messages) {
        Write-Host ("new message id={0} sender={1} kind={2} mime={3} file={4}" -f $message.Id, $message.SenderUserId, $message.Kind, $message.MimeType, $message.FileName)
    }
    if ($bob) {
        Write-Host "Bob after send: top=$($bob.TopMessageId) read=$($bob.ReadInboxMaxId) unread=$($bob.UnreadCount)"
    }
    Write-Host "new channel events after baseline: count=$($events.Count) max_pts=$($events.MaxPts)"
    Assert-Check ($messages.Count -ge $ExpectedNewMessages) "at least $ExpectedNewMessages new channel messages were written"
    Assert-Check (@($messages | Where-Object { $_.SenderUserId -eq $AndroidUserId }).Count -ge $ExpectedNewMessages) "new channel messages are from Android/Alice"
    Assert-Check (@($messages | Where-Object { $_.Kind -eq "photo" }).Count -ge 1) "new messages include uploaded photo"
    Assert-Check (@($messages | Where-Object { $_.Kind -eq "document" }).Count -ge 1) "new messages include uploaded document"
    if (-not $AllowMissingVideo) {
        Assert-Check (@($messages | Where-Object { $_.MimeType -eq "video/mp4" }).Count -ge 1) "new messages include video/mp4 document"
    }
    if (-not $AllowMissingGenericDocument) {
        Assert-Check (@($messages | Where-Object { $_.Kind -eq "document" -and $_.MimeType -ne "video/mp4" }).Count -ge 1) "new messages include a generic non-video document"
    }
    if ($bob) {
        Assert-Check ($bob.TopMessageId -gt [int]$state.BaselineTopMessageId) "Bob top_message_id advanced while offline"
        Assert-Check ($bob.ReadInboxMaxId -eq [int]$state.BaselineBobReadInboxMaxId) "Bob read_inbox_max_id did not advance before opening TDesktop"
        Assert-Check ($bob.UnreadCount -ge $ExpectedNewMessages) "Bob unread_count reflects offline messages"
    }
    Assert-Check ($events.Count -ge $ExpectedNewMessages) "durable channel_update_events exist for new messages"
}

function Run-AfterBobOpen {
    Write-Step "AfterBobOpen assertions"
    $state = Load-State
    $bob = Get-DialogState $BobUserId
    if ($bob) {
        Write-Host "Bob after open: top=$($bob.TopMessageId) read=$($bob.ReadInboxMaxId) unread=$($bob.UnreadCount)"
        Assert-Check ($bob.ReadInboxMaxId -ge $bob.TopMessageId) "Bob read_inbox_max_id catches up to current top"
        Assert-Check ($bob.UnreadCount -eq 0) "Bob unread_count is cleared after opening the channel"
    }
    $lines = @(Get-LogLinesSince ([int]$state.BaselineLogLineCount))
    $bad = @($lines | Where-Object { $_ -match "Unhandled RPC|NOT_IMPLEMENTED|bad_msg" })
    Assert-Check ($bad.Count -eq 0) "server log has no new Unhandled RPC / NOT_IMPLEMENTED / bad_msg entries"
    $expectedTDesktop = @(
        "messages.getHistory",
        "messages.getPeerDialogs",
        "upload.getFile",
        "channels.readHistory",
        "updates.getChannelDifference"
    )
    foreach ($method in $expectedTDesktop) {
        $hits = @($lines | Where-Object { $_ -like "*$method*" -and $_ -like '*client_type": "tdesktop"*' })
        Assert-Check ($hits.Count -ge 1) "TDesktop issued $method after Bob opened the channel"
    }
}

function Finish-Run {
    if ($Failures.Count -gt 0) {
        Write-Host ""
        Write-Host "Validation failed:"
        foreach ($failure in $Failures) {
            Write-Host "  - $failure"
        }
        exit 1
    }
    Write-Host ""
    Write-Host "Validation phase '$Phase' passed."
}

switch ($Phase) {
    "Preflight" {
        Run-Preflight
    }
    "Prepare" {
        Run-Prepare
    }
    "BeforeSend" {
        Run-BeforeSend
    }
    "AfterSend" {
        Run-AfterSend
    }
    "AfterBobOpen" {
        Run-AfterBobOpen
    }
    "All" {
        Run-Prepare
        Run-BeforeSend
        Read-Host "Close DebugBob/TDesktop if needed, send photo/video/document from Android, then press Enter"
        Run-AfterSend
        Read-Host "Start DebugBob/TDesktop, open the channel, confirm media renders, then press Enter"
        Run-AfterBobOpen
    }
}

Finish-Run
