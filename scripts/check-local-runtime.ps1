<#
.SYNOPSIS
Checks the local telesrv runtime state.

.DESCRIPTION
Reports the listening PID/process, git commit, schema version, MTProto port,
Android connection/package status, and recent server log errors. The script is
read-only and is intended to run before/after Android and TDesktop validation.
#>
[CmdletBinding()]
param(
    [int]$Port = 2398,
    [string]$ServerLogPath,
    [string]$AndroidPackage = "org.telegram.messenger.beta",
    [string]$DeviceSerial,
    [string]$PostgresContainer = "telesrv-postgres",
    [string]$Database = "telesrv",
    [string]$DbUser = "telesrv",
    [int]$RecentLogLines = 1200,
    [switch]$SkipAdb
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

$Failures = New-Object System.Collections.Generic.List[string]

function Write-Step([string]$Message) {
    Write-Host ""
    Write-Host "== $Message =="
}

function Add-Failure([string]$Message) {
    $script:Failures.Add($Message) | Out-Null
    Write-Host "[fail] $Message"
}

function Write-Ok([string]$Message) {
    Write-Host "[ok] $Message"
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

function Invoke-PsqlScalar([string]$Sql) {
    $result = Invoke-External "docker" @(
        "exec", $PostgresContainer,
        "psql", "-U", $DbUser, "-d", $Database,
        "-v", "ON_ERROR_STOP=1",
        "-At", "-c", $Sql
    ) -AllowFailure
    if ($result.ExitCode -ne 0) {
        Add-Failure "PostgreSQL query failed: $($result.Output)"
        return ""
    }
    return $result.Output.Trim()
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

function Get-AdbArgs([string[]]$Arguments) {
    if ($DeviceSerial) {
        return @("-s", $DeviceSerial) + $Arguments
    }
    return $Arguments
}

function Invoke-Adb([string[]]$Arguments, [switch]$AllowFailure) {
    Invoke-External "adb" (Get-AdbArgs $Arguments) -AllowFailure:$AllowFailure
}

function Get-JsonFieldFromLog {
    param([string[]]$Lines, [string]$Field)
    for ($i = $Lines.Count - 1; $i -ge 0; $i--) {
        if ($Lines[$i] -match ('"' + [regex]::Escape($Field) + '"\s*:\s*"?([^",}]+)"?')) {
            return $Matches[1]
        }
    }
    return ""
}

Write-Step "Git"
Push-Location $RepoRoot
try {
    $head = (Invoke-External "git" @("rev-parse", "HEAD") -AllowFailure).Output.Trim()
    $branch = (Invoke-External "git" @("branch", "--show-current") -AllowFailure).Output.Trim()
    $dirty = (Invoke-External "git" @("status", "--porcelain", "--untracked-files=no") -AllowFailure).Output.Trim()
    Write-Host "branch=$branch"
    Write-Host "head=$head"
    if ($dirty) {
        Write-Host "tree_state=dirty"
    } else {
        Write-Host "tree_state=clean"
    }
} finally {
    Pop-Location
}

Write-Step "Process and Port"
$listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
if ($listeners.Count -eq 0) {
    Add-Failure "no process is listening on port $Port"
} else {
    foreach ($ownerPid in @($listeners | Select-Object -ExpandProperty OwningProcess -Unique)) {
        $proc = Get-Process -Id $ownerPid -ErrorAction SilentlyContinue
        if ($proc) {
            $path = $null
            try { $path = $proc.Path } catch { $path = "" }
            Write-Host ("pid={0} name={1} start={2} path={3}" -f $proc.Id, $proc.ProcessName, $proc.StartTime, $path)
        } else {
            Write-Host "pid=$ownerPid"
        }
    }
    Write-Ok "port $Port is listening"
}
$established = @(Get-NetTCPConnection -LocalPort $Port -State Established -ErrorAction SilentlyContinue)
Write-Host "established_connections=$($established.Count)"
foreach ($conn in $established | Select-Object -First 12) {
    Write-Host ("  {0}:{1} -> {2}:{3} pid={4}" -f $conn.LocalAddress, $conn.LocalPort, $conn.RemoteAddress, $conn.RemotePort, $conn.OwningProcess)
}

Write-Step "PostgreSQL Schema"
$schema = Invoke-PsqlScalar "SELECT version::text || '|' || dirty::text FROM schema_migrations ORDER BY version DESC LIMIT 1;"
if ($schema) {
    $parts = $schema -split "\|", 2
    Write-Host "schema_version=$($parts[0])"
    Write-Host "schema_dirty=$($parts[1])"
    $schemaDirty = ""
    if ($parts.Count -gt 1) {
        $schemaDirty = $parts[1].Trim().ToLowerInvariant()
    }
    if ($schemaDirty -in @("f", "false", "0")) {
        Write-Ok "schema is clean"
    } else {
        Add-Failure "schema_migrations is dirty"
    }
}

Write-Step "Server Log"
Write-Host "log=$ServerLogPath"
$lines = @(Read-SharedLogLines)
if ($lines.Count -eq 0) {
    Add-Failure "server log is missing or empty"
} else {
    $readyLines = @($lines | Where-Object { $_ -like "*telesrv 服务就绪*" })
    if ($readyLines.Count -gt 0) {
        $ready = @($readyLines)[-1]
        Write-Host $ready
        $runtimeCommit = Get-JsonFieldFromLog @($ready) "git_commit"
        $runtimeSchema = Get-JsonFieldFromLog @($ready) "schema_version"
        $runtimePID = Get-JsonFieldFromLog @($ready) "pid"
        Write-Host "runtime_git_commit=$runtimeCommit"
        Write-Host "runtime_schema_version=$runtimeSchema"
        Write-Host "runtime_pid=$runtimePID"
        if ($head -and $runtimeCommit -and $runtimeCommit -ne $head) {
            Add-Failure "runtime git_commit $runtimeCommit != HEAD $head"
        } elseif ($runtimeCommit) {
            Write-Ok "runtime commit matches HEAD"
        }
    } else {
        Add-Failure "server log has no 'telesrv 服务就绪' line"
    }
    $recent = @($lines | Select-Object -Last $RecentLogLines)
    $bad = @($recent | Where-Object {
        $_ -cmatch "INTERNAL_SERVER_ERROR|Unhandled RPC|NOT_IMPLEMENTED|bad_msg|panic|\tERROR\t"
    })
    if ($bad.Count -eq 0) {
        Write-Ok "recent log has no internal/unhandled/bad_msg errors"
    } else {
        Add-Failure "recent log has $($bad.Count) suspicious error lines"
        $bad | Select-Object -Last 40 | ForEach-Object { Write-Host $_ }
    }
}

Write-Step "Android"
if ($SkipAdb) {
    Write-Host "adb checks skipped"
} else {
    $adb = Get-Command adb -ErrorAction SilentlyContinue
    if (-not $adb) {
        Add-Failure "adb is not available"
    } else {
        $devices = Invoke-Adb @("devices") -AllowFailure
        $deviceLines = @($devices.Output -split "`r?`n" | Where-Object { $_ -match "\tdevice$" })
        Write-Host "adb_devices=$($deviceLines.Count)"
        if ($deviceLines.Count -lt 1) {
            Add-Failure "no adb device connected"
        } elseif ($deviceLines.Count -gt 1 -and -not $DeviceSerial) {
            Add-Failure "multiple adb devices; pass -DeviceSerial"
        } else {
            $model = (Invoke-Adb @("shell", "getprop", "ro.product.model") -AllowFailure).Output.Trim()
            $sdk = (Invoke-Adb @("shell", "getprop", "ro.build.version.sdk") -AllowFailure).Output.Trim()
            $pkg = Invoke-Adb @("shell", "dumpsys", "package", $AndroidPackage) -AllowFailure
            Write-Host "device_model=$model sdk=$sdk"
            if ($pkg.Output -match "versionName=([^\r\n]+)") {
                Write-Ok "Android package $AndroidPackage installed version=$($Matches[1])"
            } else {
                Add-Failure "Android package $AndroidPackage not found"
            }
        }
    }
}

Write-Host ""
if ($Failures.Count -gt 0) {
    Write-Host "Runtime check failed:"
    foreach ($failure in $Failures) {
        Write-Host " - $failure"
    }
    exit 1
}
Write-Host "Runtime check passed."
