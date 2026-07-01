<#
.SYNOPSIS
Summarizes upload.getFile requests in telesrv logs.

.DESCRIPTION
Use this after opening media history in TDesktop or Android. It groups
upload.getFile RPCs by client_type/app_version and reports request count and
duration percentiles. Pass -SinceLine from a previous baseline if desired.
#>
[CmdletBinding()]
param(
    [string]$ServerLogPath,
    [int]$SinceLine = 0,
    [int]$Tail = 0,
    [switch]$ShowSamples
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

function Read-SharedLogLines {
    if (-not (Test-Path -LiteralPath $ServerLogPath)) {
        return @()
    }
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

function Get-Field {
    param([string]$Line, [string]$Name, [string]$Default = "")
    if ($Line -match ('"' + [regex]::Escape($Name) + '"\s*:\s*"([^"]*)"')) {
        return $Matches[1]
    }
    if ($Line -match ('"' + [regex]::Escape($Name) + '"\s*:\s*([^,}]+)')) {
        return $Matches[1]
    }
    return $Default
}

function Convert-DurationMs([string]$Text) {
    if (-not $Text) { return 0.0 }
    if ($Text -match '^([0-9.]+)ms$') { return [double]$Matches[1] }
    if ($Text -match '^([0-9.]+)s$') { return [double]$Matches[1] * 1000.0 }
    if ($Text -match '^([0-9.]+)µs$') { return [double]$Matches[1] / 1000.0 }
    if ($Text -match '^([0-9.]+)us$') { return [double]$Matches[1] / 1000.0 }
    if ($Text -match '^([0-9.]+)ns$') { return [double]$Matches[1] / 1000000.0 }
    return 0.0
}

function Percentile {
    param([double[]]$Values, [double]$P)
    if ($Values.Count -eq 0) { return 0.0 }
    $sorted = @($Values | Sort-Object)
    $idx = [int][Math]::Ceiling($P * $sorted.Count) - 1
    if ($idx -lt 0) { $idx = 0 }
    if ($idx -ge $sorted.Count) { $idx = $sorted.Count - 1 }
    return [double]$sorted[$idx]
}

$allLines = @(Read-SharedLogLines)
$lineCount = $allLines.Count
$lines = $allLines
if ($SinceLine -gt 0) {
    $lines = @($lines | Select-Object -Skip $SinceLine)
}
if ($Tail -gt 0) {
    $lines = @($lines | Select-Object -Last $Tail)
}

$items = New-Object System.Collections.Generic.List[object]
foreach ($line in $lines) {
    if ($line -notlike "*upload.getFile*") {
        continue
    }
    $method = Get-Field $line "method"
    if ($method -notlike "upload.getFile*") {
        continue
    }
    $client = Get-Field $line "client_type" "unknown"
    $app = Get-Field $line "app_version" ""
    $dur = Convert-DurationMs (Get-Field $line "dur" "0ms")
    $items.Add([pscustomobject]@{
        Client = $client
        AppVersion = $app
        DurationMs = $dur
        Line = $line
    }) | Out-Null
}

Write-Host "log=$ServerLogPath"
Write-Host "total_lines=$lineCount analyzed_lines=$($lines.Count) since_line=$SinceLine upload_get_file=$($items.Count)"
Write-Host ""

if ($items.Count -eq 0) {
    Write-Host "No upload.getFile entries found."
    exit 0
}

$groups = $items | Group-Object Client, AppVersion
foreach ($group in $groups) {
    $values = @($group.Group | ForEach-Object { [double]$_.DurationMs })
    $sum = 0.0
    foreach ($v in $values) { $sum += $v }
    $avg = $sum / [Math]::Max(1, $values.Count)
    [pscustomobject]@{
        Client = ($group.Group[0].Client)
        AppVersion = ($group.Group[0].AppVersion)
        Count = $values.Count
        AvgMs = [Math]::Round($avg, 3)
        P50Ms = [Math]::Round((Percentile $values 0.50), 3)
        P95Ms = [Math]::Round((Percentile $values 0.95), 3)
        P99Ms = [Math]::Round((Percentile $values 0.99), 3)
        MaxMs = [Math]::Round((($values | Measure-Object -Maximum).Maximum), 3)
    }
}

if ($ShowSamples) {
    Write-Host ""
    Write-Host "Samples:"
    $items | Select-Object -Last 20 | ForEach-Object { Write-Host $_.Line }
}
