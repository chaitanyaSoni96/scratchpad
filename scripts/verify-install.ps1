<#
.SYNOPSIS
CI verification harness for scripts/install.ps1 (P5.1/P5.2/P5.5).

.DESCRIPTION
Runs on a real Windows runner and proves what code review cannot: every
installer operation works, every operation is idempotent, the Scheduled Task
registers/starts/stops/unregisters, uninstall never touches the data root,
paths with spaces and non-ASCII characters work, a non-administrator user can
install, and an overridden SCRATCHPAD_ROOT is respected.

The harness itself always runs under pwsh; -Engine selects which PowerShell
engine executes install.ps1 (pwsh = PowerShell 7, powershell = Windows
PowerShell 5.1), so both engines are verified with identical assertions.

Assertions accumulate instead of failing fast, so one CI round surfaces every
bug at once. Exit 0 only when every assertion passed.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('pwsh', 'powershell')]
    [string]$Engine
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$RepoDir = Split-Path -Parent $PSScriptRoot
$Installer = Join-Path $RepoDir 'scripts\install.ps1'
$TaskName = 'scratchpad-web'
$DefaultBinDir = Join-Path $env:LOCALAPPDATA 'scratchpad\bin'
$DataRoot = Join-Path $env:USERPROFILE '.scratchpad'
$MarkerFile = Join-Path $DataRoot 'ci-survival-marker.txt'
$MarkerText = 'the data root must survive install and uninstall'

# Non-ASCII characters are constructed, not literal, so this file stays pure
# ASCII and immune to encoding disagreements between engines.
$EAcute = [string][char]0x00E9   # e with acute accent
$UUml = [string][char]0x00FC     # u with umlaut

$script:Passes = 0
$script:Failures = @()

function Assert {
    param(
        [bool]$Condition,
        [string]$Name,
        [string]$Detail = ''
    )
    if ($Condition) {
        $script:Passes++
        Write-Host "ok   : $Name"
    } else {
        $script:Failures += $Name
        Write-Host "FAIL : $Name"
        if ($Detail -ne '') {
            foreach ($line in ($Detail -split "`n")) { Write-Host "       $line" }
        }
    }
}

function Invoke-Installer {
    param(
        [string[]]$InstallerArgs,
        [string]$ScriptPath = $Installer
    )
    Write-Host ""
    Write-Host ">> $Engine -File $ScriptPath $($InstallerArgs -join ' ')"
    $lines = @(& $Engine -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $ScriptPath @InstallerArgs 2>&1 |
        ForEach-Object { [string]$_ })
    $code = $LASTEXITCODE
    foreach ($l in $lines) { Write-Host "   | $l" }
    Write-Host "   => exit $code"
    [pscustomobject]@{ ExitCode = $code; Output = ($lines -join "`n") }
}

function Get-Task {
    return Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
}

function Wait-HttpUp {
    param([int]$TimeoutSec = 45)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri 'http://127.0.0.1:8737/' -UseBasicParsing -TimeoutSec 3
            if ($r.StatusCode -eq 200) { return $true }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    return $false
}

function Wait-HttpDown {
    param([int]$TimeoutSec = 30)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            Invoke-WebRequest -Uri 'http://127.0.0.1:8737/' -UseBasicParsing -TimeoutSec 2 | Out-Null
            Start-Sleep -Milliseconds 500
        } catch {
            return $true
        }
    }
    return $false
}

function Get-UserPathEntryCount {
    param([string]$Dir)
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    $raw = ''
    if ($null -ne $key) {
        try {
            $v = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            if ($null -ne $v) { $raw = [string]$v }
        } finally { $key.Dispose() }
    }
    $n = 0
    foreach ($e in ($raw -split ';')) {
        if ($e -ne '' -and ([Environment]::ExpandEnvironmentVariables($e).TrimEnd('\') -ieq $Dir.TrimEnd('\'))) { $n++ }
    }
    return $n
}

function Copy-MinimalRepo {
    param([string]$Dest)
    foreach ($sub in @('bin', 'scripts', 'skill')) {
        New-Item -ItemType Directory -Path (Join-Path $Dest $sub) -Force | Out-Null
    }
    Copy-Item -LiteralPath (Join-Path $RepoDir 'bin\scratchpad.exe') -Destination (Join-Path $Dest 'bin') -Force
    Copy-Item -LiteralPath (Join-Path $RepoDir 'bin\scratchpad-web.exe') -Destination (Join-Path $Dest 'bin') -Force
    Copy-Item -LiteralPath $Installer -Destination (Join-Path $Dest 'scripts') -Force
    Copy-Item -LiteralPath (Join-Path $RepoDir 'skill\SKILL.md') -Destination (Join-Path $Dest 'skill') -Force
}

# ---------------------------------------------------------------------------
Write-Host "=== engine: $Engine ($((& $Engine -NoProfile -Command '$PSVersionTable.PSVersion.ToString()')))"
Write-Host "=== repo  : $RepoDir"
Write-Host "=== user  : $env:USERNAME (admin: $(([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)))"

foreach ($exe in @('scratchpad.exe', 'scratchpad-web.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path (Join-Path $RepoDir 'bin') $exe))) {
        Write-Host "::error::bin\$exe missing - build first: go build -o bin/ ./cmd/..."
        exit 1
    }
}

# --- section 0: pre-create the data root with a survival marker -------------
Write-Host ""
Write-Host "=== section 0: data root marker"
New-Item -ItemType Directory -Path $DataRoot -Force | Out-Null
Set-Content -LiteralPath $MarkerFile -Value $MarkerText
Assert (Test-Path -LiteralPath $MarkerFile) 'marker file created in data root before any install'

# --- section 1: unknown verb exits 2 ----------------------------------------
Write-Host ""
Write-Host "=== section 1: unknown verb"
$r = Invoke-Installer @('bogus-verb')
Assert ($r.ExitCode -eq 2) 'unknown verb exits 2' "exit=$($r.ExitCode)`n$($r.Output)"
Assert ($r.Output -match 'usage:') 'unknown verb prints usage' $r.Output

# --- section 2: cli (no -AddToPath) x2 --------------------------------------
Write-Host ""
Write-Host "=== section 2: cli"
$r1 = Invoke-Installer @('cli')
$r2 = Invoke-Installer @('cli')
Assert ($r1.ExitCode -eq 0) 'cli exits 0' $r1.Output
Assert ($r2.ExitCode -eq 0) 'cli is idempotent (second run exits 0)' $r2.Output
Assert (Test-Path -LiteralPath (Join-Path $DefaultBinDir 'scratchpad.exe')) 'cli installed scratchpad.exe'
Assert ($r1.Output -match 'not on the user PATH') 'cli without -AddToPath prints the PATH hint' $r1.Output
Assert ((Get-UserPathEntryCount $DefaultBinDir) -eq 0) 'cli without -AddToPath does not edit the user PATH'

# --- section 3: cli -AddToPath x2 -------------------------------------------
Write-Host ""
Write-Host "=== section 3: cli -AddToPath"
$r1 = Invoke-Installer @('cli', '-AddToPath')
$r2 = Invoke-Installer @('cli', '-AddToPath')
Assert ($r1.ExitCode -eq 0) 'cli -AddToPath exits 0' $r1.Output
Assert ($r2.ExitCode -eq 0) 'cli -AddToPath is idempotent (second run exits 0)' $r2.Output
Assert ((Get-UserPathEntryCount $DefaultBinDir) -eq 1) 'user PATH holds exactly one entry after two -AddToPath runs'
Assert ($r2.Output -match 'already on the user PATH') 'second -AddToPath run reports already-present' $r2.Output

# --- section 4: skill x2 -----------------------------------------------------
Write-Host ""
Write-Host "=== section 4: skill"
$skillTargets = @(
    (Join-Path $env:USERPROFILE '.claude\skills\scratchpad\SKILL.md'),
    (Join-Path $env:USERPROFILE '.pi\agent\skills\scratchpad\SKILL.md')
)
$r1 = Invoke-Installer @('skill')
$r2 = Invoke-Installer @('skill')
Assert ($r1.ExitCode -eq 0) 'skill exits 0' $r1.Output
Assert ($r2.ExitCode -eq 0) 'skill is idempotent (second run exits 0)' $r2.Output
foreach ($t in $skillTargets) {
    Assert (Test-Path -LiteralPath $t) "skill installed at $t"
}

# --- section 5: drop-mcp x2 --------------------------------------------------
Write-Host ""
Write-Host "=== section 5: drop-mcp"
$r1 = Invoke-Installer @('drop-mcp')
$r2 = Invoke-Installer @('drop-mcp')
Assert ($r1.ExitCode -eq 0) 'drop-mcp exits 0' $r1.Output
Assert ($r2.ExitCode -eq 0) 'drop-mcp is idempotent (second run exits 0)' $r2.Output

# --- section 6: all x2 -------------------------------------------------------
Write-Host ""
Write-Host "=== section 6: all"
$r1 = Invoke-Installer @('all')
$r2 = Invoke-Installer @('all')
Assert ($r1.ExitCode -eq 0) 'all exits 0' $r1.Output
Assert ($r2.ExitCode -eq 0) 'all is idempotent (second run exits 0)' $r2.Output

# --- section 7: install x2 (second run hits the exe-lock race) ---------------
Write-Host ""
Write-Host "=== section 7: install"
$r1 = Invoke-Installer @('install')
Assert ($r1.ExitCode -eq 0) 'install exits 0' $r1.Output
Assert ($null -ne (Get-Task)) 'install registered the scheduled task'
Assert (Wait-HttpUp) 'web serves http://127.0.0.1:8737 after install'
# Second run while the server is live: Register-Startup must stop the task and
# Install-Binary must win the exe-lock race via Invoke-WithRetry.
$r2 = Invoke-Installer @('install')
Assert ($r2.ExitCode -eq 0) 'install is idempotent while the server is running (exe-lock retry)' $r2.Output
Assert (Wait-HttpUp) 'web serves again after the second install'

# --- section 8: status (running) --------------------------------------------
Write-Host ""
Write-Host "=== section 8: status while running"
$r = Invoke-Installer @('status')
Assert ($r.ExitCode -eq 0) 'status exits 0 while running' $r.Output
Assert ($r.Output -match 'state') 'status shows the task state' $r.Output
Assert ($r.Output -match 'Running') 'status reports Running' $r.Output

# --- section 9: stop x2 ------------------------------------------------------
Write-Host ""
Write-Host "=== section 9: stop"
$r1 = Invoke-Installer @('stop')
Assert ($r1.ExitCode -eq 0) 'stop exits 0' $r1.Output
Assert (Wait-HttpDown) 'web is down after stop'
$r2 = Invoke-Installer @('stop')
Assert ($r2.ExitCode -eq 0) 'stop is idempotent (second run exits 0)' $r2.Output
$r = Invoke-Installer @('status')
Assert ($r.ExitCode -eq 0) 'status exits 0 while stopped' $r.Output
Assert ($r.Output -match 'not running') 'status reports not running after stop' $r.Output

# --- section 10: start x2 ----------------------------------------------------
Write-Host ""
Write-Host "=== section 10: start"
$r1 = Invoke-Installer @('start')
Assert ($r1.ExitCode -eq 0) 'start exits 0' $r1.Output
Assert (Wait-HttpUp) 'web serves after start'
$r2 = Invoke-Installer @('start')
Assert ($r2.ExitCode -eq 0) 'start is idempotent while already running (MultipleInstances IgnoreNew)' $r2.Output
Assert (Wait-HttpUp) 'web still serves after the second start'

# --- section 11: remove-startup x2 ------------------------------------------
Write-Host ""
Write-Host "=== section 11: remove-startup"
$r1 = Invoke-Installer @('remove-startup')
Assert ($r1.ExitCode -eq 0) 'remove-startup exits 0' $r1.Output
Assert ($null -eq (Get-Task)) 'remove-startup unregistered the task'
Assert (Wait-HttpDown) 'web is down after remove-startup'
$r2 = Invoke-Installer @('remove-startup')
Assert ($r2.ExitCode -eq 0) 'remove-startup is idempotent (second run exits 0)' $r2.Output
Assert ($r2.Output -match 'nothing to remove') 'second remove-startup reports nothing to remove' $r2.Output

# --- section 12: start/status against an unregistered task -------------------
Write-Host ""
Write-Host "=== section 12: unregistered task"
$r = Invoke-Installer @('start')
Assert ($r.ExitCode -ne 0) 'start fails cleanly when the task is not registered' "exit=$($r.ExitCode)`n$($r.Output)"
Assert ($r.Output -match 'not registered') 'start explains the task is not registered' $r.Output
$r = Invoke-Installer @('status')
Assert ($r.ExitCode -eq 0) 'status exits 0 when the task is not registered' $r.Output
Assert ($r.Output -match 'not registered') 'status reports not registered' $r.Output

# --- section 13: uninstall x2, data root survives ---------------------------
Write-Host ""
Write-Host "=== section 13: uninstall"
$r = Invoke-Installer @('install')
Assert ($r.ExitCode -eq 0) 'reinstall before uninstall exits 0' $r.Output
Assert (Wait-HttpUp) 'web serves before uninstall'
# Uninstall while the server is running: Remove-Startup stops the task, and
# the binary removal must win the same exe-lock race the install path retries.
$r1 = Invoke-Installer @('uninstall')
Assert ($r1.ExitCode -eq 0) 'uninstall exits 0 while the server is running' $r1.Output
Assert (-not (Test-Path -LiteralPath (Join-Path $DefaultBinDir 'scratchpad.exe'))) 'uninstall removed scratchpad.exe'
Assert (-not (Test-Path -LiteralPath (Join-Path $DefaultBinDir 'scratchpad-web.exe'))) 'uninstall removed scratchpad-web.exe'
Assert (-not (Test-Path -LiteralPath (Split-Path -Parent $DefaultBinDir))) 'uninstall removed the empty default install dir'
Assert ((Get-UserPathEntryCount $DefaultBinDir) -eq 0) 'uninstall removed the user PATH entry'
foreach ($t in $skillTargets) {
    Assert (-not (Test-Path -LiteralPath $t)) "uninstall removed $t"
}
Assert ($null -eq (Get-Task)) 'uninstall unregistered the task'
Assert (Test-Path -LiteralPath $MarkerFile) 'DATA ROOT SURVIVED: marker file intact after uninstall'
Assert ((Get-Content -LiteralPath $MarkerFile) -eq $MarkerText) 'marker file content unchanged'
$r2 = Invoke-Installer @('uninstall')
Assert ($r2.ExitCode -eq 0) 'uninstall is idempotent (second run exits 0)' $r2.Output
Assert (Test-Path -LiteralPath $MarkerFile) 'marker file intact after the second uninstall'

# --- section 14 (P5.5): install path with a space and a non-ASCII char ------
Write-Host ""
Write-Host "=== section 14: space + non-ASCII paths"
$spaceyRepo = Join-Path $env:RUNNER_TEMP ("scratch pad " + $EAcute)
$spaceyBin = Join-Path $env:RUNNER_TEMP ("target bin " + $UUml + "\bin")
Copy-MinimalRepo $spaceyRepo
$spaceyInstaller = Join-Path $spaceyRepo 'scripts\install.ps1'
$r1 = Invoke-Installer @('all', '-BinDir', $spaceyBin) $spaceyInstaller
Assert ($r1.ExitCode -eq 0) 'all works from a repo path with space+non-ASCII into a -BinDir with space+non-ASCII' $r1.Output
Assert (Test-Path -LiteralPath (Join-Path $spaceyBin 'scratchpad.exe')) 'exe landed in the spacey BinDir'
$r2 = Invoke-Installer @('startup', '-BinDir', $spaceyBin) $spaceyInstaller
Assert ($r2.ExitCode -eq 0) 'startup works with a spacey BinDir' $r2.Output
Assert (Wait-HttpUp) 'web serves from the spacey BinDir'
$r3 = Invoke-Installer @('status', '-BinDir', $spaceyBin) $spaceyInstaller
Assert ($r3.ExitCode -eq 0) 'status works with a spacey BinDir' $r3.Output
Assert ($r3.Output -match 'Running') 'status reports Running for the spacey install' $r3.Output
$r4 = Invoke-Installer @('uninstall', '-BinDir', $spaceyBin) $spaceyInstaller
Assert ($r4.ExitCode -eq 0) 'uninstall works with a spacey BinDir' $r4.Output
Assert ($null -eq (Get-Task)) 'spacey uninstall unregistered the task'
Assert (-not (Test-Path -LiteralPath (Join-Path $spaceyBin 'scratchpad.exe'))) 'spacey uninstall removed the exe'
Assert (Test-Path -LiteralPath (Split-Path -Parent $spaceyBin)) 'uninstall never walks up out of a user-supplied -BinDir'

# --- section 15 (P5.5): overridden SCRATCHPAD_ROOT --------------------------
Write-Host ""
Write-Host "=== section 15: SCRATCHPAD_ROOT override"
$overrideRoot = Join-Path $env:RUNNER_TEMP ("ovr root " + $EAcute + "\store")
try {
    $env:SCRATCHPAD_ROOT = $overrideRoot

    # Shell-only override: the task reads persisted env, so the installer must
    # say the override will NOT apply and print the exact fix.
    $r1 = Invoke-Installer @('startup')
    Assert ($r1.ExitCode -eq 0) 'startup exits 0 with a shell-only SCRATCHPAD_ROOT' $r1.Output
    Assert ($r1.Output -match 'SCRATCHPAD_ROOT' -and $r1.Output -match 'only in this shell') 'shell-only override triggers the NOTE' $r1.Output
    Assert ($r1.Output -match 'SetEnvironmentVariable') 'the NOTE prints the exact persistence command' $r1.Output
    Assert (Wait-HttpUp) 'web serves (with the default root) despite the shell-only override'

    # Persisted override: re-running startup must not warn, and the task-run
    # server must create and use the overridden root.
    [Environment]::SetEnvironmentVariable('SCRATCHPAD_ROOT', $overrideRoot, 'User')
    $r2 = Invoke-Installer @('startup')
    Assert ($r2.ExitCode -eq 0) 'startup exits 0 with a persisted SCRATCHPAD_ROOT' $r2.Output
    Assert ($r2.Output -notmatch 'only in this shell') 'no NOTE when the override is persisted' $r2.Output
    Assert (Wait-HttpUp) 'web serves with the persisted override'
    $deadline = (Get-Date).AddSeconds(20)
    while (-not (Test-Path -LiteralPath $overrideRoot) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
    Assert (Test-Path -LiteralPath $overrideRoot) 'the task-run server created the overridden root (persisted env picked up)'

    # Uninstall must leave the overridden root exactly as untouched as the
    # default one.
    $ovrMarker = Join-Path $overrideRoot 'ovr-marker.txt'
    Set-Content -LiteralPath $ovrMarker -Value $MarkerText
    $r3 = Invoke-Installer @('uninstall')
    Assert ($r3.ExitCode -eq 0) 'uninstall exits 0 with an overridden root' $r3.Output
    Assert (Test-Path -LiteralPath $ovrMarker) 'OVERRIDDEN DATA ROOT SURVIVED uninstall'
} finally {
    [Environment]::SetEnvironmentVariable('SCRATCHPAD_ROOT', $null, 'User')
    Remove-Item Env:SCRATCHPAD_ROOT -ErrorAction SilentlyContinue
}

# --- section 16 (P5.5): non-administrator user ------------------------------
Write-Host ""
Write-Host "=== section 16: non-administrator user"
$naUser = 'spverify'
$naPass = 'Sp-Verify!2026-ci'
$naRepo = 'C:\Users\Public\scratchpad-na'          # readable by every local user
$naOutDir = 'C:\Users\Public\scratchpad-na-out'    # writable by every local user

$naReady = $false
try {
    $secure = ConvertTo-SecureString $naPass -AsPlainText -Force
    New-LocalUser -Name $naUser -Password $secure -PasswordNeverExpires -AccountNeverExpires | Out-Null
    Add-LocalGroupMember -Group 'Users' -Member $naUser
    Copy-MinimalRepo $naRepo
    New-Item -ItemType Directory -Path $naOutDir -Force | Out-Null
    $naReady = $true
} catch {
    Assert $false 'non-admin environment setup (local user + public repo copy)' $_.Exception.Message
}

function Invoke-AsNonAdmin {
    param([string[]]$InstallerArgs)
    $cred = New-Object System.Management.Automation.PSCredential(
        ".\$naUser", (ConvertTo-SecureString $naPass -AsPlainText -Force))
    $stamp = [guid]::NewGuid().ToString('N')
    $outF = Join-Path $naOutDir "out-$stamp.txt"
    $errF = Join-Path $naOutDir "err-$stamp.txt"
    $argList = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
                 '-File', (Join-Path $naRepo 'scripts\install.ps1')) + $InstallerArgs
    Write-Host ""
    Write-Host ">> [as $naUser] $Engine -File install.ps1 $($InstallerArgs -join ' ')"
    $p = Start-Process -FilePath $Engine -ArgumentList $argList -Credential $cred -LoadUserProfile `
        -WorkingDirectory $naOutDir -RedirectStandardOutput $outF -RedirectStandardError $errF -Wait -PassThru
    $out = @()
    if (Test-Path -LiteralPath $outF) { $out += @(Get-Content -LiteralPath $outF) }
    if (Test-Path -LiteralPath $errF) { $out += @(Get-Content -LiteralPath $errF) }
    foreach ($l in $out) { Write-Host "   | $l" }
    Write-Host "   => exit $($p.ExitCode)"
    [pscustomobject]@{ ExitCode = $p.ExitCode; Output = ($out -join "`n") }
}

if ($naReady) {
    $naProfile = "C:\Users\$naUser"
    $r1 = Invoke-AsNonAdmin @('all')
    Assert ($r1.ExitCode -eq 0) 'non-admin: all exits 0' $r1.Output
    Assert (Test-Path -LiteralPath (Join-Path $naProfile 'AppData\Local\scratchpad\bin\scratchpad.exe')) 'non-admin: exe in the non-admin profile'
    Assert (Test-Path -LiteralPath (Join-Path $naProfile '.claude\skills\scratchpad\SKILL.md')) 'non-admin: skill in the non-admin profile'
    $r2 = Invoke-AsNonAdmin @('all')
    Assert ($r2.ExitCode -eq 0) 'non-admin: all is idempotent (second run exits 0)' $r2.Output

    # Task registration needs no elevation. The start-at-the-end of `startup`
    # is expected to behave differently here than on a real machine: the CI
    # non-admin user has no interactive session, and the task runs with an
    # interactive token, so the launched run cannot succeed. Registration and
    # removal are the assertions; the start outcome is recorded, not asserted.
    $r3 = Invoke-AsNonAdmin @('startup')
    Write-Host "   (startup as non-admin exited $($r3.ExitCode); start-now cannot succeed without an interactive session)"
    Assert ($null -ne (Get-Task)) 'non-admin: startup registered the scheduled task without elevation'
    $r4 = Invoke-AsNonAdmin @('status')
    Assert ($r4.ExitCode -eq 0) 'non-admin: status exits 0' $r4.Output
    $r5 = Invoke-AsNonAdmin @('remove-startup')
    Assert ($r5.ExitCode -eq 0) 'non-admin: remove-startup exits 0' $r5.Output
    Assert ($null -eq (Get-Task)) 'non-admin: remove-startup unregistered the task'
    $r6 = Invoke-AsNonAdmin @('uninstall')
    Assert ($r6.ExitCode -eq 0) 'non-admin: uninstall exits 0' $r6.Output
    Assert (-not (Test-Path -LiteralPath (Join-Path $naProfile 'AppData\Local\scratchpad'))) 'non-admin: uninstall removed the install dir'
}

# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "=== summary: $script:Passes passed, $($script:Failures.Count) failed"
if ($script:Failures.Count -gt 0) {
    foreach ($f in $script:Failures) { Write-Host "::error::install.ps1 verification failed: $f" }
    exit 1
}
Write-Host 'install.ps1 verification: all assertions passed'
exit 0
