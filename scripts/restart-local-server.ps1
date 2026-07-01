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
    # 按 PID 去重，合并两条发现路径：
    #   1) 端口监听者——主路径，但 Get-NetTCPConnection 偶发返回空（曾漏判成 "no listener"，
    #      导致旧进程没被停、promote 复制撞文件锁）。
    #   2) 按进程名/路径 + 仓库 bin 下 telesrv* 可执行文件——兜底覆盖端口漏报，并能抓到
    #      “持有 telesrv.exe / telesrv.exe~ 文件锁但端口尚未就绪”的实例（promote 复制前必须停掉）。
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
        # 只接管仓库内的实例：路径在 repo/bin 下，或路径不可读但进程名看起来就是 telesrv（兜底）。
        # 仓库外的同名进程（用户在别处跑的）一律不动。
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
    if (-not $SkipBuild) {
        Write-Step "Build telesrv"
        $commit = Get-GitOutput @("rev-parse", "HEAD")
        $branch = Get-GitOutput @("branch", "--show-current")
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
        # telesrv.exe 可能被外部 watcher/watchdog 抢先重生的实例占用文件锁；停掉持有者后短暂重试，
        # 避免直接撞 "being used by another process" 复制失败（曾因此 promote 失败）。
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
        Exe = $ExePath
        Stdout = $stdoutPath
        Stderr = $stderrPath
    }
} finally {
    Pop-Location
}
