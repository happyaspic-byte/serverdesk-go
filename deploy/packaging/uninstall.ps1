param([switch]$Full)

# serverdesk Windows uninstaller. Default preserves customer state and diagnostics.
$ErrorActionPreference = 'Stop'
$programDir = Join-Path $env:ProgramFiles 'Serverdesk'
$dataDir = Join-Path $env:ProgramData 'Serverdesk'
$legacyRoot = 'C:\serverdesk'
$dst = $programDir
$commonPath = Join-Path $programDir 'windows-deployment-common.ps1'
if (Test-Path -LiteralPath $commonPath -PathType Leaf) {
    $cursor = [IO.Path]::GetFullPath($commonPath)
    while (-not [string]::IsNullOrWhiteSpace($cursor)) {
        $component = Get-Item -LiteralPath $cursor -Force
        if (($component.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing to load deployment code through a reparse point: $($component.FullName)"
        }
        $parent = Split-Path -Parent $cursor
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent
    }
    . $commonPath
    Assert-ServerdeskAdministrator
} else {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'This operation requires an elevated Administrator PowerShell session.'
    }
}

foreach ($transactionPath in @("$dst\.update-transaction", "$dst\.install-in-progress",
    "$dst\serverdesk.exe.install-backup", "$dst\.serverdesk.update-new",
    "$dst\serverdesk.exe.bak.new", "$dst\config.local.json.update.tmp",
    "$dst\update.ps1.update-new", "$dst\uninstall.ps1.update-new",
    "$dst\windows-deployment-common.ps1.update-new")) {
    if (Test-Path -LiteralPath $transactionPath) {
        throw "A deployment transaction/recovery path is present. Inspect it before uninstalling: $transactionPath"
    }
}
if ($Full -and (Test-Path -LiteralPath $dst)) {
    $destinationItem = Get-Item -LiteralPath $dst -Force
    if (($destinationItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Full uninstall refuses a reparse-point installation root: $dst"
    }
    $reparseChild = Get-ChildItem -LiteralPath $dst -Recurse -Force |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $reparseChild) {
        throw "Full uninstall refuses to traverse customer/reparse path: $($reparseChild.FullName)"
    }
}

$installedTask = Get-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
if (Get-Command Assert-ServerdeskManagedTask -ErrorAction SilentlyContinue) {
    Assert-ServerdeskManagedTask $installedTask $dst
} elseif ($null -ne $installedTask) {
    $actions = @($installedTask.Actions)
    $principal = [string]$installedTask.Principal.UserId
    if ($principal -notin @('SYSTEM', 'S-1-5-18', 'NT AUTHORITY\SYSTEM',
        'LOCAL SERVICE', 'S-1-5-19', 'NT AUTHORITY\LOCAL SERVICE') -or $actions.Count -ne 1) {
        throw 'Refusing to remove an unrecognized serverdesk scheduled task.'
    }
    $action = $actions[0]
    $executeName = [IO.Path]::GetFileName([string]$action.Execute)
    $arguments = ([string]$action.Arguments).Trim()
    $workingDirectory = ([string]$action.WorkingDirectory).TrimEnd('\')
    $knownAction = ($executeName -ieq 'powershell.exe' -and $workingDirectory -ieq $dst -and
            $arguments -ieq '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File C:\serverdesk\run-serverdesk.ps1') -or
        ($executeName -ieq 'cmd.exe' -and $workingDirectory -ieq $dst -and
            $arguments -ieq '/d /c run-serverdesk.cmd') -or
        ($executeName -in @('cmd', 'cmd.exe') -and
            $arguments -ieq '/c C:\serverdesk\serverdesk.exe -c C:\serverdesk\config.local.json >> C:\serverdesk\run.log 2>&1')
    if (-not $knownAction) { throw 'Refusing to remove an unrecognized serverdesk scheduled task.' }
}

Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
if (Get-Command Stop-ServerdeskInstalledProcess -ErrorAction SilentlyContinue) {
    Stop-ServerdeskInstalledProcess "$dst\serverdesk.exe"
} else {
    foreach ($process in @(Get-Process -Name serverdesk -ErrorAction SilentlyContinue)) {
        $actual = ''
        try { $actual = [IO.Path]::GetFullPath([string]$process.Path) } catch { continue }
        if ($actual -ieq [IO.Path]::GetFullPath("$dst\serverdesk.exe")) {
            Stop-Process -Id $process.Id -Force -ErrorAction Stop
            Wait-Process -Id $process.Id -Timeout 10 -ErrorAction SilentlyContinue
        }
    }
}
Start-Sleep -Seconds 2
Unregister-ScheduledTask -TaskName serverdesk -Confirm:$false -ErrorAction SilentlyContinue
if ($null -ne (Get-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue)) {
    throw 'Scheduled task removal could not be verified.'
}

if (Get-Command Remove-ServerdeskManagedFirewall -ErrorAction SilentlyContinue) {
    Remove-ServerdeskManagedFirewall
} else {
    Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq 'Serverdesk-Managed-Inbound' -or $_.DisplayName -eq 'serverdesk 6005'
    } | Remove-NetFirewallRule -ErrorAction Stop
}
$remainingRules = @(Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object {
    $_.Name -eq 'Serverdesk-Managed-Inbound' -or $_.DisplayName -eq 'serverdesk 6005'
})
if ($remainingRules.Count -ne 0) { throw 'Managed firewall-rule removal could not be verified.' }

if ($Full) {
    Set-Location $env:TEMP
    if (Test-Path -LiteralPath $dst) { Remove-Item -LiteralPath $dst -Recurse -Force }
    if (Test-Path -LiteralPath $dataDir) { Remove-Item -LiteralPath $dataDir -Recurse -Force }
    if (Test-Path -LiteralPath $legacyRoot) { Remove-Item -LiteralPath $legacyRoot -Recurse -Force }
    foreach ($setup in @('C:\serverdesk-setup.exe', 'C:\serverdesk-setup.download')) {
        if (Test-Path -LiteralPath $setup) { Remove-Item -LiteralPath $setup -Force }
    }
    if (Test-Path -LiteralPath $dst) { throw "Complete removal failed: $dst still exists." }
    Write-Host '[OK] Removed task, managed firewall rule, program, configuration, credentials, data, and logs.'
} else {
    if (Test-Path -LiteralPath $dst) {
        # Delete only known package-owned runtime files. Unknown/customer-provisioned
        # AVCLI, JRE, MIB, TLS, credential, config, data, and diagnostic files stay.
        foreach ($name in @('serverdesk.exe', 'serverdesk.exe.bak', 'serverdesk.exe.bak.new',
            '.serverdesk.update-new', 'run-serverdesk.ps1', 'run-serverdesk.cmd')) {
            $path = Join-Path $dst $name
            if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
        }
    }
    Write-Host '[OK] Removed runtime/task/firewall; preserved config, auth, credentials, data, logs, TLS, MIB, AVCLI/JRE, and maintenance scripts.'
    Write-Host '     Use -Full only after backing up customer state to remove everything.'
}
