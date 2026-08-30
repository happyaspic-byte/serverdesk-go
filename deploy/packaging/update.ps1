param([string]$Binary, [string]$HealthUrl)

# Transactional serverdesk Windows updater (PowerShell 5.1).
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$programDir = Join-Path $env:ProgramFiles 'Serverdesk'
$dataDir = Join-Path $env:ProgramData 'Serverdesk'
$dst = $programDir
$exe = Join-Path $programDir 'serverdesk.exe'
$configPath = Join-Path $dataDir 'config.local.json'
$authPath = Join-Path $dataDir 'auth.json'
$transaction = Join-Path $dataDir '.update-transaction'
$new = if ($Binary) {
    $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Binary)
} else {
    "$src\serverdesk-windows-amd64.exe"
}
$commonPath = "$src\windows-deployment-common.ps1"
foreach ($requiredAsset in @($commonPath, "$src\uninstall.ps1")) {
    if (-not (Test-Path -LiteralPath $requiredAsset -PathType Leaf)) {
        throw "Required update package asset is missing: $requiredAsset"
    }
    $requiredItem = Get-Item -LiteralPath $requiredAsset -Force
    if (($requiredItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Required update package asset must not be a reparse point: $requiredAsset"
    }
}
# Refuse to execute the elevated shared module through a package-directory
# junction/reparse component.
$bootstrapCursor = [IO.Path]::GetFullPath($commonPath)
while (-not [string]::IsNullOrWhiteSpace($bootstrapCursor)) {
    $bootstrapItem = Get-Item -LiteralPath $bootstrapCursor -Force
    if (($bootstrapItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Required update package path must not traverse a reparse point: $($bootstrapItem.FullName)"
    }
    $bootstrapParent = Split-Path -Parent $bootstrapCursor
    if ([string]::IsNullOrWhiteSpace($bootstrapParent) -or $bootstrapParent -eq $bootstrapCursor) { break }
    $bootstrapCursor = $bootstrapParent
}
. $commonPath
Assert-ServerdeskAdministrator

if (-not (Test-Path -LiteralPath $new -PathType Leaf)) {
    throw "New serverdesk executable not found: $new (pass -Binary <path> or run from a release package)."
}
Assert-ServerdeskRegularFile $new 'New serverdesk executable'
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
    if (-not (Test-Path -LiteralPath "$src\install-windows.ps1" -PathType Leaf)) {
        throw 'Serverdesk is not installed and the full installer is unavailable.'
    }
    & "$src\install-windows.ps1" -HealthUrl $HealthUrl
    # PowerShell scripts report installer failures by throwing; a successful
    # script invocation does not reliably reset the prior native exit status.
    exit 0
}
Assert-ServerdeskRegularFile $exe 'Installed serverdesk executable'
Assert-ServerdeskRegularFile $configPath 'Installed config'
Assert-ServerdeskRegularFile $authPath 'Installed auth store'
Assert-ServerdeskManagedWritablePath $configPath 'Installed config'
Assert-ServerdeskManagedWritablePath $authPath 'Auth store'
$initialLoginPath = Join-Path $dataDir 'initial-login.txt'
if (Test-Path -LiteralPath $initialLoginPath -PathType Leaf) {
    Assert-ServerdeskManagedWritablePath $initialLoginPath 'Initial-login file'
}
$destinationItem = Get-Item -LiteralPath $dst -Force
if (-not $destinationItem.PSIsContainer -or
    ($destinationItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Installation directory must be a regular non-reparse directory: $dst"
}
$reparseChild = Get-ChildItem -LiteralPath $dst -Recurse -Force |
    Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
    Select-Object -First 1
if ($null -ne $reparseChild) {
    throw "Installation directory contains a reparse point: $($reparseChild.FullName)"
}
Assert-ServerdeskTrustedReadOnlyPath $dst 'Installation directory'
foreach ($trustedInstalledPath in @($exe, "$dst\run-serverdesk.ps1", "$dst\run-serverdesk.cmd",
    "$dst\update.ps1", "$dst\uninstall.ps1", "$dst\windows-deployment-common.ps1")) {
    if (Test-Path -LiteralPath $trustedInstalledPath -PathType Leaf) {
        Assert-ServerdeskTrustedReadOnlyPath $trustedInstalledPath 'Installed package/control file'
    }
}
$credentialDir = Join-Path $dataDir 'credentials'
$credentialDirectoryExisted = Test-Path -LiteralPath $credentialDir
if ($credentialDirectoryExisted) {
    $credentialItem = Get-Item -LiteralPath $credentialDir -Force
    if (-not $credentialItem.PSIsContainer -or
        ($credentialItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Credential store must be a regular non-reparse directory: $credentialDir"
    }
    Assert-ServerdeskManagedWritablePath $credentialDir 'Managed credential store'
    foreach ($credentialItemPath in @(Get-ChildItem -LiteralPath $credentialDir -Recurse -Force |
        ForEach-Object { $_.FullName })) {
        Assert-ServerdeskManagedWritablePath $credentialItemPath 'Managed credential entry'
    }
}
& $new -auth $authPath -check-auth | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'The new binary failed auth-store compatibility validation.' }

$effectiveHealthUrl = if ($HealthUrl) { $HealthUrl } else { $env:SERVERDESK_HEALTH_URL }
$endpoint = Get-ServerdeskEndpoint -ConfigPath $configPath -HealthUrl $effectiveHealthUrl
if (-not (Test-Path -LiteralPath $endpoint.RuntimePath -PathType Container)) {
    throw "Managed runtime directory is missing; run the full installer before updating: $($endpoint.RuntimePath)"
}
Assert-ServerdeskPathComponents $endpoint.RuntimePath
Assert-ServerdeskManagedWritablePath $endpoint.RuntimePath 'Managed runtime directory'
$allowDegraded = $env:SERVERDESK_ALLOW_DEGRADED_COLLECTION -eq '1'
$preflightAvcliBin = [string](Get-ServerdeskProperty $endpoint.Config 'avcli_bin' '')
if ([IO.Path]::GetExtension($preflightAvcliBin).ToLowerInvariant() -in @('.bat', '.cmd')) {
    $expectedLegacy = [IO.Path]::GetFullPath("$dst\avcli\avcli.bat")
    if ((Resolve-ServerdeskConfigPath $preflightAvcliBin $configPath) -ine $expectedLegacy) {
        throw "Refusing unrecognized AVCLI batch launcher: $preflightAvcliBin"
    }
    $preflightJre = Get-ChildItem "$dst\jre" -Directory -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -First 1
    if ($null -eq $preflightJre) { throw 'Legacy AVCLI migration needs the separately licensed installed JRE.' }
    $preflightJava = "$($preflightJre.FullName)\bin\java.exe"
    $preflightJar = "$dst\avcli\avcli.jar"
    Assert-ServerdeskRegularFile $preflightJava 'Installed Java executable'
    Assert-ServerdeskRegularFile $preflightJar 'Installed AVCLI JAR'
    $endpoint.Config.avcli_bin = $preflightJava
    if ($null -ne $endpoint.Config.PSObject.Properties['avcli_args']) {
        $endpoint.Config.avcli_args = @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', $preflightJar)
    } else {
        $endpoint.Config | Add-Member -NotePropertyName avcli_args `
            -NotePropertyValue @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', $preflightJar)
    }
}
Test-ServerdeskAvcliPrerequisites -Endpoint $endpoint -ConfigPath $configPath -AllowDegraded:$allowDegraded

foreach ($stalePath in @($transaction, "$dst\.serverdesk.update-new", "$dst\serverdesk.exe.bak.new",
    "$configPath.update.tmp", "$dst\update.ps1.update-new", "$dst\uninstall.ps1.update-new",
    "$dst\windows-deployment-common.ps1.update-new")) {
    if (Test-Path -LiteralPath $stalePath) {
        throw "Refusing stale update transaction path: $stalePath (inspect and remove it before retrying)."
    }
}

function Copy-Verified([string]$From, [string]$To) {
    Assert-ServerdeskRegularFile $From 'Backup source'
    Copy-Item -LiteralPath $From -Destination $To -Force
    Assert-ServerdeskRegularFile $To 'Backup copy'
    $sourceHash = (Get-FileHash -LiteralPath $From -Algorithm SHA256).Hash
    $copyHash = (Get-FileHash -LiteralPath $To -Algorithm SHA256).Hash
    if ($sourceHash -cne $copyHash) { throw "Backup verification failed: $From -> $To" }
}

$trackedFiles = @('serverdesk.exe', 'run-serverdesk.cmd', 'run-serverdesk.ps1',
    'update.ps1', 'uninstall.ps1', 'windows-deployment-common.ps1')
$existed = @{}
$priorAclEntries = @()
$aclPaths = @($dst, $dataDir, $credentialDir, $authPath, (Join-Path $dataDir 'initial-login.txt'))
$aclPaths += @($trackedFiles | ForEach-Object { Join-Path $dst $_ })
if (Test-Path -LiteralPath $credentialDir -PathType Container) {
    $aclPaths += @(Get-ChildItem -LiteralPath $credentialDir -Recurse -Force |
        ForEach-Object { $_.FullName })
}
foreach ($path in $aclPaths) {
    if (Test-Path -LiteralPath $path) {
        $item = Get-Item -LiteralPath $path -Force
        $priorAclEntries += [PSCustomObject]@{ Path = $item.FullName; Acl = (Get-Acl -LiteralPath $item.FullName) }
    }
}
$priorTask = Get-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
$null = Assert-ServerdeskManagedTask $priorTask $dst
$priorTaskXml = if ($null -ne $priorTask) { Export-ScheduledTask -TaskName serverdesk } else { $null }
$priorTaskWasRunning = $false
$priorTaskWasDisabled = $false
$priorTaskState = 'Absent'
if ($null -ne $priorTask) {
    $priorTaskState = [string]$priorTask.State
    $priorTaskWasRunning = $priorTask.State -eq 'Running'
    $priorTaskWasDisabled = $priorTask.State -eq 'Disabled'
}
$priorFirewall = Get-ServerdeskFirewallSnapshot
$serviceTouched = $false
$rollbackFailed = $false

try {
    New-Item -ItemType Directory -Path $transaction | Out-Null
    & icacls.exe $transaction /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Failed to protect update transaction directory.' }

    foreach ($name in $trackedFiles) {
        $installed = Join-Path $dst $name
        $present = Test-Path -LiteralPath $installed -PathType Leaf
        $existed[$name] = $present
        if ($present) { Copy-Verified $installed (Join-Path $transaction $name) }
    }
    if ($null -ne $priorTaskXml) {
        [IO.File]::WriteAllText((Join-Path $transaction 'scheduled-task.xml'), $priorTaskXml, [Text.Encoding]::Unicode)
    }
    @($priorFirewall) | Export-Clixml -LiteralPath (Join-Path $transaction 'firewall.xml')
    @($priorAclEntries) | Export-Clixml -LiteralPath (Join-Path $transaction 'acls.xml')
    [PSCustomObject]@{
        TaskExisted = ($null -ne $priorTask)
        TaskState = $priorTaskState
        CapturedAtUtc = [DateTime]::UtcNow.ToString('o')
    } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $transaction 'state.json') -Encoding UTF8
    Copy-Verified $new "$dst\.serverdesk.update-new"
    $serviceTouched = $true
    Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    Stop-ServerdeskInstalledProcess $exe
    Start-Sleep -Seconds 2

    Move-Item -LiteralPath "$dst\.serverdesk.update-new" -Destination $exe -Force

    # Migrate only the recognized legacy launcher already installed by older builds.
    $cfg = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $avcliBin = [string](Get-ServerdeskProperty $cfg 'avcli_bin' '')
    if ([IO.Path]::GetExtension($avcliBin).ToLowerInvariant() -in @('.bat', '.cmd')) {
        $expectedLegacy = [IO.Path]::GetFullPath("$dst\avcli\avcli.bat")
        if ((Resolve-ServerdeskConfigPath $avcliBin $configPath) -ine $expectedLegacy) {
            throw "Refusing unrecognized AVCLI batch launcher: $avcliBin"
        }
        $jre = Get-ChildItem "$dst\jre" -Directory -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -First 1
        if ($null -eq $jre) { throw 'Legacy AVCLI migration needs the separately licensed installed JRE.' }
        $java = "$($jre.FullName)\bin\java.exe"
        $jar = "$dst\avcli\avcli.jar"
        Assert-ServerdeskRegularFile $java 'Installed Java executable'
        Assert-ServerdeskRegularFile $jar 'Installed AVCLI JAR'
        $cfg.avcli_bin = $java
        if ($null -ne $cfg.PSObject.Properties['avcli_args']) {
            $cfg.avcli_args = @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', $jar)
        } else {
            $cfg | Add-Member -NotePropertyName avcli_args -NotePropertyValue @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', $jar)
        }
        $utf8NoBom = New-Object Text.UTF8Encoding($false)
        $configTemp = "$configPath.update.tmp"
        [IO.File]::WriteAllText($configTemp, (($cfg | ConvertTo-Json -Depth 20) + [Environment]::NewLine), $utf8NoBom)
        Move-Item -LiteralPath $configTemp -Destination $configPath -Force
    }

    foreach ($name in @('update.ps1', 'uninstall.ps1', 'windows-deployment-common.ps1')) {
        $from = Join-Path $src $name
        $to = Join-Path $dst $name
        if ((Test-Path -LiteralPath $from -PathType Leaf) -and
            ([IO.Path]::GetFullPath($from) -ine [IO.Path]::GetFullPath($to))) {
            Copy-Verified $from "$to.update-new"
            Move-Item -LiteralPath "$to.update-new" -Destination $to -Force
        }
    }

    New-Item -ItemType Directory -Path $credentialDir -Force | Out-Null
    & icacls.exe $credentialDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Failed to protect the runtime credential store.' }
    Write-ServerdeskRunner "$dst\run-serverdesk.ps1" $programDir $dataDir $credentialDir
    if ($null -ne (Get-ChildItem -LiteralPath $credentialDir -Force | Select-Object -First 1)) {
        & icacls.exe "$credentialDir\*" /reset /T /C | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Failed to protect existing managed credentials.' }
        & icacls.exe "$credentialDir\*" /setowner '*S-1-5-32-544' /T /C | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Failed to set trusted owners on existing managed credentials.' }
    }
    foreach ($ownedPath in @($exe, "$dst\run-serverdesk.ps1", "$dst\update.ps1", "$dst\uninstall.ps1",
        "$dst\windows-deployment-common.ps1")) {
        if (Test-Path -LiteralPath $ownedPath) {
            $ownedItem = Get-Item -LiteralPath $ownedPath -Force
            $systemGrant = if ($ownedItem.PSIsContainer) { '*S-1-5-18:(OI)(CI)F' } else { '*S-1-5-18:F' }
            $adminGrant = if ($ownedItem.PSIsContainer) { '*S-1-5-32-544:(OI)(CI)F' } else { '*S-1-5-32-544:F' }
            & icacls.exe $ownedPath /inheritance:r /grant:r $systemGrant $adminGrant | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "Failed to protect installed control path: $ownedPath" }
            & icacls.exe $ownedPath /setowner '*S-1-5-32-544' | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "Failed to set trusted owner: $ownedPath" }
        }
    }
    Set-ServerdeskProgramAcl $programDir
    Set-ServerdeskDataAcl $dataDir
    Assert-ServerdeskRuntimeAcl -ProgramDirectory $programDir -DataDirectory $dataDir
    Register-ServerdeskTask $programDir $dataDir
    Set-ServerdeskFirewall -Endpoint $endpoint -Program $exe

    Start-ScheduledTask -TaskName serverdesk
    Start-Sleep -Seconds 6
    $code = Invoke-ServerdeskHealth $endpoint.HealthUrl
    Remove-Item -LiteralPath "$dst\run-serverdesk.cmd" -Force -ErrorAction SilentlyContinue
    if ($null -ne $priorTask -and -not $priorTaskWasRunning) {
        Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    }
    if ($priorTaskWasDisabled) { Disable-ScheduledTask -TaskName serverdesk | Out-Null }

    # Retain the verified pre-update executable only after the new service passes health.
    Copy-Verified (Join-Path $transaction 'serverdesk.exe') "$dst\serverdesk.exe.bak.new"
    Move-Item -LiteralPath "$dst\serverdesk.exe.bak.new" -Destination "$dst\serverdesk.exe.bak" -Force
    Remove-Item -LiteralPath $transaction -Recurse -Force
    if ($null -ne $priorTask -and -not $priorTaskWasRunning) {
        Write-Host "[OK] update passed transient health validation ($code); prior task state ($($priorTask.State)) was restored."
    } else {
        Write-Host "[OK] updated transactionally and healthy ($code) at $($endpoint.HealthUrl)."
    }
    Write-Host "     Previous executable is retained as $exe.bak."
} catch {
    $updateError = $_
    if (-not $serviceTouched) {
        Remove-Item -LiteralPath "$dst\.serverdesk.update-new", "$dst\serverdesk.exe.bak.new" -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $transaction -Recurse -Force -ErrorAction SilentlyContinue
        throw "Update aborted before service stop; installed service was untouched. $($updateError.Exception.Message)"
    }

    Write-Host '[FAIL] update failed; restoring binary, config, scripts, task, and firewall.'
    try {
        Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
        Stop-ServerdeskInstalledProcess $exe
        Start-Sleep -Seconds 2
        foreach ($name in $trackedFiles) {
            $installed = Join-Path $dst $name
            $backup = Join-Path $transaction $name
            if ($existed[$name]) {
                Copy-Verified $backup $installed
            } else {
                Remove-Item -LiteralPath $installed -Force -ErrorAction SilentlyContinue
            }
            Remove-Item -LiteralPath "$installed.update-new" -Force -ErrorAction SilentlyContinue
        }
        Remove-Item -LiteralPath "$configPath.update.tmp", "$dst\.serverdesk.update-new", "$dst\serverdesk.exe.bak.new" -Force -ErrorAction SilentlyContinue
        if ($null -ne $priorTaskXml) {
            Register-ScheduledTask -TaskName serverdesk -Xml $priorTaskXml -Force | Out-Null
        } else {
            Unregister-ScheduledTask -TaskName serverdesk -Confirm:$false -ErrorAction SilentlyContinue
        }
        Restore-ServerdeskFirewall $priorFirewall
        foreach ($entry in @($priorAclEntries | Sort-Object { $_.Path.Length } -Descending)) {
            if (Test-Path -LiteralPath $entry.Path) {
                Set-Acl -LiteralPath $entry.Path -AclObject $entry.Acl
            }
        }
        if (-not $credentialDirectoryExisted -and (Test-Path -LiteralPath $credentialDir)) {
            $credentialItem = Get-Item -LiteralPath $credentialDir -Force
            if (-not $credentialItem.PSIsContainer -or
                ($credentialItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw 'New credential-store path is unsafe during rollback.'
            }
            if ($null -ne (Get-ChildItem -LiteralPath $credentialDir -Force | Select-Object -First 1)) {
                throw 'New credential store is not empty; preserving the transaction for inspection.'
            }
            Remove-Item -LiteralPath $credentialDir -Force
        }
        if ($priorTaskWasRunning) {
            Start-ScheduledTask -TaskName serverdesk
            Start-Sleep -Seconds 6
            $null = Invoke-ServerdeskHealth $endpoint.HealthUrl
        }
    } catch {
        $rollbackFailed = $true
        Write-Host "[CRITICAL] rollback verification failed: $($_.Exception.Message)"
    }
    if (-not $rollbackFailed) {
        Remove-Item -LiteralPath $transaction -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host '[OK] previous installation state was restored and verified.'
        throw $updateError
    }
    throw "Update and rollback both failed. Preserve $transaction and inspect Task Scheduler/run.log. Original error: $($updateError.Exception.Message)"
}
