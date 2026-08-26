<#
.SYNOPSIS
Native Windows installer for scratchpad -- the counterpart of scripts/install.sh.

.DESCRIPTION
Puts the scratchpad CLI (and optionally scratchpad-web) into a user-writable
directory, installs the skill that teaches agents to drive the CLI, cleans up
MCP registrations left by older installs, and manages a per-user logon
Scheduled Task for scratchpad-web. There is no MCP server: the CLI is the only
agent interface.

Everything here is idempotent (safe to re-run; re-running is how updates
propagate) and nothing here ever requires elevation. No operation -- including
uninstall -- touches the data root (%USERPROFILE%\.scratchpad, or
SCRATCHPAD_ROOT when set): install and uninstall never delete user data.

A Scheduled Task is used instead of a Windows Service on purpose: services
typically require elevation and run under a different profile, which conflicts
with %USERPROFILE%\.scratchpad. Foreground execution is a fully supported
alternative:

    & "$env:LOCALAPPDATA\scratchpad\bin\scratchpad-web.exe" --addr 127.0.0.1:8737

.PARAMETER Command
    all             cli + skill + drop-mcp (default; mirrors install.sh all)
    cli             copy scratchpad.exe into -BinDir
    skill           copy skill\SKILL.md into ~\.claude\skills\scratchpad and
                    ~\.pi\agent\skills\scratchpad (copies, not links)
    drop-mcp        remove obsolete MCP registrations left by older installs
    install         all + startup (full native install; mirrors 'make install')
    startup         install scratchpad-web.exe, register the per-user logon
                    Scheduled Task 'scratchpad-web' and start it now
    start           start the Scheduled Task now
    stop            stop the Scheduled Task now
    status          show the Scheduled Task state and last result
    remove-startup  stop and unregister the Scheduled Task (binaries stay)
    uninstall       remove the task, the installed binaries, the user-PATH
                    entry, and the installed skill copies. NEVER touches the
                    data root.

.PARAMETER BinDir
Where the executables are installed. Default: %LOCALAPPDATA%\scratchpad\bin.

.PARAMETER AddToPath
Explicit consent to append -BinDir to the *user* PATH (HKCU\Environment).
Without it the exact command to do so is printed instead. The machine PATH is
never touched.

.PARAMETER Lan
Bind scratchpad-web on 0.0.0.0 instead of loopback. WARNING: the site has no
authentication, so LAN mode exposes all hosted content plus the delete and
notes-write endpoints to every client that can reach the machine.
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$Command = 'all',

    [string]$BinDir,

    [switch]$AddToPath,

    [switch]$Lan
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($env:OS -ne 'Windows_NT') {
    [Console]::Error.WriteLine('install.ps1 is the native Windows installer; use scripts/install.sh on Unix.')
    exit 1
}

# Two supported source layouts, and every payload lookup must accept both.
#
#   git checkout    <repo>\scripts\install.ps1, binaries in <repo>\bin,
#                   skill at <repo>\skill\SKILL.md
#   release archive <extracted>\install.ps1, binaries beside it,
#                   skill at <extracted>\skill\SKILL.md
#
# Resolving only against $RepoDir (the checkout layout) made the shipped
# installer unable to install from the shipped archive: $RepoDir would be the
# *parent of the extracted folder*, so `cli` died with "not found ... build it
# first". Look beside the script first, then fall back to the checkout layout.
$RepoDir = Split-Path -Parent $PSScriptRoot
$TaskName = 'scratchpad-web'
$DefaultBinDir = Join-Path $env:LOCALAPPDATA 'scratchpad\bin'
if (-not $BinDir) { $BinDir = $DefaultBinDir }
if ($Lan) { $Addr = '0.0.0.0:8737' } else { $Addr = '127.0.0.1:8737' }
if ($env:SCRATCHPAD_ROOT) { $DataRoot = $env:SCRATCHPAD_ROOT } else { $DataRoot = Join-Path $env:USERPROFILE '.scratchpad' }

function Show-Usage {
    [Console]::Error.WriteLine('usage: install.ps1 [all|cli|skill|drop-mcp|install|startup|start|stop|status|remove-startup|uninstall] [-BinDir <dir>] [-AddToPath] [-Lan]')
}

# --- user PATH (registry, format-preserving) --------------------------------
# The raw HKCU\Environment Path value is read without expanding %vars% and
# written back as REG_EXPAND_SZ, so entries other than ours survive
# byte-for-byte. Only the user hive is ever touched.

function Get-RawUserPath {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    if ($null -eq $key) { return '' }
    try {
        $v = $key.GetValue('Path', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -eq $v) { return '' }
        return [string]$v
    } finally { $key.Dispose() }
}

function Set-RawUserPath([string]$Value) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    if ($null -eq $key) { throw 'cannot open HKCU\Environment for writing' }
    try {
        $key.SetValue('Path', $Value, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    } finally { $key.Dispose() }
    Publish-EnvironmentChange
}

# Broadcast WM_SETTINGCHANGE "Environment" so already-running shells that
# listen for it (Explorer, Windows Terminal) pick up the new PATH without a
# sign-out. Best-effort: failure to broadcast is not failure to install.
function Publish-EnvironmentChange {
    try {
        if (-not ('Scratchpad.NativeMethods' -as [type])) {
            Add-Type -Namespace Scratchpad -Name NativeMethods -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
        }
        $result = [UIntPtr]::Zero
        [void][Scratchpad.NativeMethods]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
    } catch { }
}

function Test-PathEntryMatches([string]$Entry, [string]$Dir) {
    $e = [Environment]::ExpandEnvironmentVariables($Entry).TrimEnd('\')
    return $e -ieq $Dir.TrimEnd('\')
}

function Add-UserPathEntry([string]$Dir) {
    $raw = Get-RawUserPath
    foreach ($e in @($raw -split ';')) {
        if ($e -ne '' -and (Test-PathEntryMatches $e $Dir)) {
            Write-Host "path: $Dir is already on the user PATH"
            return
        }
    }
    if ($raw -eq '') { $new = $Dir }
    elseif ($raw.EndsWith(';')) { $new = $raw + $Dir }
    else { $new = $raw + ';' + $Dir }
    Set-RawUserPath $new
    Write-Host "path: added $Dir to the user PATH (open a new terminal to pick it up)"
}

function Remove-UserPathEntry([string]$Dir) {
    $raw = Get-RawUserPath
    if ($raw -eq '') { return }
    $entries = @($raw -split ';')
    $kept = @($entries | Where-Object { $_ -eq '' -or -not (Test-PathEntryMatches $_ $Dir) })
    if ($kept.Count -eq $entries.Count) { return }   # nothing of ours there: leave the value untouched
    $kept = @($kept | Where-Object { $_ -ne '' })
    Set-RawUserPath ($kept -join ';')
    Write-Host "path: removed $Dir from the user PATH"
}

# --- binaries ---------------------------------------------------------------

# A just-stopped scratchpad-web.exe can hold its file lock for a moment after
# Stop-ScheduledTask returns; retry briefly instead of failing on the race.
function Invoke-WithRetry([scriptblock]$Body, [int]$Tries = 10) {
    for ($i = 1; ; $i++) {
        try { & $Body; return } catch {
            if ($i -ge $Tries) { throw }
            Start-Sleep -Milliseconds 500
        }
    }
}

# Returns the first candidate that exists as a file, or $null. Candidate order
# encodes the layout preference documented at $RepoDir above.
function Resolve-Payload([string[]]$Candidates) {
    foreach ($c in $Candidates) {
        if (Test-Path -LiteralPath $c -PathType Leaf) { return $c }
    }
    return $null
}

function Install-Binary([string]$Name) {
    $archive = Join-Path $PSScriptRoot $Name
    $checkout = Join-Path (Join-Path $RepoDir 'bin') $Name
    $src = Resolve-Payload @($archive, $checkout)
    if ($null -eq $src) {
        throw "$Name not found. Looked in the release-archive layout ($archive) and the git-checkout layout ($checkout). From a checkout, build it first: go build -o bin/ ./cmd/..."
    }
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    $dst = Join-Path $BinDir $Name
    Invoke-WithRetry { Copy-Item -LiteralPath $src -Destination $dst -Force }
    Write-Host "cli: installed $Name at $dst"
}

function Install-Cli {
    Install-Binary 'scratchpad.exe'
    if ($AddToPath) {
        Add-UserPathEntry $BinDir
    } else {
        $onPath = $false
        foreach ($e in @((Get-RawUserPath) -split ';')) {
            if ($e -ne '' -and (Test-PathEntryMatches $e $BinDir)) { $onPath = $true }
        }
        if (-not $onPath) {
            Write-Host "path: $BinDir is not on the user PATH. To add it, either re-run with -AddToPath"
            Write-Host "      or run:  [Environment]::SetEnvironmentVariable('Path', ([Environment]::GetEnvironmentVariable('Path','User') + ';' + '$BinDir'), 'User')"
        }
    }
}

# --- skill ------------------------------------------------------------------
# Agent Skills standard (agentskills.io): one SKILL.md serves every agent that
# can run a shell. These are copies, not links -- re-running this script is how
# an edited skill/SKILL.md propagates, exactly like install.sh.

function Install-Skill {
    $archive = Join-Path (Join-Path $PSScriptRoot 'skill') 'SKILL.md'
    $checkout = Join-Path (Join-Path $RepoDir 'skill') 'SKILL.md'
    $src = Resolve-Payload @($archive, $checkout)
    if ($null -eq $src) {
        throw "skill source not found. Looked in the release-archive layout ($archive) and the git-checkout layout ($checkout)."
    }
    $targets = @(
        (Join-Path $env:USERPROFILE '.claude\skills\scratchpad'),
        (Join-Path $env:USERPROFILE '.pi\agent\skills\scratchpad')
    )
    foreach ($dst in $targets) {
        New-Item -ItemType Directory -Path $dst -Force | Out-Null
        Copy-Item -LiteralPath $src -Destination (Join-Path $dst 'SKILL.md') -Force
        Write-Host "skill: installed at $(Join-Path $dst 'SKILL.md')"
    }
    # superseded by skill+CLI (same cleanup install.sh performs)
    $obsolete = Join-Path $env:USERPROFILE '.pi\agent\extensions\scratchpad.ts'
    if (Test-Path -LiteralPath $obsolete) {
        Remove-Item -LiteralPath $obsolete -Force
        Write-Host "skill: removed obsolete $obsolete"
    }
}

# --- obsolete MCP cleanup ---------------------------------------------------
# Older versions of the installer registered a /scratchpad-mcp binary. The
# shell installer removes those registrations; here we only do what is safe
# *and format-preserving* in stock PowerShell:
#   * `claude mcp remove` is delegated to the claude CLI, which owns its own
#     config format -- safe.
#   * opencode.json / goose config.yaml are NOT rewritten. PowerShell 5.1's
#     ConvertTo-Json re-serializes the whole file (indentation, key order,
#     unicode escapes, depth truncation), which risks corrupting a user's
#     config. Deliberately doing LESS than install.sh: we detect a leftover
#     entry and print exact manual-removal instructions instead.

function Get-JsonProp($Object, [string]$Name) {
    if ($null -eq $Object) { return $null }
    $p = $Object.PSObject.Properties[$Name]
    if ($null -eq $p) { return $null }
    return $p.Value
}

function Remove-ObsoleteMcp {
    $claude = Get-Command claude -ErrorAction SilentlyContinue
    if ($null -ne $claude) {
        $prev = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            & $claude.Source mcp remove -s user scratchpad 2>&1 | Out-Null
            & $claude.Source mcp remove -s local scratchpad 2>&1 | Out-Null
        } catch { } finally { $ErrorActionPreference = $prev }
        Write-Host 'mcp: removed any claude registration'
    }

    $oc = Join-Path $env:USERPROFILE '.config\opencode\opencode.json'
    if (Test-Path -LiteralPath $oc) {
        $cfg = $null
        try { $cfg = Get-Content -LiteralPath $oc -Raw | ConvertFrom-Json } catch { }
        $mcp = Get-JsonProp $cfg 'mcp'
        $direct = Get-JsonProp $mcp 'scratchpad'
        $nested = Get-JsonProp (Get-JsonProp $mcp 'servers') 'scratchpad'
        if ($null -ne $direct -or $null -ne $nested) {
            Write-Host "mcp: found an obsolete 'scratchpad' entry in $oc"
            Write-Host '     This installer does not rewrite that file (a PowerShell JSON round-trip'
            Write-Host '     would reformat it). Remove it manually: delete the "scratchpad" key under'
            Write-Host '     "mcp" (1.x) or "mcp.servers" (2.x).'
        }
    }

    $goose = Join-Path $env:USERPROFILE '.config\goose\config.yaml'
    if (Test-Path -LiteralPath $goose) {
        $hit = Select-String -LiteralPath $goose -Pattern '^\s*scratchpad\s*:' -Quiet
        if ($hit) {
            Write-Host "mcp: found an obsolete 'scratchpad' entry in $goose"
            Write-Host '     This installer does not rewrite YAML. Remove it manually: delete the'
            Write-Host "     'scratchpad' block under 'extensions'."
        }
    }
}

# --- per-user startup (Scheduled Task) --------------------------------------

function Get-StartupTask {
    return Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
}

function Write-ForegroundHint {
    $webExe = Join-Path $BinDir 'scratchpad-web.exe'
    Write-Host 'scratchpad-web does not need the task -- foreground execution is fully supported:'
    Write-Host "  & '$webExe' --addr 127.0.0.1:8737"
}

function Register-Startup {
    if ($Lan) {
        Write-Host 'startup: WARNING -- -Lan binds 0.0.0.0: the site has no authentication, so this'
        Write-Host '         exposes all hosted content plus the delete and notes-write endpoints to'
        Write-Host '         every client that can reach this machine. Use only on a trusted network.'
    }

    # A running instance holds a lock on the exe; stop it before copying.
    if ($null -ne (Get-StartupTask)) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    }
    Install-Binary 'scratchpad-web.exe'
    $webExe = Join-Path $BinDir 'scratchpad-web.exe'

    $action = New-ScheduledTaskAction -Execute $webExe -Argument "--addr $Addr" -WorkingDirectory $BinDir
    if ($env:USERDOMAIN) { $userId = "$env:USERDOMAIN\$env:USERNAME" } else { $userId = $env:USERNAME }
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $userId
    # ExecutionTimeLimit 0 = no limit (the default would kill the server after
    # 72 hours). Restart* gives on-failure restarts like the systemd unit.
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -MultipleInstances IgnoreNew `
        -ExecutionTimeLimit (New-TimeSpan -Seconds 0) `
        -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

    try {
        Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
    } catch {
        Write-Host "startup: could not register the scheduled task '$TaskName'."
        Write-Host "  Error: $($_.Exception.Message)"
        Write-Host 'This usually means Group Policy on a managed machine blocks per-user task'
        Write-Host 'creation, or the Task Scheduler service is disabled. Nothing else was changed.'
        Write-ForegroundHint
        throw "scheduled task registration failed: $($_.Exception.Message)"
    }
    Write-Host "startup: registered '$TaskName' to run at logon for $userId (--addr $Addr)"

    # The task reads *persisted* (registry) environment variables at logon, not
    # this shell's. A shell-only SCRATCHPAD_ROOT override would silently not
    # apply to the task, so say so and print the exact fix.
    if ($env:SCRATCHPAD_ROOT) {
        $userRoot = [Environment]::GetEnvironmentVariable('SCRATCHPAD_ROOT', 'User')
        $machineRoot = [Environment]::GetEnvironmentVariable('SCRATCHPAD_ROOT', 'Machine')
        if ($env:SCRATCHPAD_ROOT -ne $userRoot -and $env:SCRATCHPAD_ROOT -ne $machineRoot) {
            Write-Host "startup: NOTE -- SCRATCHPAD_ROOT='$env:SCRATCHPAD_ROOT' is set only in this shell."
            Write-Host '  The scheduled task reads user-level environment variables, so it will use'
            if ($userRoot) {
                Write-Host "  SCRATCHPAD_ROOT='$userRoot' (the persisted user value) instead."
            } else {
                Write-Host "  the default root ($(Join-Path $env:USERPROFILE '.scratchpad')) instead."
            }
            Write-Host '  To make the task see this override, persist it and re-run "install.ps1 startup":'
            Write-Host "    [Environment]::SetEnvironmentVariable('SCRATCHPAD_ROOT', '$env:SCRATCHPAD_ROOT', 'User')"
        }
    }

    Start-Startup
}

function Start-Startup {
    if ($null -eq (Get-StartupTask)) {
        throw "scheduled task '$TaskName' is not registered; run: install.ps1 startup"
    }
    try {
        Start-ScheduledTask -TaskName $TaskName
    } catch {
        Write-Host "startup: could not start the scheduled task '$TaskName'."
        Write-Host "  Error: $($_.Exception.Message)"
        Write-ForegroundHint
        throw "scheduled task start failed: $($_.Exception.Message)"
    }
    Write-Host "startup: '$TaskName' started -- http://localhost:8737"
}

function Stop-Startup {
    if ($null -eq (Get-StartupTask)) {
        Write-Host "startup: scheduled task '$TaskName' is not registered; nothing to stop"
        return
    }
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Write-Host "startup: '$TaskName' stopped"
}

function Show-Status {
    $task = Get-StartupTask
    if ($null -eq $task) {
        Write-Host "status: scheduled task '$TaskName' is not registered (run: install.ps1 startup)"
        Write-ForegroundHint
        return
    }
    $info = Get-ScheduledTaskInfo -TaskName $TaskName
    $act = @($task.Actions)[0]
    Write-Host "status: task    : $TaskName"
    Write-Host "status: state   : $($task.State)"
    Write-Host "status: command : $($act.Execute) $($act.Arguments)"
    Write-Host "status: lastRun : $($info.LastRunTime) (result 0x$('{0:X}' -f $info.LastTaskResult))"
    if ("$($task.State)" -eq 'Running') {
        Write-Host 'status: serving : http://localhost:8737'
    } else {
        Write-Host 'status: not running (install.ps1 start, or run scratchpad-web.exe in the foreground)'
    }
}

function Remove-Startup {
    $task = Get-StartupTask
    if ($null -eq $task) {
        Write-Host "startup: scheduled task '$TaskName' is not registered; nothing to remove"
        return
    }
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    Write-Host "startup: unregistered '$TaskName' (binaries and data untouched)"
}

# --- uninstall --------------------------------------------------------------
# Removes what this installer created: the task, the binaries, the user-PATH
# entry, the installed skill copies. It NEVER touches the data root -- neither
# %USERPROFILE%\.scratchpad nor an overridden SCRATCHPAD_ROOT. There is
# deliberately no code path here that can reach it.

function Uninstall-All {
    Remove-Startup

    foreach ($name in @('scratchpad.exe', 'scratchpad-web.exe')) {
        $p = Join-Path $BinDir $name
        if (Test-Path -LiteralPath $p) {
            # Same exe-lock race as Install-Binary: the server Remove-Startup
            # just stopped can hold its file lock for a moment after
            # Stop-ScheduledTask returns.
            Invoke-WithRetry { Remove-Item -LiteralPath $p -Force }
            Write-Host "uninstall: removed $p"
        }
    }
    if ((Test-Path -LiteralPath $BinDir) -and (@(Get-ChildItem -LiteralPath $BinDir -Force).Count -eq 0)) {
        Remove-Item -LiteralPath $BinDir -Force
    }
    # Only walk up to the default parent we created ourselves; never guess at
    # the parent of a user-supplied -BinDir.
    if ($BinDir.TrimEnd('\') -ieq $DefaultBinDir.TrimEnd('\')) {
        $parent = Split-Path -Parent $DefaultBinDir
        if ((Test-Path -LiteralPath $parent) -and (@(Get-ChildItem -LiteralPath $parent -Force).Count -eq 0)) {
            Remove-Item -LiteralPath $parent -Force
        }
    }
    Remove-UserPathEntry $BinDir

    foreach ($dst in @((Join-Path $env:USERPROFILE '.claude\skills\scratchpad'),
                       (Join-Path $env:USERPROFILE '.pi\agent\skills\scratchpad'))) {
        if (Test-Path -LiteralPath $dst) {
            Remove-Item -LiteralPath $dst -Recurse -Force
            Write-Host "uninstall: removed $dst"
        }
    }

    Write-Host "uninstall: done. Data root left untouched: $DataRoot"
}

# --- dispatch ---------------------------------------------------------------

switch ($Command) {
    'all'            { Install-Cli; Install-Skill; Remove-ObsoleteMcp }
    'cli'            { Install-Cli }
    'skill'          { Install-Skill }
    'drop-mcp'       { Remove-ObsoleteMcp }
    'install'        { Install-Cli; Install-Skill; Remove-ObsoleteMcp; Register-Startup }
    'startup'        { Register-Startup }
    'start'          { Start-Startup }
    'stop'           { Stop-Startup }
    'status'         { Show-Status }
    'remove-startup' { Remove-Startup }
    'uninstall'      { Uninstall-All }
    default          { Show-Usage; exit 2 }
}
