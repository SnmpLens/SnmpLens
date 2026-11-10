<#
.SYNOPSIS
Updates the latest published release with this branch's installer, the way that
release's own updater does, and checks what is left.

.DESCRIPTION
An update is run by the copy being replaced. The latest release decides whether
to run the installer or to replace itself — its own code, compiled from its tag
and run from where it is installed — then launches the installer with no
arguments and quits. So what this branch's installer meets is the old release's
files, registry and choices, and that is what is installed here first, by the
old release's own installer.

The installer is then driven page by page, as a person would, by its window
controls: the page names are logged, a message box is a failure, and the
scenario picks a radio button where a person would.

Run it on a disposable machine — the Upgrade workflow: it installs for all
users, writes the registry and installs a Windows service.
#>
param(
    [Parameter(Mandatory)]
    [ValidateSet('all-users-service', 'for-me', 'for-me-to-all-users', 'portable')]
    [string]$Scenario,
    # Holds this branch's SnmpLens-windows-amd64-setup.exe and SnmpLens.exe.
    [Parameter(Mandatory)][string]$Next,
    # The version this branch's build was given.
    [Parameter(Mandatory)][string]$NextVersion,
    [string]$Repository = $env:GITHUB_REPOSITORY
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3

$failures = [System.Collections.Generic.List[string]]::new()
$notes = [System.Collections.Generic.List[string]]::new()

function Check([bool]$ok, [string]$what) {
    if ($ok) {
        Write-Host "  ok    $what"
    } else {
        Write-Host "  FAIL  $what" -ForegroundColor Red
        $failures.Add($what)
    }
}

function Note([string]$what) {
    Write-Host "  note  $what" -ForegroundColor Yellow
    $notes.Add($what)
}

function Step([string]$what) { Write-Host "`n== $what" }

Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

public static class Win {
    delegate bool EnumProc(IntPtr hwnd, IntPtr lParam);
    [DllImport("user32.dll")] static extern bool EnumWindows(EnumProc f, IntPtr l);
    [DllImport("user32.dll")] static extern bool EnumChildWindows(IntPtr parent, EnumProc f, IntPtr l);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
    [DllImport("user32.dll")] public static extern bool IsWindowEnabled(IntPtr h);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
    [DllImport("user32.dll")] public static extern int GetDlgCtrlID(IntPtr h);
    [DllImport("user32.dll")] static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
    [DllImport("user32.dll")] static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
    [DllImport("kernel32.dll")] static extern IntPtr OpenProcess(uint access, bool inherit, uint pid);
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr h);
    [DllImport("advapi32.dll")] static extern bool OpenProcessToken(IntPtr p, uint access, out IntPtr token);
    [DllImport("advapi32.dll")] static extern bool GetTokenInformation(IntPtr t, int cls, out int info, int len, out int ret);

    public static List<IntPtr> Windows(int[] pids) {
        var set = new HashSet<uint>();
        foreach (var p in pids) set.Add((uint)p);
        var found = new List<IntPtr>();
        EnumWindows((h, l) => {
            uint pid;
            GetWindowThreadProcessId(h, out pid);
            if (set.Contains(pid) && IsWindowVisible(h)) found.Add(h);
            return true;
        }, IntPtr.Zero);
        return found;
    }

    public static List<IntPtr> Children(IntPtr parent) {
        var found = new List<IntPtr>();
        EnumChildWindows(parent, (h, l) => { found.Add(h); return true; }, IntPtr.Zero);
        return found;
    }

    public static string Text(IntPtr h) { var s = new StringBuilder(2048); GetWindowText(h, s, s.Capacity); return s.ToString(); }
    public static string Class(IntPtr h) { var s = new StringBuilder(256); GetClassName(h, s, s.Capacity); return s.ToString(); }
    [DllImport("user32.dll")] static extern IntPtr GetParent(IntPtr h);
    // What the dialog receives when the button is clicked: WM_COMMAND with
    // BN_CLICKED, posted so that a click which ends the dialog does not block
    // the driver. Not BM_CLICK, which goes through the mouse and fails once the
    // dialog is no longer the active window — measured: every page went by
    // until the old release's window came up, and Install was then never
    // pressed.
    public static void Press(IntPtr button) {
        int id = GetDlgCtrlID(button);
        PostMessage(GetParent(button), 0x0111, (IntPtr)(id & 0xFFFF), button);
    }
    // BM_GETCHECK and BM_SETCHECK
    public static bool Checked(IntPtr h) { return SendMessage(h, 0x00F0, IntPtr.Zero, IntPtr.Zero) == (IntPtr)1; }
    public static void SetChecked(IntPtr h, bool on) { SendMessage(h, 0x00F1, (IntPtr)(on ? 1 : 0), IntPtr.Zero); }

    // 1 elevated, 0 not, -1 unknown.
    public static int Elevated(int pid) {
        IntPtr p = OpenProcess(0x1000, false, (uint)pid); // PROCESS_QUERY_LIMITED_INFORMATION
        if (p == IntPtr.Zero) return -1;
        try {
            IntPtr t;
            if (!OpenProcessToken(p, 0x0008, out t)) return -1; // TOKEN_QUERY
            try {
                int e, r;
                if (!GetTokenInformation(t, 20, out e, 4, out r)) return -1; // TokenElevation
                return e != 0 ? 1 : 0;
            } finally { CloseHandle(t); }
        } finally { CloseHandle(p); }
    }
}
'@

$uninstall = 'SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\SnmpLens'
$uninstall32 = 'SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\SnmpLens'
$runKey = 'SOFTWARE\Microsoft\Windows\CurrentVersion\Run'
$runKey32 = 'SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Run'

function Get-RegValue([string]$path, [string]$name) {
    $item = Get-ItemProperty -Path $path -ErrorAction SilentlyContinue
    if ($null -eq $item) { return $null }
    $prop = $item.PSObject.Properties[$name]
    if ($null -eq $prop) { return $null }
    return [string]$prop.Value
}

function Get-Controls([IntPtr]$window) {
    foreach ($c in [Win]::Children($window)) {
        [pscustomobject]@{
            Handle  = $c
            Id      = [Win]::GetDlgCtrlID($c)
            Class   = [Win]::Class($c)
            Text    = [Win]::Text($c)
            Visible = [Win]::IsWindowVisible($c)
            Enabled = [Win]::IsWindowEnabled($c)
        }
    }
}

# Runs an installer and presses its default button on every page until no copy
# of it is left: a copy that relaunches itself elevated exits before the copy it
# started has a window, so absence is believed only once it has lasted.
function Invoke-Installer {
    param([string]$Path, [string[]]$Arguments = @(), [scriptblock]$OnPage = {}, [int]$TimeoutSeconds = 420)
    $name = [IO.Path]::GetFileNameWithoutExtension($Path)
    $started = if ($Arguments.Count) { Start-Process -FilePath $Path -ArgumentList $Arguments -PassThru } else { Start-Process -FilePath $Path -PassThru }
    Write-Host "    started $name ($($started.Id)) $($Arguments -join ' ')"
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $seen = @{}
    $goneSince = $null
    $lastPage = $null
    $pageSince = Get-Date
    while ($true) {
        if ((Get-Date) -gt $deadline) {
            Save-Screen 'timeout'
            throw "the installer did not finish within $TimeoutSeconds s"
        }
        $procs = @(Get-Process -Name $name -ErrorAction SilentlyContinue)
        if ($procs.Count -eq 0) {
            if ($null -eq $goneSince) { $goneSince = Get-Date }
            elseif (((Get-Date) - $goneSince).TotalSeconds -gt 8) { return }
            Start-Sleep -Milliseconds 500
            continue
        }
        $goneSince = $null
        foreach ($w in [Win]::Windows([int[]]@($procs.Id))) {
            $controls = @(Get-Controls $w)
            $visible = @($controls | Where-Object Visible)
            $texts = @($visible | Where-Object { $_.Text } | ForEach-Object { $_.Text.Trim() })
            $page = (@([Win]::Text($w)) + $texts) -join ' | '
            if (-not $seen.ContainsKey($page)) {
                $seen[$page] = $true
                Write-Host "    page: $page"
            }
            $isLanguage = @($controls | Where-Object Class -eq 'ComboBox').Count -gt 0 -and $controls.Count -le 6
            $isMessage = -not $isLanguage -and $controls.Count -le 5 -and
                @($controls | Where-Object { $_.Class -eq 'Static' -and $_.Id -eq 0xFFFF }).Count -gt 0
            if ($isMessage) {
                if (-not $seen.ContainsKey("message:$page")) {
                    $seen["message:$page"] = $true
                    $failures.Add("the installer showed a message: $page")
                    Save-Screen 'message'
                }
                # Abort, else Cancel, else OK: the message is the failure, and
                # waiting on it only spends the timeout.
                $answers = @(foreach ($id in 3, 2, 1) {
                        $visible | Where-Object { $_.Id -eq $id -and $_.Class -eq 'Button' } | Select-Object -First 1
                    })
                if ($answers.Count) { [Win]::Press($answers[0].Handle) }
                Start-Sleep -Milliseconds 1500
                continue
            }
            & $OnPage $visible
            $default = $visible | Where-Object { $_.Id -eq 1 -and $_.Class -eq 'Button' -and $_.Enabled } | Select-Object -First 1
            if ($default) { [Win]::Press($default.Handle) }
            if ($lastPage -ne $page) { $lastPage = $page; $pageSince = Get-Date }
            elseif (((Get-Date) - $pageSince).TotalSeconds -gt 90 -and -not $seen.ContainsKey("stuck:$page")) {
                $seen["stuck:$page"] = $true
                Write-Host "    stuck on this page for 90 s; its controls:"
                $controls | ForEach-Object { Write-Host ("      {0,-5} {1,-18} visible={2} enabled={3} '{4}'" -f $_.Id, $_.Class, $_.Visible, $_.Enabled, $_.Text) }
                Save-Screen 'stuck'
            }
        }
        Start-Sleep -Milliseconds 1500
    }
}

# A picture of the screen for the workflow's artifacts, when something waits.
function Save-Screen([string]$name) {
    try {
        Add-Type -AssemblyName System.Windows.Forms, System.Drawing
        $b = [System.Windows.Forms.SystemInformation]::VirtualScreen
        $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $g.CopyFromScreen($b.Left, $b.Top, 0, 0, $bmp.Size)
        $base = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
        $dir = Join-Path $base 'upgrade-screens'
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        $path = Join-Path $dir "$Scenario-$name-$(Get-Date -Format HHmmss).png"
        $bmp.Save($path)
        Write-Host "    screen saved: $path"
    } catch {
        Write-Host "    no screen: $_"
    }
}

# The decision a release's updater makes, by its own code: pkg/updater's
# target() compiled from source and run from the directory in question, since
# os.Executable is part of what it reads.
function New-DecisionProbe([string]$Source, [string]$Out) {
    $test = Join-Path $Source 'pkg/updater/upgradedecision_test.go'
    Set-Content -Path $test -Encoding utf8NoBOM -Value @'
package updater

import (
	"fmt"
	"testing"
)

func TestUpgradeDecision(t *testing.T) {
	asset, mode := target()
	fmt.Printf("UPGRADE-DECISION %d %s\n", mode, asset)
}
'@
    Push-Location $Source
    try {
        go test -c -o $Out ./pkg/updater
        if ($LASTEXITCODE) { throw "building the decision probe from $Source failed" }
    } finally {
        Pop-Location
        Remove-Item $test -Force
    }
}

function Get-Decision([string]$Probe, [string]$Dir) {
    $copy = Join-Path $Dir 'upgrade-decision.test.exe'
    Copy-Item $Probe $copy -Force
    try {
        $out = & $copy '-test.run=^TestUpgradeDecision$' '-test.v' 2>&1 | Out-String
    } finally {
        Remove-Item $copy -Force
    }
    if ($out -notmatch 'UPGRADE-DECISION (\d) (\S+)') { throw "no decision from $Probe in ${Dir}:`n$out" }
    # The order of applyMode in pkg/updater, unchanged since the first release.
    $mode = @('replace itself', 'run the installer', 'open the browser')[[int]$Matches[1]]
    return [pscustomobject]@{ Mode = $mode; Asset = $Matches[2] }
}

function Get-ShortcutTarget([string]$lnk) {
    if (-not (Test-Path $lnk)) { return $null }
    return (New-Object -ComObject WScript.Shell).CreateShortcut($lnk).TargetPath
}

function Get-Sha256([string]$path) { (Get-FileHash -Algorithm SHA256 -Path $path).Hash }

# Waits for the window the finish page starts: a SnmpLens.exe in a desktop
# session that is not the service.
function Wait-Window([int[]]$except, [int]$seconds = 30) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        $p = Get-CimInstance Win32_Process -Filter "Name='SnmpLens.exe'" |
            Where-Object { $_.SessionId -ne 0 -and $except -notcontains [int]$_.ProcessId } |
            Select-Object -First 1
        if ($p) { return $p }
        Start-Sleep -Seconds 1
    }
    return $null
}

function Test-LaunchedLikeTheShell([int]$windowPid) {
    $shell = Get-Process -Name explorer -ErrorAction SilentlyContinue | Select-Object -First 1
    $app = [Win]::Elevated($windowPid)
    if ($null -eq $shell) {
        Note "no Explorer runs on this machine; the window's elevation ($app) is not compared"
        return
    }
    $expected = [Win]::Elevated($shell.Id)
    Check ($app -eq $expected) "the window runs at the shell's elevation (window $app, Explorer $expected)"
}

# ---------------------------------------------------------------------------

$work = Join-Path ([IO.Path]::GetTempPath()) "snmplens-upgrade-$Scenario"
Remove-Item $work -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $work | Out-Null
$old = Join-Path $work 'old'

Step 'The latest published release'
$tag = gh release view --repo $Repository --json tagName --jq .tagName
if ($LASTEXITCODE -or -not $tag) { throw 'cannot read the latest release' }
Write-Host "  $tag"
gh release download $tag --repo $Repository --dir $old --pattern 'SnmpLens-windows-amd64-setup.exe' `
    --pattern 'SnmpLens-windows-amd64.exe' --pattern 'SnmpLens-checksums.txt'
if ($LASTEXITCODE) { throw "cannot download $tag" }
$sums = Get-Content (Join-Path $old 'SnmpLens-checksums.txt')
foreach ($asset in 'SnmpLens-windows-amd64-setup.exe', 'SnmpLens-windows-amd64.exe') {
    $line = $sums | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+\*?$([regex]::Escape($asset))$" } | Select-Object -First 1
    $want = if ($line) { ($line -split '\s+')[0] } else { '' }
    Check ($want -ne '' -and $want -eq (Get-Sha256 (Join-Path $old $asset))) "$asset matches the release's checksums"
}
$oldSetup = Join-Path $old 'SnmpLens-windows-amd64-setup.exe'
$oldExe = Join-Path $old 'SnmpLens-windows-amd64.exe'

# The assets the old updater asks for are the ones the next release publishes.
$release = Get-Content -Raw (Join-Path $PSScriptRoot '..\..\.github\workflows\release.yml')
foreach ($asset in 'SnmpLens-windows-amd64-setup.exe', 'SnmpLens-windows-amd64.exe') {
    Check ($release.Contains($asset)) "the release workflow still publishes $asset"
}

$nextExe = Join-Path $Next 'SnmpLens.exe'
$nextHash = Get-Sha256 $nextExe
# The old updater downloads the installer to a temporary name ending in the asset's.
$nextSetup = Join-Path $work 'snmplens-update-1-SnmpLens-windows-amd64-setup.exe'
Copy-Item (Join-Path $Next 'SnmpLens-windows-amd64-setup.exe') $nextSetup

Step "Decision probes: $tag's updater and this branch's"
$probeOld = Join-Path $work 'decide-old.exe'
$probeNext = Join-Path $work 'decide-next.exe'
$tree = Join-Path $work 'tree'
git fetch --quiet --no-tags --depth 1 origin "refs/tags/${tag}:refs/tags/${tag}"
git worktree add --quiet --detach $tree $tag
if ($LASTEXITCODE) { throw "cannot check out $tag" }
New-DecisionProbe $tree $probeOld
New-DecisionProbe (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path $probeNext

$uac = Get-RegValue 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' 'EnableLUA'
Write-Host "  UAC (EnableLUA): $uac"

$dataDir = Join-Path $env:APPDATA 'SnmpLens'
$userDesktop = [Environment]::GetFolderPath('Desktop')
$publicDesktop = [Environment]::GetFolderPath('CommonDesktopDirectory')
$userPrograms = Join-Path ([Environment]::GetFolderPath('Programs')) 'SnmpLens'
$commonPrograms = Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'SnmpLens'

function Seed-Settings {
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    # Background mode with a tray: the old release's Quit then hides the window
    # instead of quitting, and the installer must still replace its file.
    Set-Content -Path (Join-Path $dataDir 'service.json') -Encoding utf8NoBOM -Value (@{
            runInBackground = $true; startHidden = $false; autoStartTrapListener = $false
            trapPort = 162; autoResumeMonitors = $false; auditFailedSets = $false
        } | ConvertTo-Json)
}

function Start-Old([string]$exe) {
    $p = Start-Process -FilePath $exe -PassThru
    Start-Sleep -Seconds 12
    $alive = $null -ne (Get-Process -Id $p.Id -ErrorAction SilentlyContinue)
    Check $alive "$tag runs from $exe, as it would when its updater starts the installer"
    return $p.Id
}

function Check-Desktop([string]$target) {
    $links = @(@($userDesktop, $publicDesktop) | ForEach-Object { Join-Path $_ 'SnmpLens.lnk' } | Where-Object { Test-Path $_ })
    Check ($links.Count -eq 1) "one SnmpLens shortcut on the desktop (found: $($links -join ', '))"
    foreach ($l in $links) { Check ((Get-ShortcutTarget $l) -eq $target) "$l starts $target" }
}

function Check-UserData {
    $cfg = Get-Content -Raw (Join-Path $dataDir 'service.json') -ErrorAction SilentlyContinue | ConvertFrom-Json
    Check ($null -ne $cfg -and $cfg.runInBackground) 'the user''s settings are still there'
}

switch ($Scenario) {

    'all-users-service' {
        Step "$tag installed for all users, start at sign-in on, running in background mode"
        Start-Process -FilePath $oldSetup -ArgumentList '/S', '/ALLUSERS' -Wait
        $dir = Join-Path ${env:ProgramFiles(x86)} 'SnmpLens'
        Check (Test-Path (Join-Path $dir 'SnmpLens.exe')) "$tag is in $dir"
        $where = if (Get-RegValue "HKLM:\$uninstall32" 'InstallLocation') { '32-bit view' } elseif (Get-RegValue "HKLM:\$uninstall" 'InstallLocation') { '64-bit view' } else { 'nowhere' }
        Write-Host "  $tag registered itself in the $where"
        # What the old installer writes when its box is ticked: machine-wide and unquoted.
        & reg.exe add "HKLM\$runKey" /v SnmpLens /t REG_SZ /d (Join-Path $dir 'SnmpLens.exe') /f /reg:32 | Out-Null
        Seed-Settings
        $d = Get-Decision $probeOld $dir
        Check ($d.Mode -eq 'run the installer' -and $d.Asset -eq 'SnmpLens-windows-amd64-setup.exe') "$tag's updater runs the installer here (it would: $($d.Mode), $($d.Asset))"
        $oldPid = Start-Old (Join-Path $dir 'SnmpLens.exe')

        Step 'The update, with the service ticked on the options page'
        Invoke-Installer -Path $nextSetup -Arguments @('/SERVICE')

        Step 'After'
        Check ($null -eq (Get-Process -Id $oldPid -ErrorAction SilentlyContinue)) "$tag's window was closed for the update"
        Check ((Get-Sha256 (Join-Path $dir 'SnmpLens.exe')) -eq $nextHash) "the copy in $dir is the new version"
        Check (-not (Test-Path (Join-Path $env:ProgramFiles 'SnmpLens'))) 'no second copy in the 64-bit Program Files'
        Check ((Get-RegValue "HKLM:\$uninstall" 'InstallLocation') -eq $dir) 'the all-users entry is in the 64-bit view'
        Check ((Get-RegValue "HKLM:\$uninstall" 'DisplayVersion') -eq $NextVersion) "it says $NextVersion"
        Check ($null -eq (Get-RegValue "HKLM:\$uninstall32" 'InstallLocation')) 'the 32-bit entry is gone'
        Check ($null -eq (Get-RegValue "HKLM:\$runKey" 'SnmpLens') -and $null -eq (Get-RegValue "HKLM:\$runKey32" 'SnmpLens')) 'no machine-wide start at sign-in'
        Check ((Get-RegValue "HKCU:\$runKey" 'SnmpLens') -eq "`"$(Join-Path $dir 'SnmpLens.exe')`"") 'start at sign-in is this user''s, quoted'
        Check-Desktop (Join-Path $dir 'SnmpLens.exe')
        Check-UserData

        $svc = Get-CimInstance Win32_Service -Filter "Name='SnmpLens'"
        Check ($null -ne $svc) 'the service is installed'
        if ($svc) {
            Check ($svc.PathName -like "*$(Join-Path $dir 'SnmpLens.exe')*service run*") "it runs this copy ($($svc.PathName))"
            Check ($svc.StartName -eq 'NT SERVICE\SnmpLens') "under its own account ($($svc.StartName))"
            Check ($svc.State -eq 'Running') "it runs ($($svc.State))"
            $rules = @(Get-NetFirewallApplicationFilter -Program (Join-Path $dir 'SnmpLens.exe') -ErrorAction SilentlyContinue)
            Check ($rules.Count -gt 0) 'the firewall lets notifications reach it'
        }
        $servicePid = if ($svc) { [int]$svc.ProcessId } else { -1 }
        $window = Wait-Window @($servicePid)
        Check ($null -ne $window) 'the finish page started the window'
        if ($window -and $svc) {
            Test-LaunchedLikeTheShell ([int]$window.ProcessId)
            Start-Sleep -Seconds 15
            $listeners = @(Get-NetUDPEndpoint -LocalPort 162 -ErrorAction SilentlyContinue | ForEach-Object OwningProcess | Sort-Object -Unique)
            Check ($listeners.Count -eq 1 -and $listeners[0] -eq $svc.ProcessId) "only the service listens for notifications (port 162: $($listeners -join ', '))"
        }
        $d = Get-Decision $probeNext $dir
        Check ($d.Mode -eq 'run the installer') "the next update runs the installer here (it would: $($d.Mode))"
    }

    { $_ -in 'for-me', 'for-me-to-all-users' } {
        Step "$tag installed for this user, start at sign-in on, running in background mode"
        Start-Process -FilePath $oldSetup -ArgumentList '/S' -Wait
        $dir = Join-Path $env:LOCALAPPDATA 'SnmpLens'
        Check (Test-Path (Join-Path $dir 'SnmpLens.exe')) "$tag is in $dir"
        & reg.exe add "HKCU\$runKey" /v SnmpLens /t REG_SZ /d (Join-Path $dir 'SnmpLens.exe') /f | Out-Null
        Seed-Settings
        $d = Get-Decision $probeOld $dir
        Check ($d.Mode -eq 'run the installer') "$tag's updater runs the installer here (it would: $($d.Mode))"
        $oldPid = Start-Old (Join-Path $dir 'SnmpLens.exe')

        if ($Scenario -eq 'for-me') {
            Step 'The update, as offered: for this user'
            Invoke-Installer -Path $nextSetup
            $newDir = $dir
        } else {
            Step 'The update, choosing all users on the mode page'
            $script:pickedAllUsers = $false
            Invoke-Installer -Path $nextSetup -OnPage {
                param($controls)
                $all = $controls | Where-Object { $_.Class -eq 'Button' -and $_.Text -like 'For all users*' } | Select-Object -First 1
                $me = $controls | Where-Object { $_.Class -eq 'Button' -and $_.Text -like 'For me only*' } | Select-Object -First 1
                if ($all -and $me -and -not [Win]::Checked($all.Handle)) {
                    [Win]::SetChecked($me.Handle, $false)
                    [Win]::SetChecked($all.Handle, $true)
                    $script:pickedAllUsers = $true
                }
            }
            Check $script:pickedAllUsers 'the mode page offered all users'
            $newDir = Join-Path $env:ProgramFiles 'SnmpLens'
        }

        Step 'After'
        $exe = Join-Path $newDir 'SnmpLens.exe'
        Check ($null -eq (Get-Process -Id $oldPid -ErrorAction SilentlyContinue)) "$tag's window was closed for the update"
        Check ((Test-Path $exe) -and (Get-Sha256 $exe) -eq $nextHash) "the copy in $newDir is the new version"
        Check ((Get-RegValue "HKCU:\$runKey" 'SnmpLens') -eq "`"$exe`"") 'start at sign-in is this user''s, quoted, and starts the new copy'
        Check ($null -eq (Get-RegValue "HKLM:\$runKey" 'SnmpLens') -and $null -eq (Get-RegValue "HKLM:\$runKey32" 'SnmpLens')) 'no machine-wide start at sign-in'
        Check-Desktop $exe
        Check-UserData
        if ($Scenario -eq 'for-me') {
            Check ((Get-RegValue "HKCU:\$uninstall" 'DisplayVersion') -eq $NextVersion) "this user's entry says $NextVersion"
            Check ($null -eq (Get-RegValue "HKLM:\$uninstall" 'InstallLocation') -and $null -eq (Get-RegValue "HKLM:\$uninstall32" 'InstallLocation')) 'nothing was installed for all users'
            Check ((Get-ShortcutTarget (Join-Path $userPrograms 'SnmpLens.lnk')) -eq $exe) 'the Start menu starts it'
        } else {
            Check (-not (Test-Path (Join-Path $dir 'SnmpLens.exe'))) "the copy in $dir is gone"
            Check ($null -eq (Get-RegValue "HKCU:\$uninstall" 'InstallLocation')) 'this user''s entry is gone'
            Check ((Get-RegValue "HKLM:\$uninstall" 'InstallLocation') -eq $newDir) 'the all-users entry is in the 64-bit view'
            Check ((Get-RegValue "HKLM:\$uninstall" 'DisplayVersion') -eq $NextVersion) "it says $NextVersion"
            Check (-not (Test-Path $userPrograms)) 'this user''s Start menu entries are gone'
            Check ((Get-ShortcutTarget (Join-Path $commonPrograms 'SnmpLens.lnk')) -eq $exe) 'the all-users Start menu starts the new copy'
        }
        $window = Wait-Window @()
        Check ($null -ne $window) 'the finish page started the window'
        if ($window) { Test-LaunchedLikeTheShell ([int]$window.ProcessId) }
        $d = Get-Decision $probeNext $newDir
        Check ($d.Mode -eq 'run the installer') "the next update runs the installer here (it would: $($d.Mode))"
    }

    'portable' {
        Step "$tag as a portable copy, alone"
        $dir = Join-Path $env:SystemDrive 'SnmpLensPortable'
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        Copy-Item $oldExe (Join-Path $dir 'SnmpLens.exe')
        $d = Get-Decision $probeOld $dir
        Check ($d.Mode -eq 'replace itself' -and $d.Asset -eq 'SnmpLens-windows-amd64.exe') "$tag's updater replaces itself here (it would: $($d.Mode), $($d.Asset))"
        # Its self-replace, as minio/selfupdate leaves it: the new file in place.
        Copy-Item $nextExe (Join-Path $dir 'SnmpLens.exe') -Force
        $d = Get-Decision $probeNext $dir
        Check ($d.Mode -eq 'replace itself') "the next update replaces it again (it would: $($d.Mode))"

        Step "Beside a copy of $tag installed for this user"
        Start-Process -FilePath $oldSetup -ArgumentList '/S' -Wait
        Copy-Item $oldExe (Join-Path $dir 'SnmpLens.exe') -Force
        $d = Get-Decision $probeOld $dir
        if ($d.Mode -ne 'replace itself') {
            Note "$tag's updater, run from a portable copy beside an installed one, would $($d.Mode): it updates the installed copy and leaves this one as it is"
        }
        Copy-Item $nextExe (Join-Path $dir 'SnmpLens.exe') -Force
        $d = Get-Decision $probeNext $dir
        Check ($d.Mode -eq 'replace itself') "from then on a portable copy beside an installed one replaces itself (it would: $($d.Mode))"
    }
}

git worktree remove --force $tree 2>$null

$summary = @("### Upgrade from $tag — $Scenario", '')
if ($failures.Count) { $summary += $failures | ForEach-Object { "- ❌ $_" } } else { $summary += '- ✅ everything checked holds' }
$summary += $notes | ForEach-Object { "- ℹ️ $_" }
if ($env:GITHUB_STEP_SUMMARY) { $summary -join "`n" | Add-Content -Path $env:GITHUB_STEP_SUMMARY -Encoding utf8 }

if ($failures.Count) {
    Write-Host "`n$($failures.Count) failure(s):" -ForegroundColor Red
    $failures | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
    exit 1
}
Write-Host "`nAll checks hold."
