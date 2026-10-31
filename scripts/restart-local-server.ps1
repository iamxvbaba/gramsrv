<#
.SYNOPSIS
Builds and restarts the local telesrv process with explicit runtime logs.

.DESCRIPTION
This helper is meant for Windows development loops. It builds the current
workspace into a staging executable, stops the repo-local process currently
listening on the configured MTProto port, promotes the new executable, starts it
hidden, and verifies that the port is listening again.
#>
[CmdletBinding()]
param(
    [string]$Listen = "0.0.0.0:2398",
    [string]$AdvertiseIP,
    [string]$PostgresDSN,
    [string]$PostgresContainer = "telesrv-postgres",
    [string]$PostgresUser = "telesrv",
    [string]$ExePath,
    [string]$LogDir,
    [int]$HealthTimeoutSeconds = 20,
    [int]$Tail = 80,
    [switch]$SkipBuild,
    [switch]$NoStart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RepoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
if (-not $ExePath) {
    $ExePath = Join-Path $RepoRoot "bin\telesrv.exe"
}
if (-not $LogDir) {
    $LogDir = Join-Path $RepoRoot "logs"
}

$ExePath = [System.IO.Path]::GetFullPath($ExePath)
$LogDir = [System.IO.Path]::GetFullPath($LogDir)
$BinDir = Split-Path -Parent $ExePath
$NextExePath = Join-Path $BinDir "telesrv.next.exe"

function Write-Step {
    param([string]$Message)
    Write-Host ""
    Write-Host "== $Message =="
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

function Get-GitOutput {
    param([string[]]$Arguments, [string]$Default = "unknown")
    $res = Invoke-External "git" $Arguments -AllowFailure
    if ($res.ExitCode -ne 0) {
        return $Default
    }
    $text = $res.Output.Trim()
    if ($text.Length -eq 0) {
        return $Default
    }
    return $text
}

function Get-ListenPort {
    param([string]$Address)
    if ($Address -match '^\[.+\]:(\d+)$') {
        return [int]$Matches[1]
    }
    if ($Address -match ':(\d+)$') {
        return [int]$Matches[1]
    }
    throw "Cannot parse listen port from '$Address'"
}

function Test-PathUnderRepo {
    param([string]$Path)
    if (-not $Path) {
        return $false
    }
    $full = [System.IO.Path]::GetFullPath($Path)
    return $full.StartsWith($RepoRoot, [System.StringComparison]::OrdinalIgnoreCase)
}

function Test-RepoTelesrvProcess {
    param(
        [object]$Process,
        [string]$ExePath
    )
    if (-not $Process) {
        return $false
    }

    $path = $null
    try {
        $path = $Process.Path
    } catch {
        $path = $null
    }

    if ($path) {
        $fullPath = [System.IO.Path]::GetFullPath($path)
        $fullExePath = [System.IO.Path]::GetFullPath($ExePath)
        $binDir = [System.IO.Path]::GetFullPath((Split-Path -Parent $fullExePath))
        $fileName = [System.IO.Path]::GetFileName($fullPath)

        if ($fullPath.Equals($fullExePath, [System.StringComparison]::OrdinalIgnoreCase)) {
            return $true
        }
        if ($fullPath.StartsWith($binDir, [System.StringComparison]::OrdinalIgnoreCase) -and ($fileName -like "telesrv*.exe*")) {
            return $true
        }
        return $false
    }

    # Path can be unavailable for protected or already-exiting processes. Only
    # take ownership of telesrv-looking processes in that ambiguous state.
    return (($Process.ProcessName -eq "telesrv") -or ($Process.ProcessName -like "telesrv*"))
}

function Get-RepoTelesrvProcesses {
    param([int]$Port, [string]$ExePath)
    # Deduplicate by PID, merging two discovery paths:
    #   1) Port listeners -- the primary path, but Get-NetTCPConnection occasionally
    #      returns empty (once misjudged as "no listener", leaving the old process
    #      running and the promote copy hitting a file lock).
    #   2) By process name/path + telesrv* executables under the repo bin -- a fallback
    #      covering missed ports that also catches instances "holding a telesrv.exe /
    #      telesrv.exe~ file lock but not yet listening" (which must be stopped before
    #      the promote copy).
    $foundByPid = @{}

    $listenerPids = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique)
    foreach ($ownerPid in $listenerPids) {
        if (-not $ownerPid) {
            continue
        }
        $proc = Get-Process -Id $ownerPid -ErrorAction SilentlyContinue
        if (-not $proc) {
            continue
        }
        $path = $null
        try {
            $path = $proc.Path
        } catch {
            $path = $null
        }
        $procId = [int]$proc.Id
        $isRepoProcess = Test-RepoTelesrvProcess -Process $proc -ExePath $ExePath
        if ($isRepoProcess) {
            $foundByPid[$procId] = $proc
        } else {
            throw "Port $Port is held by non-repo process PID $($proc.Id) ($($proc.ProcessName)) at '$path'"
        }
    }

    $candidateProcesses = @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -eq "telesrv" -or $_.ProcessName -like "telesrv*" })
    foreach ($proc in $candidateProcesses) {
        $procId = [int]$proc.Id
        if ($foundByPid.ContainsKey($procId)) {
            continue
        }
        # Take over only instances inside the repo: path under repo/bin, or an
        # unreadable path whose process name looks like telesrv (fallback). Same-named
        # processes outside the repo (run by the user elsewhere) are never touched.
        $isRepoProcess = Test-RepoTelesrvProcess -Process $proc -ExePath $ExePath
        if ($isRepoProcess) {
            $foundByPid[$procId] = $proc
        }
    }

    return @($foundByPid.Values)
}

function Wait-ProcessesExited {
    param([int[]]$Pids)
    if (-not $Pids -or $Pids.Count -eq 0) {
        return
    }
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        $alive = @($Pids | Where-Object { Get-Process -Id $_ -ErrorAction SilentlyContinue })
        if ($alive.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for old telesrv process(es) to exit: $($Pids -join ', ')"
}

function Wait-PortFree {
    param([int]$Port, [int[]]$Pids)
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $deadline) {
        $stillListening = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue |
            Where-Object { $Pids -contains $_.OwningProcess })
        if ($stillListening.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for old telesrv listener on port $Port to stop"
}

$ListenPort = Get-ListenPort $Listen
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

Push-Location $RepoRoot
try {
    $branch = Get-GitOutput @("branch", "--show-current")
    $postgresDatabase = "external"
    if ([string]::IsNullOrWhiteSpace($PostgresDSN)) {
        switch ($branch) {
            "main" { $postgresDatabase = "telesrv_main" }
            "v2" { $postgresDatabase = "telesrv_v2" }
            default {
                throw "Branch '$branch' has no implicit local PostgreSQL database. Pass -PostgresDSN explicitly."
            }
        }
        Write-Step "Resolve branch-isolated PostgreSQL"
        & (Join-Path $PSScriptRoot "ensure-local-databases.ps1") `
            -PostgresContainer $PostgresContainer `
            -DbUser $PostgresUser
        $PostgresDSN = "postgres://telesrv:telesrv@127.0.0.1:5432/$postgresDatabase`?sslmode=disable"
        Write-Host "[ok] branch=$branch database=$postgresDatabase"
    } else {
        Write-Step "Use explicit PostgreSQL DSN"
        Write-Host "[ok] branch=$branch database=explicit-override"
    }

    if (-not $SkipBuild) {
        Write-Step "Build telesrv"
        $commit = Get-GitOutput @("rev-parse", "HEAD")
        $dirty = Get-GitOutput @("status", "--porcelain", "--untracked-files=no") -Default ""
        $treeState = "clean"
        if ($dirty.Length -gt 0) {
            $treeState = "dirty"
        }
        $buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        $ldflags = "-X main.gitCommit=$commit -X main.gitBranch=$branch -X main.gitTreeState=$treeState -X main.buildTime=$buildTime"

        Remove-Item -LiteralPath $NextExePath -ErrorAction SilentlyContinue
        Invoke-External "go" @("build", "-ldflags", $ldflags, "-o", $NextExePath, ".\cmd\telesrv") | Out-Null
        Write-Host "[ok] built $NextExePath"
        Write-Host "[ok] commit=$commit branch=$branch tree=$treeState build_time=$buildTime"
    }

    Write-Step "Stop old telesrv processes"
    $oldProcesses = @(Get-RepoTelesrvProcesses $ListenPort $ExePath)
    if ($oldProcesses.Count -eq 0) {
        Write-Host "[ok] no existing repo-local listener on port $ListenPort"
    } else {
        $oldPids = @($oldProcesses | Select-Object -ExpandProperty Id)
        foreach ($proc in $oldProcesses) {
            Write-Host "[stop] PID $($proc.Id) $($proc.ProcessName) $($proc.Path)"
            Stop-Process -Id $proc.Id -Force
        }
        Wait-ProcessesExited $oldPids
        Wait-PortFree $ListenPort $oldPids
        Write-Host "[ok] stopped old listener(s): $($oldPids -join ', ')"
    }

    if (-not $SkipBuild) {
        Write-Step "Promote executable"
        # telesrv.exe may be file-locked by an instance an external watcher/watchdog
        # respawned first; after stopping the holder, retry briefly to avoid hitting
        # "being used by another process" and failing the copy (once caused promote to fail).
        $promoted = $false
        for ($attempt = 1; $attempt -le 10; $attempt++) {
            try {
                Copy-Item -LiteralPath $NextExePath -Destination $ExePath -Force -ErrorAction Stop
                $promoted = $true
                break
            } catch {
                $holders = @(Get-RepoTelesrvProcesses $ListenPort $ExePath)
                foreach ($holder in $holders) {
                    Write-Host "[stop] PID $($holder.Id) holding $ExePath; retry $attempt/10"
                    Stop-Process -Id $holder.Id -Force -ErrorAction SilentlyContinue
                }
                Start-Sleep -Milliseconds 300
            }
        }
        if (-not $promoted) {
            throw "Failed to promote $ExePath after retries (file kept locked; an external watcher may be respawning telesrv)"
        }
        Remove-Item -LiteralPath $NextExePath -ErrorAction SilentlyContinue
        Write-Host "[ok] promoted $ExePath"
    } elseif (-not (Test-Path -LiteralPath $ExePath)) {
        throw "Executable not found: $ExePath"
    }

    if ($NoStart) {
        Write-Host "[ok] NoStart requested; executable is ready but not running"
        return
    }

    Write-Step "Start telesrv"
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $stdoutPath = Join-Path $LogDir "telesrv-$stamp.out.log"
    $stderrPath = Join-Path $LogDir "telesrv-$stamp.err.log"

    $env:TELESRV_LISTEN = $Listen
    $env:TELESRV_POSTGRES_DSN = $PostgresDSN
    if ($AdvertiseIP) {
        $env:TELESRV_ADVERTISE_IP = $AdvertiseIP
    }

    $proc = Start-Process -FilePath $ExePath `
        -WorkingDirectory $RepoRoot `
        -RedirectStandardOutput $stdoutPath `
        -RedirectStandardError $stderrPath `
        -PassThru `
        -WindowStyle Hidden

    $deadline = (Get-Date).AddSeconds($HealthTimeoutSeconds)
    $listening = $false
    while ((Get-Date) -lt $deadline) {
        $proc.Refresh()
        if ($proc.HasExited) {
            $errTail = ""
            if (Test-Path -LiteralPath $stderrPath) {
                $errTail = (Get-Content -LiteralPath $stderrPath -Tail $Tail -ErrorAction SilentlyContinue) -join "`n"
            }
            throw "telesrv exited during startup with code $($proc.ExitCode):`n$errTail"
        }
        $conn = @(Get-NetTCPConnection -LocalPort $ListenPort -State Listen -ErrorAction SilentlyContinue |
            Where-Object { $_.OwningProcess -eq $proc.Id })
        if ($conn.Count -gt 0) {
            $listening = $true
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $listening) {
        throw "telesrv PID $($proc.Id) did not listen on port $ListenPort within ${HealthTimeoutSeconds}s"
    }

    Write-Host "[ok] started PID $($proc.Id), listening on $Listen"
    Write-Host "[ok] stdout: $stdoutPath"
    Write-Host "[ok] stderr: $stderrPath"
    if (Test-Path -LiteralPath $stderrPath) {
        Get-Content -LiteralPath $stderrPath -Tail $Tail
    }

    [pscustomobject]@{
        Pid = $proc.Id
        Listen = $Listen
        AdvertiseIP = $env:TELESRV_ADVERTISE_IP
        PostgresDatabase = $postgresDatabase
        Exe = $ExePath
        Stdout = $stdoutPath
        Stderr = $stderrPath
    }
} finally {
    Pop-Location
}
