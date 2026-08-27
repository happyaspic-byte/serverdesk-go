param([string]$HealthUrl)

# serverdesk Windows installer - run in an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File install-windows.ps1
# Steps: validate package/config -> install -> configure the actual listen port -> start + health-check.
# Licensed Stratus AVCLI/JRE/MIB artifacts are provisioned separately and are never unpacked here.
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$legacyRoot = 'C:\serverdesk'
$programDir = Join-Path $env:ProgramFiles 'Serverdesk'
$dataDir = Join-Path $env:ProgramData 'Serverdesk'
$dst = $programDir
$configPath = Join-Path $dataDir 'config.local.json'
$authPath = Join-Path $dataDir 'auth.json'
$initialLoginPath = Join-Path $dataDir 'initial-login.txt'
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$binarySource = "$src\serverdesk-windows-amd64.exe"
$commonSource = "$src\windows-deployment-common.ps1"
$installMarker = Join-Path $dataDir '.install-in-progress'
$destinationCreatedByRun = $false
$mayMigrateLegacyAcl = $false
foreach ($requiredAsset in @($binarySource, $commonSource, "$src\config.example.json",
    "$src\update.ps1", "$src\uninstall.ps1")) {
    if (-not (Test-Path -LiteralPath $requiredAsset -PathType Leaf)) {
        throw "Required package asset is missing: $requiredAsset"
    }
    $requiredItem = Get-Item -LiteralPath $requiredAsset -Force
    if (($requiredItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Required package asset must not be a reparse point: $requiredAsset"
    }
}
# The shared module executes in this elevated process. Validate every existing
# component before dot-sourcing it so a package directory junction cannot escape
# to attacker-controlled deployment code.
$bootstrapCursor = [IO.Path]::GetFullPath($commonSource)
while (-not [string]::IsNullOrWhiteSpace($bootstrapCursor)) {
    $bootstrapItem = Get-Item -LiteralPath $bootstrapCursor -Force
    if (($bootstrapItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Required package path must not traverse a reparse point: $($bootstrapItem.FullName)"
    }
    $bootstrapParent = Split-Path -Parent $bootstrapCursor
    if ([string]::IsNullOrWhiteSpace($bootstrapParent) -or $bootstrapParent -eq $bootstrapCursor) { break }
    $bootstrapCursor = $bootstrapParent
}
. $commonSource
Assert-ServerdeskAdministrator

foreach ($managedRoot in @($programDir, $dataDir)) {
    if (Test-Path -LiteralPath $managedRoot) {
        $managedItem = Get-Item -LiteralPath $managedRoot -Force
        if (-not $managedItem.PSIsContainer -or
            ($managedItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Managed root must be a regular directory: $managedRoot"
        }
    } else {
        New-Item -ItemType Directory -Path $managedRoot -Force | Out-Null
    }
}

# Legacy C:\serverdesk state is copied, never moved, until health succeeds.
if (Test-Path -LiteralPath $legacyRoot -PathType Container -and
    -not (Test-Path -LiteralPath (Join-Path $dataDir 'config.local.json'))) {
    foreach ($name in @('config.local.json', 'auth.json', 'initial-login.txt', 'data', 'credentials', 'run.log')) {
        $source = Join-Path $legacyRoot $name
        if (Test-Path -LiteralPath $source) {
            Copy-Item -LiteralPath $source -Destination (Join-Path $dataDir $name) -Recurse -Force
        }
    }
}

& icacls.exe $programDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)RX' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Failed to protect Program Files root: $programDir" }
& icacls.exe $dataDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Failed to protect ProgramData root: $dataDir" }

$binarySourceItem = Get-Item -LiteralPath $binarySource -Force
if (($binarySourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing reparse-point package binary: $binarySource"
}
$bundledVendorMib = $null
$packageMibDir = "$src\docs\mibs"
if (Test-Path -LiteralPath $packageMibDir) {
    $packageMibItem = Get-Item -LiteralPath $packageMibDir -Force
    if (($packageMibItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or -not $packageMibItem.PSIsContainer) {
        $bundledVendorMib = $packageMibItem
    } else {
        $bundledVendorMib = Get-ChildItem -LiteralPath $packageMibDir -Force -Recurse |
            Where-Object {
                $_.FullName -ine (Join-Path $packageMibDir 'README.md') -or
                ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
                $_.PSIsContainer
            } | Select-Object -First 1
    }
}
if ((Test-Path "$src\avcli.zip") -or (Test-Path "$src\jre.zip") -or ($null -ne $bundledVendorMib)) {
    throw 'Vendor AVCLI/JRE/MIB artifacts must not be bundled. Provision licensed dependencies separately.'
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
    Assert-ServerdeskManagedTask $legacyTask $dst
    $legacyActionRecognized = $null -ne $legacyTask
    $mayMigrateLegacyAcl = $legacyActionRecognized -and
        (Test-Path -LiteralPath "$dst\serverdesk.exe" -PathType Leaf) -and
        (Test-Path -LiteralPath $configPath -PathType Leaf)
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

$originalAclEntries = @()
$installTrackedBytes = @{}
$installTrackedExisted = @{}
$installCreatedDirectories = @()
$docsExistedBefore = Test-Path -LiteralPath "$dst\docs"
if (-not $destinationCreatedByRun) {
    $aclItems = @((Get-Item -LiteralPath $dst -Force))
    $aclItems += @(Get-ChildItem -LiteralPath $dst -Recurse -Force)
    foreach ($item in $aclItems) {
        $originalAclEntries += [PSCustomObject]@{ Path = $item.FullName; Acl = (Get-Acl -LiteralPath $item.FullName) }
    }
}
foreach ($name in @('update.ps1', 'uninstall.ps1', 'windows-deployment-common.ps1',
    'run-serverdesk.ps1', 'run-serverdesk.cmd', 'auth.json', 'initial-login.txt')) {
    $path = Join-Path $dst $name
    $present = Test-Path -LiteralPath $path -PathType Leaf
    $installTrackedExisted[$name] = $present
    if ($present) { $installTrackedBytes[$name] = [IO.File]::ReadAllBytes($path) }
}

function New-ServerdeskTrackedDirectory([string]$Path, [string]$InstallationRoot) {
    $fullPath = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $rootPath = [IO.Path]::GetFullPath($InstallationRoot).TrimEnd('\')
    if ($fullPath -eq $rootPath -or
        -not $fullPath.StartsWith(($rootPath + '\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to create an unmanaged directory: $fullPath"
    }
    if (Test-Path -LiteralPath $fullPath) {
        $existing = Get-Item -LiteralPath $fullPath -Force
        if (-not $existing.PSIsContainer -or
            ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Managed path must be a regular directory: $fullPath"
        }
        return @()
    }

    $missing = @()
    $cursor = $fullPath
    while (-not (Test-Path -LiteralPath $cursor)) {
        $missing += $cursor
        $cursor = Split-Path -Parent $cursor
        if ([string]::IsNullOrWhiteSpace($cursor) -or
            ($cursor -ine $rootPath -and
             -not $cursor.StartsWith(($rootPath + '\'), [StringComparison]::OrdinalIgnoreCase))) {
            throw "Managed directory escaped the installation root: $fullPath"
        }
    }
    $parentItem = Get-Item -LiteralPath $cursor -Force
    if (-not $parentItem.PSIsContainer -or
        ($parentItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Managed directory parent is unsafe: $cursor"
    }
    New-Item -ItemType Directory -Path $fullPath -Force | Out-Null
    return @($missing)
}

try {
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
    & icacls.exe "$dst\*" /setowner '*S-1-5-32-544' /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to set trusted child owners in $dst"
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

$freshInstall = -not (Test-Path -LiteralPath $configPath)
if (-not $freshInstall) {
    Assert-RegularNonReparseFile $configPath 'Existing config'
    $null = Get-Content $configPath -Raw | ConvertFrom-Json
    $preflightHealthUrl = if ($HealthUrl) { $HealthUrl } else { $env:SERVERDESK_HEALTH_URL }
    $preflightEndpoint = Get-ServerdeskEndpoint -ConfigPath $configPath -HealthUrl $preflightHealthUrl
    $preflightAllowDegraded = $env:SERVERDESK_ALLOW_DEGRADED_COLLECTION -eq '1'
    Test-ServerdeskAvcliPrerequisites -Endpoint $preflightEndpoint -ConfigPath $configPath -AllowDegraded:$preflightAllowDegraded
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
    if (Test-Path -LiteralPath $binaryBackup) {
        throw "Stale installation backup requires operator inspection before retrying: $binaryBackup"
    }
    Copy-Item "$dst\serverdesk.exe" $binaryBackup -Force
    if ((Get-FileHash -LiteralPath "$dst\serverdesk.exe" -Algorithm SHA256).Hash -cne
        (Get-FileHash -LiteralPath $binaryBackup -Algorithm SHA256).Hash) {
        throw 'Installed binary backup verification failed before service stop.'
    }
}
$hadExistingConfig = -not $freshInstall
$existingConfigBytes = if ($hadExistingConfig) { [IO.File]::ReadAllBytes($configPath) } else { $null }
$priorTask = Get-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
$null = Assert-ServerdeskManagedTask $priorTask $dst
$priorTaskXml = Export-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
$priorTaskWasRunning = $false
$priorTaskWasDisabled = $false
if ($null -ne $priorTask) {
    $priorTaskWasRunning = $priorTask.State -eq 'Running'
    $priorTaskWasDisabled = $priorTask.State -eq 'Disabled'
}
$priorFirewall = Get-ServerdeskFirewallSnapshot

try {
# Stop the scheduled task before replacing files so the watchdog cannot race installation.
Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
Stop-ServerdeskInstalledProcess "$dst\serverdesk.exe"
Start-Sleep -Seconds 2
Copy-Item $binarySource "$dst\serverdesk.exe" -Force
foreach ($name in @('update.ps1', 'uninstall.ps1', 'windows-deployment-common.ps1')) {
    if (Test-Path "$src\$name") {
        Copy-Item "$src\$name" "$dst\$name" -Force
    }
}
if ((Test-Path "$src\docs") -and -not (Test-Path "$dst\docs")) {
    Copy-Item "$src\docs" "$dst\docs" -Recurse -Force
} elseif (Test-Path "$dst\docs") {
    Write-Host '[INFO] keeping existing docs directory (customer/vendor files are never replaced on reinstall)'
}

if ($freshInstall) {
    Copy-Item "$src\config.example.json" $configPath
    Write-Host "[INFO] config.local.json created - edit device addresses and settings."
    Write-Host "       NOTE: save it as UTF-8 *without* BOM (VS Code default). Notepad UTF-8 BOM is rejected by the Go JSON parser."
} else {
    Write-Host "[INFO] keeping existing config.local.json"
}

$cfg = Get-Content $configPath -Raw | ConvertFrom-Json
$configChanged = $false
$runtimeValue = [string]$cfg.runtime_dir
if ([string]::IsNullOrWhiteSpace($runtimeValue)) {
    $runtimeValue = 'data'
    if ($cfg.PSObject.Properties['runtime_dir']) {
        $cfg.runtime_dir = $runtimeValue
    } else {
        $cfg | Add-Member -NotePropertyName runtime_dir -NotePropertyValue $runtimeValue
    }
    $configChanged = $true
    Write-Host "[INFO] runtime data path set to $runtimeValue"
}
$runtimeDir = if ([IO.Path]::IsPathRooted($runtimeValue)) {
    [IO.Path]::GetFullPath($runtimeValue).TrimEnd('\')
} else {
    [IO.Path]::GetFullPath((Join-Path $dataDir $runtimeValue)).TrimEnd('\')
}
$installRoot = [IO.Path]::GetFullPath($dataDir).TrimEnd('\')
if ($runtimeDir -eq $installRoot -or
    -not $runtimeDir.StartsWith(($installRoot + '\'), [StringComparison]::OrdinalIgnoreCase)) {
    throw "runtime_dir must be a child of the protected installation directory: $installRoot"
}

$legacyRemoved = $false
foreach ($name in @('sim_devices', 'sim_seed', '_sim_note')) {
    if ($cfg.PSObject.Properties[$name]) {
        $cfg.PSObject.Properties.Remove($name)
        $legacyRemoved = $true
        $configChanged = $true
    }
}
if ($configChanged) {
    [IO.File]::WriteAllText($configPath, (($cfg | ConvertTo-Json -Depth 20) + [Environment]::NewLine), $utf8NoBom)
}
if ($legacyRemoved) {
    Write-Host "[INFO] removed legacy simulation settings"
}
$installCreatedDirectories += @(New-ServerdeskTrackedDirectory $runtimeDir $dataDir)
$credentialDir = Join-Path $dataDir 'credentials'
$installCreatedDirectories += @(New-ServerdeskTrackedDirectory $credentialDir $dataDir)
& icacls.exe $credentialDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Failed to harden credential store ACLs: $credentialDir" }

$mibDir = "$dst\mibs"
$installCreatedDirectories += @(New-ServerdeskTrackedDirectory $mibDir $dst)
Write-Host "[INFO] Licensed MIB files may be provisioned separately in $mibDir; vendor MIBs are not bundled."

& icacls.exe "$dst\*" /setowner '*S-1-5-32-544' /T /C | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Failed to enforce trusted owners in $dst" }

$effectiveHealthUrl = if ($HealthUrl) { $HealthUrl } else { $env:SERVERDESK_HEALTH_URL }
$endpoint = Get-ServerdeskEndpoint -ConfigPath $configPath -HealthUrl $effectiveHealthUrl
$allowDegraded = $env:SERVERDESK_ALLOW_DEGRADED_COLLECTION -eq '1'
Test-ServerdeskAvcliPrerequisites -Endpoint $endpoint -ConfigPath $configPath -AllowDegraded:$allowDegraded
Set-ServerdeskFirewall -Endpoint $endpoint -Program "$dst\serverdesk.exe"
Write-ServerdeskRunner "$dst\run-serverdesk.ps1" $credentialDir
& icacls.exe "$dst\run-serverdesk.ps1" /setowner '*S-1-5-32-544' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Failed to set trusted owner on the runtime runner.' }
Register-ServerdeskTask $programDir $dataDir
Start-ScheduledTask -TaskName serverdesk
Start-Sleep -Seconds 6

    $code = Invoke-ServerdeskHealth $endpoint.HealthUrl
    if ($null -ne $priorTask -and -not $priorTaskWasRunning) {
        Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    }
    if ($priorTaskWasDisabled) { Disable-ScheduledTask -TaskName serverdesk | Out-Null }
    Remove-Item -LiteralPath $installMarker -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $binaryBackup -Force -ErrorAction SilentlyContinue
    if ($null -ne $priorTask -and -not $priorTaskWasRunning) {
        Write-Host "[OK] install/update passed transient health validation; prior task state ($($priorTask.State)) was restored."
    } elseif ($endpoint.Exposed) {
        Write-Host "[OK] serverdesk is up - $($endpoint.DisplayUrl) (health $code)"
    } else {
        Write-Host "[OK] serverdesk is up locally - $($endpoint.DisplayUrl) (health $code); edit listen/TLS for remote access."
    }
    Write-Host "     Update:    powershell -ExecutionPolicy Bypass -File C:\serverdesk\update.ps1 -Binary <new-exe>"
    Write-Host "     Uninstall: powershell -ExecutionPolicy Bypass -File C:\serverdesk\uninstall.ps1 -Full"
} catch {
    $installError = $_
    Write-Host '[FAIL] installation failed - restoring the previous service state'
    Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    Stop-ServerdeskInstalledProcess "$dst\serverdesk.exe"
    if ($hadExistingBinary -and (Test-Path -LiteralPath $binaryBackup -PathType Leaf)) {
        Copy-Item $binaryBackup "$dst\serverdesk.exe" -Force
        if ((Get-FileHash -LiteralPath "$dst\serverdesk.exe" -Algorithm SHA256).Hash -cne
            (Get-FileHash -LiteralPath $binaryBackup -Algorithm SHA256).Hash) {
            throw "Rollback binary verification failed; preserve $binaryBackup for operator recovery."
        }
    } elseif (-not $hadExistingBinary) {
        Remove-Item "$dst\serverdesk.exe" -Force -ErrorAction SilentlyContinue
    }
    if ($hadExistingConfig) {
        [IO.File]::WriteAllBytes($configPath, $existingConfigBytes)
    } else {
        Remove-Item $configPath -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $priorTaskXml) {
        Register-ScheduledTask -TaskName serverdesk -Xml $priorTaskXml -Force | Out-Null
        if ($priorTaskWasRunning) { Start-ScheduledTask -TaskName serverdesk }
    } else {
        Unregister-ScheduledTask -TaskName serverdesk -Confirm:$false -ErrorAction SilentlyContinue
    }
    Restore-ServerdeskFirewall $priorFirewall
    Remove-Item -LiteralPath $binaryBackup -Force -ErrorAction SilentlyContinue
    throw $installError
}
} catch {
    $outerError = $_
    $rollbackProblems = @()
    if ($destinationCreatedByRun) {
        try {
            Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
            Stop-ServerdeskInstalledProcess "$dst\serverdesk.exe"
            Unregister-ScheduledTask -TaskName serverdesk -Confirm:$false -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $dst) { Remove-Item -LiteralPath $dst -Recurse -Force }
        } catch { $rollbackProblems += $_.Exception.Message }
    } else {
        if (-not $docsExistedBefore) {
            try { Remove-Item -LiteralPath "$dst\docs" -Recurse -Force -ErrorAction SilentlyContinue }
            catch { $rollbackProblems += "remove newly copied docs: $($_.Exception.Message)" }
        }
        foreach ($name in $installTrackedExisted.Keys) {
            $path = Join-Path $dst $name
            try {
                if ($installTrackedExisted[$name]) {
                    [IO.File]::WriteAllBytes($path, [byte[]]$installTrackedBytes[$name])
                } else {
                    Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
                }
            } catch { $rollbackProblems += "restore ${name}: $($_.Exception.Message)" }
        }
        foreach ($entry in @($originalAclEntries | Sort-Object { $_.Path.Length } -Descending)) {
            try {
                if (Test-Path -LiteralPath $entry.Path) {
                    Set-Acl -LiteralPath $entry.Path -AclObject $entry.Acl
                }
            } catch { $rollbackProblems += "restore ACL $($entry.Path): $($_.Exception.Message)" }
        }
        foreach ($directory in @($installCreatedDirectories | Sort-Object Length -Descending -Unique)) {
            try {
                if (Test-Path -LiteralPath $directory) {
                    $directoryItem = Get-Item -LiteralPath $directory -Force
                    if (-not $directoryItem.PSIsContainer -or
                        ($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                        throw 'path is no longer a regular directory'
                    }
                    if ($null -ne (Get-ChildItem -LiteralPath $directory -Force | Select-Object -First 1)) {
                        throw 'new transaction directory is not empty; preserving it for inspection'
                    }
                    Remove-Item -LiteralPath $directory -Force
                }
            } catch { $rollbackProblems += "remove new directory ${directory}: $($_.Exception.Message)" }
        }
    }
    if ($rollbackProblems.Count -gt 0) {
        throw "Installation failed and local file/ACL rollback was incomplete: $($rollbackProblems -join '; '). Original: $($outerError.Exception.Message)"
    }
    throw $outerError
}
