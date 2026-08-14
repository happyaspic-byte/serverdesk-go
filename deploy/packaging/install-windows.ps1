# serverdesk Windows installer - run in an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File install-windows.ps1
# Steps: create C:\serverdesk -> copy exe/config -> allow firewall 6005 -> register + start Scheduled Task.
# Optional: if avcli.zip + jre.zip sit next to this script, Stratus collection (avcli) is installed too.
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$dst = 'C:\serverdesk'
$authPath = "$dst\auth.json"
$initialLoginPath = "$dst\initial-login.txt"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$binarySource = "$src\serverdesk-windows-amd64.exe"
$installMarker = "$dst\.install-in-progress"
$destinationCreatedByRun = $false
$mayMigrateLegacyAcl = $false
if (-not (Test-Path -LiteralPath $binarySource -PathType Leaf)) {
    throw "Required package binary is missing: $binarySource"
}
$binarySourceItem = Get-Item -LiteralPath $binarySource -Force
if (($binarySourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing reparse-point package binary: $binarySource"
}
if (-not (Test-Path -LiteralPath "$src\config.example.json" -PathType Leaf)) {
    throw 'Required config.example.json is missing.'
}

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'This installer must be run from an elevated Administrator PowerShell session.'
}

if (Test-Path -LiteralPath $dst) {
    $destination = Get-Item -LiteralPath $dst -Force
    if (($destination.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing reparse-point installation destination: $dst"
    }
    if (-not (Test-Path -LiteralPath "$dst\serverdesk.exe" -PathType Leaf) -and
        -not (Test-Path -LiteralPath $installMarker -PathType Leaf)) {
        throw "Refusing unrecognized pre-existing installation destination: $dst"
    }
    $legacyTask = Get-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    $legacyActionRecognized = $false
    if ($null -ne $legacyTask -and
        [string]$legacyTask.Principal.UserId -in @('SYSTEM', 'S-1-5-18')) {
        foreach ($action in @($legacyTask.Actions)) {
            $executeName = [IO.Path]::GetFileName([string]$action.Execute)
            $arguments = ([string]$action.Arguments).Trim()
            $workingDirectory = ([string]$action.WorkingDirectory).TrimEnd('\')
            $currentAction = $executeName -ieq 'cmd.exe' -and
                $workingDirectory -ieq $dst -and
                $arguments -ieq '/d /c run-serverdesk.cmd'
            $legacyAction = $executeName -in @('cmd', 'cmd.exe') -and
                $arguments -ieq '/c C:\serverdesk\serverdesk.exe -c C:\serverdesk\config.local.json >> C:\serverdesk\run.log 2>&1'
            if ($currentAction -or $legacyAction) {
                $legacyActionRecognized = $true
            }
        }
    }
    $mayMigrateLegacyAcl = $legacyActionRecognized -and
        (Test-Path -LiteralPath "$dst\serverdesk.exe" -PathType Leaf) -and
        (Test-Path -LiteralPath "$dst\config.local.json" -PathType Leaf)
    $destinationAcl = Get-Acl -LiteralPath $dst
    $ownerAccount = New-Object -TypeName Security.Principal.NTAccount -ArgumentList $destinationAcl.Owner
    $ownerSid = $ownerAccount.Translate([Security.Principal.SecurityIdentifier]).Value
    if ($ownerSid -notin @('S-1-5-18', 'S-1-5-32-544') -and -not $mayMigrateLegacyAcl) {
        throw "Refusing installation destination with untrusted owner: $ownerSid"
    }
    foreach ($entry in @($destinationAcl.Access)) {
        $entrySid = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($entrySid -notin @('S-1-5-18', 'S-1-5-32-544') -and -not $mayMigrateLegacyAcl) {
            throw "Refusing installation destination with untrusted ACL entry: $entrySid"
        }
    }
    $reparseChild = Get-ChildItem -LiteralPath $dst -Recurse -Force |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $reparseChild) {
        throw "Refusing child reparse point: $($reparseChild.FullName)"
    }
} else {
    New-Item -ItemType Directory -Path $dst | Out-Null
    $destinationCreatedByRun = $true
}

& icacls.exe $dst /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to harden ACLs on $dst"
}
& icacls.exe $dst /setowner '*S-1-5-32-544' | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Failed to set trusted owner on $dst"
}
$hardenedAcl = Get-Acl -LiteralPath $dst
$hardenedOwner = New-Object -TypeName Security.Principal.NTAccount -ArgumentList $hardenedAcl.Owner
if ($hardenedOwner.Translate([Security.Principal.SecurityIdentifier]).Value -ne 'S-1-5-32-544') {
    throw "Failed to verify trusted owner on $dst"
}
if ($destinationCreatedByRun) {
    [IO.File]::WriteAllText($installMarker, "serverdesk installer transaction`r`n", [Text.Encoding]::ASCII)
}
if ($null -ne (Get-ChildItem -LiteralPath $dst -Force | Select-Object -First 1)) {
    & icacls.exe "$dst\*" /reset /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to reset child ACLs in $dst"
    }
}
function Assert-RegularNonReparseFile([string]$Path, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description must be a regular file: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Description must not be a reparse point: $Path"
    }
}

$freshInstall = -not (Test-Path -LiteralPath "$dst\config.local.json")
if (-not $freshInstall) {
    Assert-RegularNonReparseFile "$dst\config.local.json" 'Existing config'
    $null = Get-Content "$dst\config.local.json" -Raw | ConvertFrom-Json
}

if (Test-Path -LiteralPath $authPath) {
    Assert-RegularNonReparseFile $authPath 'Existing auth store'
    & $binarySource -auth $authPath -check-auth | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Existing auth store failed strict validation: $authPath"
    }
} elseif (Test-Path -LiteralPath $initialLoginPath) {
    throw "Refusing stale initial credential file without auth store: $initialLoginPath"
}
if (-not (Test-Path -LiteralPath $authPath)) {
    try {
        $authOutput = & $binarySource -auth $authPath -init-auth
        if ($LASTEXITCODE -ne 0) {
            throw 'Credential initialization failed.'
        }

        $credentials = @{}
        foreach ($line in $authOutput) {
            if ($line -match '^(ADMIN_USERNAME|ADMIN_PASSWORD)=(.+)$') {
                if ($credentials.ContainsKey($matches[1])) {
                    throw 'Credential initialization returned duplicate fields.'
                }
                $credentials[$matches[1]] = $matches[2]
            } else {
                throw 'Credential initialization returned unexpected output.'
            }
        }
        if (-not $credentials.ContainsKey('ADMIN_USERNAME') -or -not $credentials.ContainsKey('ADMIN_PASSWORD')) {
            throw 'Credential initialization did not return required fields.'
        }
        [IO.File]::WriteAllText($initialLoginPath, ("ADMIN_USERNAME={0}`r`nADMIN_PASSWORD={1}`r`n" -f $credentials.ADMIN_USERNAME, $credentials.ADMIN_PASSWORD), $utf8NoBom)
    } catch {
        Remove-Item -LiteralPath $authPath -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $initialLoginPath -Force -ErrorAction SilentlyContinue
        throw
    }
    Write-Host "[INFO] Initial web login credentials: $initialLoginPath"
} else {
    Write-Host '[INFO] keeping existing auth.json'
}

$hadExistingBinary = Test-Path -LiteralPath "$dst\serverdesk.exe" -PathType Leaf
$binaryBackup = "$dst\serverdesk.exe.install-backup"
if ($hadExistingBinary) {
    Copy-Item "$dst\serverdesk.exe" $binaryBackup -Force
}
$hadExistingConfig = -not $freshInstall
$existingConfigBytes = if ($hadExistingConfig) { [IO.File]::ReadAllBytes("$dst\config.local.json") } else { $null }
$priorTaskXml = Export-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue

try {
# Stop the scheduled task before replacing files so the watchdog cannot race installation.
Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
Copy-Item $binarySource "$dst\serverdesk.exe" -Force
foreach ($name in @('update.ps1', 'uninstall.ps1')) {
    if (Test-Path "$src\$name") {
        Copy-Item "$src\$name" "$dst\$name" -Force
    }
}
if (Test-Path "$src\docs") {
    Remove-Item "$dst\docs" -Recurse -Force -ErrorAction SilentlyContinue
    Copy-Item "$src\docs" "$dst\docs" -Recurse -Force
}

if ($freshInstall) {
    Copy-Item "$src\config.example.json" "$dst\config.local.json"
    Write-Host "[INFO] config.local.json created - edit device addresses and settings."
    Write-Host "       NOTE: save it as UTF-8 *without* BOM (VS Code default). Notepad UTF-8 BOM is rejected by the Go JSON parser."
} else {
    Write-Host "[INFO] keeping existing config.local.json"
}

$cfg = Get-Content "$dst\config.local.json" -Raw | ConvertFrom-Json
$configChanged = $false
$runtimeDir = [string]$cfg.runtime_dir
if ([string]::IsNullOrWhiteSpace($runtimeDir) -or -not [IO.Path]::IsPathRooted($runtimeDir)) {
    $runtimeDir = "$dst\data"
    if ($cfg.PSObject.Properties['runtime_dir']) {
        $cfg.runtime_dir = $runtimeDir
    } else {
        $cfg | Add-Member -NotePropertyName runtime_dir -NotePropertyValue $runtimeDir
    }
    $configChanged = $true
    Write-Host "[INFO] runtime data path set to $runtimeDir"
}

$legacyRemoved = $false
foreach ($name in @('sim_devices', 'sim_seed', '_sim_note')) {
    if ($cfg.PSObject.Properties[$name]) {
        $cfg.PSObject.Properties.Remove($name)
        $legacyRemoved = $true
        $configChanged = $true
    }
}
$hasBundledAvcli = (Test-Path "$src\avcli.zip") -and (Test-Path "$src\jre.zip")
if ($mayMigrateLegacyAcl -and -not $hasBundledAvcli) {
    $disabledAvcli = "$dst\avcli-disabled.exe"
    Remove-Item -LiteralPath $disabledAvcli -Force -ErrorAction SilentlyContinue
    if ($cfg.PSObject.Properties['avcli_bin']) {
        $cfg.avcli_bin = $disabledAvcli
    } else {
        $cfg | Add-Member -NotePropertyName avcli_bin -NotePropertyValue $disabledAvcli
    }
    $emptyArgs = [string[]]@()
    if ($cfg.PSObject.Properties['avcli_args']) {
        $cfg.avcli_args = $emptyArgs
    } else {
        $cfg | Add-Member -NotePropertyName avcli_args -NotePropertyValue $emptyArgs
    }
    $configChanged = $true
    Write-Host '[WARN] legacy ACL migrated without bundled avcli; collection is disabled until the full package is installed.'
}
if ($configChanged) {
    [IO.File]::WriteAllText("$dst\config.local.json", (($cfg | ConvertTo-Json -Depth 20) + [Environment]::NewLine), $utf8NoBom)
}
if ($legacyRemoved) {
    Write-Host "[INFO] removed legacy simulation settings"
}
New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null

# optional Stratus collection: avcli.zip (avcli.jar set) + jre.zip (Windows JRE)
if ($hasBundledAvcli) {
    Expand-Archive "$src\avcli.zip" -DestinationPath "$dst" -Force
    Expand-Archive "$src\jre.zip" -DestinationPath "$dst\jre" -Force
    $jre = (Get-ChildItem "$dst\jre" -Directory | Select-Object -First 1).FullName
    $java = "$jre\bin\java.exe"
    if (-not (Test-Path -LiteralPath $java)) {
        throw "Bundled Java executable not found: $java"
    }
    $cfg = Get-Content "$dst\config.local.json" -Raw | ConvertFrom-Json
    if ($cfg.PSObject.Properties['avcli_bin']) {
        $cfg.avcli_bin = $java
    } else {
        $cfg | Add-Member -NotePropertyName avcli_bin -NotePropertyValue $java
    }
    $avcliArgs = @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', "$dst\avcli\avcli.jar")
    if ($cfg.PSObject.Properties['avcli_args']) {
        $cfg.avcli_args = $avcliArgs
    } else {
        $cfg | Add-Member -NotePropertyName avcli_args -NotePropertyValue $avcliArgs
    }
    [IO.File]::WriteAllText("$dst\config.local.json", (($cfg | ConvertTo-Json -Depth 20) + [Environment]::NewLine), $utf8NoBom)
    Remove-Item "$dst\avcli\avcli.bat" -Force -ErrorAction SilentlyContinue
    Write-Host "[INFO] avcli + JRE installed - Stratus collection enabled"
}

Get-NetFirewallRule -DisplayName 'serverdesk 6005' -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName 'serverdesk 6005' -Direction Inbound -Program "$dst\serverdesk.exe" -Protocol TCP -LocalPort 6005 -RemoteAddress LocalSubnet -Profile Domain,Private -Action Allow | Out-Null
$runner = @'
@echo off
cd /d C:\serverdesk
:restart
serverdesk.exe -c config.local.json -auth auth.json >> run.log 2>&1
set "SERVERDESK_EXIT=%ERRORLEVEL%"
>> run.log echo [%date% %time%] serverdesk exited with code %SERVERDESK_EXIT%; restarting in 5 seconds
timeout /t 5 /nobreak >nul
goto restart
'@
[IO.File]::WriteAllText("$dst\run-serverdesk.cmd", ($runner + [Environment]::NewLine), [Text.Encoding]::ASCII)

$taskAction = New-ScheduledTaskAction `
    -Execute $env:ComSpec `
    -Argument '/d /c run-serverdesk.cmd' `
    -WorkingDirectory $dst
$taskTrigger = New-ScheduledTaskTrigger -AtStartup
$taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$taskSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName serverdesk -Action $taskAction -Trigger $taskTrigger `
    -Principal $taskPrincipal -Settings $taskSettings -Force | Out-Null
Start-ScheduledTask -TaskName serverdesk
Start-Sleep -Seconds 6

    $code = (Invoke-WebRequest http://127.0.0.1:6005/api/health -UseBasicParsing -TimeoutSec 8).StatusCode
    Remove-Item -LiteralPath $installMarker -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $binaryBackup -Force -ErrorAction SilentlyContinue
    Write-Host "[OK] serverdesk is up - http://<this-server-ip>:6005  (health $code)"
    Write-Host "     Update:    powershell -ExecutionPolicy Bypass -File C:\serverdesk\update.ps1 -Binary <new-exe>"
    Write-Host "     Uninstall: powershell -ExecutionPolicy Bypass -File C:\serverdesk\uninstall.ps1 -Full"
} catch {
    $installError = $_
    Write-Host '[FAIL] installation failed - restoring the previous service state'
    Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    if ($hadExistingBinary -and (Test-Path -LiteralPath $binaryBackup -PathType Leaf)) {
        Copy-Item $binaryBackup "$dst\serverdesk.exe" -Force
    } elseif (-not $hadExistingBinary) {
        Remove-Item "$dst\serverdesk.exe" -Force -ErrorAction SilentlyContinue
    }
    if ($hadExistingConfig) {
        [IO.File]::WriteAllBytes("$dst\config.local.json", $existingConfigBytes)
    } else {
        Remove-Item "$dst\config.local.json" -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $priorTaskXml) {
        Register-ScheduledTask -TaskName serverdesk -Xml $priorTaskXml -Force | Out-Null
        Start-ScheduledTask -TaskName serverdesk
    } else {
        Unregister-ScheduledTask -TaskName serverdesk -Confirm:$false -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $binaryBackup -Force -ErrorAction SilentlyContinue
    throw $installError
}