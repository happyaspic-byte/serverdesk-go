# Shared Windows deployment helpers. PowerShell 5.1 compatible.
Set-StrictMode -Version 2.0

$script:ServerdeskFirewallName = 'Serverdesk-Managed-Inbound'
$script:ServerdeskLegacyFirewallDisplayName = 'serverdesk 6005'

function Assert-ServerdeskPathComponents {
    param(
        [Parameter(Mandatory=$true)][string]$Path,
        [switch]$AllowMissing
    )
    $cursor = [IO.Path]::GetFullPath($Path)
    if ($cursor.Length -gt 3) { $cursor = $cursor.TrimEnd('\') }
    while (-not [string]::IsNullOrWhiteSpace($cursor)) {
        if (Test-Path -LiteralPath $cursor) {
            $component = Get-Item -LiteralPath $cursor -Force
            if (($component.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Path must not traverse a reparse point: $($component.FullName)"
            }
        } elseif (-not $AllowMissing) {
            throw "Path component is missing: $cursor"
        }
        $parent = Split-Path -Parent $cursor
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent
    }
}

function Assert-ServerdeskTrustedReadOnlyPath([string]$Path, [string]$Description) {
    Assert-ServerdeskPathComponents $Path
    $trustedWriters = @(
        'S-1-5-18',
        'S-1-5-32-544',
        'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464'
    )
    $writeMask = ([Security.AccessControl.FileSystemRights]::Write -bor
        [Security.AccessControl.FileSystemRights]::Modify -bor
        [Security.AccessControl.FileSystemRights]::FullControl -bor
        [Security.AccessControl.FileSystemRights]::Delete -bor
        [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor
        [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership)
    $runtimeReadMask = ([Security.AccessControl.FileSystemRights]::ReadData -bor
        [Security.AccessControl.FileSystemRights]::ExecuteFile)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $cursor = $fullPath
    $root = [IO.Path]::GetPathRoot($cursor)
    while ($cursor -ine $root) {
        $acl = Get-Acl -LiteralPath $cursor
        try {
            $ownerAccount = New-Object -TypeName Security.Principal.NTAccount -ArgumentList $acl.Owner
            $ownerSid = $ownerAccount.Translate([Security.Principal.SecurityIdentifier]).Value
        } catch {
            throw "$Description has an unresolvable owner: $cursor"
        }
        if ($ownerSid -notin $trustedWriters) {
            throw "$Description path has a non-administrative owner: $cursor ($ownerSid)"
        }
        $runtimeReadAllowed = $false
        $runtimeReadDenied = $false
        foreach ($entry in @($acl.Access)) {
            if (($entry.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly) -ne 0) {
                continue
            }
            try {
                $entrySid = $entry.IdentityReference.Translate(
                    [Security.Principal.SecurityIdentifier]).Value
            } catch {
                throw "$Description has an unresolvable ACL entry: $cursor"
            }
            if ($entry.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
                ($entry.FileSystemRights -band $writeMask) -ne 0 -and
                $entrySid -notin $trustedWriters) {
                throw "$Description path grants write/modify rights to a non-administrative principal: $cursor ($entrySid)"
            }
            if ($cursor -ieq $fullPath -and $entrySid -in @('S-1-5-18', 'S-1-5-32-544')) {
                if ($entry.AccessControlType -eq [Security.AccessControl.AccessControlType]::Deny -and
                    ($entry.FileSystemRights -band $runtimeReadMask) -ne 0) {
                    $runtimeReadDenied = $true
                } elseif ($entry.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
                    ($entry.FileSystemRights -band $runtimeReadMask) -eq $runtimeReadMask) {
                    $runtimeReadAllowed = $true
                }
            }
        }
        if ($cursor -ieq $fullPath -and ($runtimeReadDenied -or -not $runtimeReadAllowed)) {
            throw "$Description must grant SYSTEM or Administrators read/execute access: $cursor"
        }
        $cursor = Split-Path -Parent $cursor
    }
}

function Set-ServerdeskProgramAcl([string]$ProgramDirectory) {
    & icacls.exe $ProgramDirectory /inheritance:r /grant:r `
        '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)RX' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to protect Program Files root: $ProgramDirectory" }
    if ($null -ne (Get-ChildItem -LiteralPath $ProgramDirectory -Force | Select-Object -First 1)) {
        & icacls.exe "$ProgramDirectory\*" /reset /T /C | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Failed to propagate Program Files ACLs: $ProgramDirectory" }
    }
    & icacls.exe $ProgramDirectory /setowner '*S-1-5-32-544' /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to set Program Files owners: $ProgramDirectory" }
}

function Set-ServerdeskDataAcl([string]$DataDirectory) {
    & icacls.exe $DataDirectory /inheritance:r /grant:r `
        '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-18:(OI)(CI)F' '*S-1-5-19:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to protect ProgramData root: $DataDirectory" }
    if ($null -ne (Get-ChildItem -LiteralPath $DataDirectory -Force | Select-Object -First 1)) {
        & icacls.exe "$DataDirectory\*" /reset /T /C | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Failed to propagate ProgramData ACLs: $DataDirectory" }
    }
    & icacls.exe $DataDirectory /setowner '*S-1-5-32-544' /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to set ProgramData owners: $DataDirectory" }
}

function Test-ServerdeskAclGrant {
    param(
        [Parameter(Mandatory=$true)][string]$Path,
        [Parameter(Mandatory=$true)][string]$Sid,
        [Parameter(Mandatory=$true)][Security.AccessControl.FileSystemRights]$RequiredRights
    )
    $acl = Get-Acl -LiteralPath $Path
    foreach ($entry in @($acl.Access)) {
        try {
            $entrySid = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        } catch { continue }
        if ($entrySid -eq $Sid -and
            $entry.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            ($entry.FileSystemRights -band $RequiredRights) -eq $RequiredRights) {
            return $true
        }
    }
    return $false
}

function Assert-ServerdeskRuntimeAcl {
    param(
        [Parameter(Mandatory=$true)][string]$ProgramDirectory,
        [Parameter(Mandatory=$true)][string]$DataDirectory
    )
    $readExecute = ([Security.AccessControl.FileSystemRights]::ReadAndExecute)
    $modify = ([Security.AccessControl.FileSystemRights]::Modify)
    $executable = Join-Path $ProgramDirectory 'serverdesk.exe'
    $configPath = Join-Path $DataDirectory 'config.local.json'
    $authPath = Join-Path $DataDirectory 'auth.json'
    foreach ($readPath in @($ProgramDirectory, $executable)) {
        if (-not (Test-Path -LiteralPath $readPath)) { continue }
        if (-not (Test-ServerdeskAclGrant -Path $readPath -Sid 'S-1-5-19' -RequiredRights $readExecute)) {
            throw "Program Files ACL must grant LocalService read/execute access: $readPath"
        }
        if (Test-ServerdeskAclGrant -Path $readPath -Sid 'S-1-5-19' -RequiredRights $modify) {
            throw "Program Files ACL must not grant LocalService modify access: $readPath"
        }
    }
    foreach ($writePath in @($DataDirectory, $configPath, $authPath)) {
        if (-not (Test-Path -LiteralPath $writePath)) { continue }
        if (-not (Test-ServerdeskAclGrant -Path $writePath -Sid 'S-1-5-19' -RequiredRights $modify)) {
            throw "ProgramData ACL must grant LocalService modify access: $writePath"
        }
    }
}

function Assert-ServerdeskAdministrator {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'This operation requires an elevated Administrator PowerShell session.'
    }
}

function Assert-ServerdeskManagedTask($Task, [string]$Destination) {
    if ($null -eq $Task) { return }
    $principal = [string]$Task.Principal.UserId
    if ($principal -notin @('SYSTEM', 'S-1-5-18', 'NT AUTHORITY\SYSTEM',
        'LOCAL SERVICE', 'S-1-5-19', 'NT AUTHORITY\LOCAL SERVICE')) {
        throw "Refusing an existing serverdesk task with an unexpected principal: $principal"
    }
    $actions = @($Task.Actions)
    if ($actions.Count -ne 1) {
        throw "Refusing an existing serverdesk task with $($actions.Count) actions; exactly one managed action is required."
    }
    $action = $actions[0]
    $executeName = [IO.Path]::GetFileName([string]$action.Execute)
    $arguments = ([string]$action.Arguments).Trim()
    $workingDirectory = ([string]$action.WorkingDirectory).TrimEnd('\')
    $directBinaryAction = $executeName -ieq 'serverdesk.exe' -and
        $workingDirectory -ieq $Destination -and
        ([IO.Path]::GetFullPath([string]$action.Execute) -ieq [IO.Path]::GetFullPath((Join-Path $Destination 'serverdesk.exe')))
    $currentAction = $executeName -ieq 'powershell.exe' -and
        $workingDirectory -ieq $Destination -and
        $arguments -ieq '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File C:\serverdesk\run-serverdesk.ps1'
    $cmdRunnerAction = $executeName -ieq 'cmd.exe' -and
        $workingDirectory -ieq $Destination -and
        $arguments -ieq '/d /c run-serverdesk.cmd'
    $legacyAction = $executeName -in @('cmd', 'cmd.exe') -and
        $arguments -ieq '/c C:\serverdesk\serverdesk.exe -c C:\serverdesk\config.local.json >> C:\serverdesk\run.log 2>&1'
    if (-not ($directBinaryAction -or $currentAction -or $cmdRunnerAction -or $legacyAction)) {
        throw 'Refusing an existing serverdesk task whose action is not owned by this installation.'
    }
    if ([string]$Task.State -notin @('Running', 'Ready', 'Disabled')) {
        throw "Existing serverdesk task is in a transient/unsupported state; retry after it settles: $($Task.State)"
    }
}

function Assert-ServerdeskRegularFile([string]$Path, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description must be a regular file: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Description must not be a reparse point: $Path"
    }
    Assert-ServerdeskPathComponents $Path
}

function Stop-ServerdeskInstalledProcess([string]$ExecutablePath) {
    $expected = [IO.Path]::GetFullPath($ExecutablePath)
    foreach ($process in @(Get-Process -Name serverdesk -ErrorAction SilentlyContinue)) {
        $actual = ''
        try { $actual = [IO.Path]::GetFullPath([string]$process.Path) } catch { continue }
        if ($actual -ieq $expected) {
            Stop-Process -Id $process.Id -Force -ErrorAction Stop
            Wait-Process -Id $process.Id -Timeout 10 -ErrorAction SilentlyContinue
            if ($null -ne (Get-Process -Id $process.Id -ErrorAction SilentlyContinue)) {
                throw "Installed Serverdesk process did not stop within 10 seconds: PID $($process.Id)"
            }
        }
    }
}

function Resolve-ServerdeskConfigPath([string]$Value, [string]$ConfigPath) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return '' }
    if ([IO.Path]::IsPathRooted($Value)) { return [IO.Path]::GetFullPath($Value) }
    return [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $ConfigPath) $Value))
}

function Assert-ServerdeskPrivateKey {
    param(
        [Parameter(Mandatory=$true)][string]$Path,
        [Parameter(Mandatory=$true)][string]$ConfigPath,
        [string]$Description = 'TLS private key'
    )
    Assert-ServerdeskRegularFile $Path $Description
    $configRoot = [IO.Path]::GetFullPath((Split-Path -Parent $ConfigPath)).TrimEnd('\')
    $keyPath = [IO.Path]::GetFullPath($Path)
    if (-not $keyPath.StartsWith(($configRoot + '\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "$Description must be stored below the protected installation directory: $configRoot"
    }

    $allowedSids = @('S-1-5-18', 'S-1-5-32-544')
    $acl = Get-Acl -LiteralPath $keyPath
    try {
        $ownerAccount = New-Object -TypeName Security.Principal.NTAccount -ArgumentList $acl.Owner
        $ownerSid = $ownerAccount.Translate([Security.Principal.SecurityIdentifier]).Value
    } catch {
        throw "$Description has an unresolvable owner: $keyPath"
    }
    if ($ownerSid -notin $allowedSids) {
        throw "$Description owner must be SYSTEM or Administrators: $keyPath"
    }
    $allowedReaders = @{}
    foreach ($entry in @($acl.Access)) {
        try {
            $entrySid = $entry.IdentityReference.Translate(
                [Security.Principal.SecurityIdentifier]).Value
        } catch {
            throw "$Description has an unresolvable ACL entry: $keyPath"
        }
        if ($entrySid -notin $allowedSids) {
            throw "$Description ACL may grant access only to SYSTEM and Administrators: $keyPath"
        }
        if ($entry.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            ($entry.FileSystemRights -band [Security.AccessControl.FileSystemRights]::ReadData) -ne 0) {
            $allowedReaders[$entrySid] = $true
        }
    }
    foreach ($requiredSid in $allowedSids) {
        if (-not $allowedReaders.ContainsKey($requiredSid)) {
            throw "$Description ACL must grant access to SYSTEM and Administrators: $keyPath"
        }
    }
}

function Get-ServerdeskProperty($Object, [string]$Name, $DefaultValue) {
    if ($null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name]) {
        return $Object.PSObject.Properties[$Name].Value
    }
    return $DefaultValue
}

function Test-ServerdeskLocalAddress([string]$HostName) {
    try {
        $literal = $null
        $resolved = if ([Net.IPAddress]::TryParse($HostName, [ref]$literal)) {
            @($literal)
        } else {
            @([Net.Dns]::GetHostAddresses($HostName))
        }
        $local = @([Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces() |
            ForEach-Object { $_.GetIPProperties().UnicastAddresses } |
            ForEach-Object { $_.Address.ToString() })
        if ($resolved.Count -eq 0) { return $false }
        foreach ($address in $resolved) {
            if ([Net.IPAddress]::IsLoopback($address)) { continue }
            if ($local -notcontains $address.ToString()) { return $false }
        }
        return $true
    } catch {
        return $false
    }
}

function Find-ServerdeskMachineCommand([string]$Name) {
    $machinePath = [string][Environment]::GetEnvironmentVariable('Path', 'Machine')
    $extensions = @('')
    if ([string]::IsNullOrWhiteSpace([IO.Path]::GetExtension($Name))) {
        $machinePathExt = [string][Environment]::GetEnvironmentVariable('PATHEXT', 'Machine')
        if ([string]::IsNullOrWhiteSpace($machinePathExt)) { $machinePathExt = '.COM;.EXE;.BAT;.CMD' }
        $extensions = @($machinePathExt.Split(';') | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    }
    foreach ($directoryValue in @($machinePath.Split(';'))) {
        $directory = [Environment]::ExpandEnvironmentVariables($directoryValue.Trim().Trim('"'))
        if ([string]::IsNullOrWhiteSpace($directory)) { continue }
        foreach ($extension in $extensions) {
            try { $candidate = Join-Path $directory ($Name + $extension) } catch { continue }
            if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                Assert-ServerdeskRegularFile $candidate 'Machine-PATH executable'
                return [IO.Path]::GetFullPath($candidate)
            }
        }
    }
    return ''
}

function Get-ServerdeskEndpoint {
    param(
        [Parameter(Mandatory=$true)][string]$ConfigPath,
        [string]$HealthUrl
    )
    Assert-ServerdeskRegularFile $ConfigPath 'Serverdesk config'
    try {
        $cfg = Get-Content -LiteralPath $ConfigPath -Raw | ConvertFrom-Json
    } catch {
        throw "Invalid config JSON: $($_.Exception.Message)"
    }

    $configRoot = [IO.Path]::GetFullPath((Split-Path -Parent $ConfigPath)).TrimEnd('\')
    $runtimeValue = [string](Get-ServerdeskProperty $cfg 'runtime_dir' 'data')
    if ([string]::IsNullOrWhiteSpace($runtimeValue)) { $runtimeValue = 'data' }
    $runtimePath = if ([IO.Path]::IsPathRooted($runtimeValue)) {
        [IO.Path]::GetFullPath($runtimeValue).TrimEnd('\')
    } else {
        [IO.Path]::GetFullPath((Join-Path $configRoot $runtimeValue)).TrimEnd('\')
    }
    if ($runtimePath -eq $configRoot -or
        -not $runtimePath.StartsWith(($configRoot + '\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "runtime_dir must be a child of the protected installation directory: $configRoot"
    }
    Assert-ServerdeskPathComponents -Path $runtimePath -AllowMissing

    $listen = [string](Get-ServerdeskProperty $cfg 'listen' '127.0.0.1:9891')
    if ([string]::IsNullOrWhiteSpace($listen)) { $listen = '127.0.0.1:9891' }
    $hostName = ''
    $portText = ''
    if ($listen -match '^\[([^\]]+)\]:(\d{1,5})$') {
        $hostName, $portText = $matches[1], $matches[2]
    } elseif ($listen -match '^([^:\s]+):(\d{1,5})$') {
        $hostName, $portText = $matches[1], $matches[2]
    } else {
        throw "listen must be HOST:PORT (IPv6 must use brackets): $listen"
    }
    $port = [int]$portText
    if ($port -lt 1 -or $port -gt 65535) { throw "listen port is outside 1..65535: $port" }

    $cert = [string](Get-ServerdeskProperty $cfg 'tls_cert_file' '')
    $key = [string](Get-ServerdeskProperty $cfg 'tls_key_file' '')
    if ([string]::IsNullOrWhiteSpace($cert) -ne [string]::IsNullOrWhiteSpace($key)) {
        throw 'tls_cert_file and tls_key_file must be configured together.'
    }
    $tls = -not [string]::IsNullOrWhiteSpace($cert)
    if ($tls) {
        Assert-ServerdeskRegularFile (Resolve-ServerdeskConfigPath $cert $ConfigPath) 'TLS certificate'
        Assert-ServerdeskPrivateKey -Path (Resolve-ServerdeskConfigPath $key $ConfigPath) -ConfigPath $ConfigPath
    }

    $normalizedHost = $hostName.Trim().ToLowerInvariant()
    $wildcard = $normalizedHost -in @('0.0.0.0', '::', '*', '+')
    $loopback = $normalizedHost -in @('127.0.0.1', '::1', 'localhost')
    $exposed = -not $loopback
    $allowInsecure = $false
    if ($null -ne $cfg.PSObject.Properties['allow_insecure_http']) {
        $allowInsecureValue = Get-ServerdeskProperty $cfg 'allow_insecure_http' $false
        if ($allowInsecureValue -isnot [bool]) {
            throw 'allow_insecure_http must be a JSON Boolean.'
        }
        $allowInsecure = $allowInsecureValue
    }
    if ($exposed -and -not $tls -and -not $allowInsecure) {
        throw 'Non-loopback HTTP requires TLS or allow_insecure_http=true (break-glass only).'
    }

    $scheme = if ($tls) { 'https' } else { 'http' }
    $healthHost = if ($normalizedHost -in @('0.0.0.0', '*', '+')) {
        '127.0.0.1'
    } elseif ($normalizedHost -eq '::') {
        '[::1]'
    } elseif ($hostName.Contains(':')) {
        "[$hostName]"
    } else {
        $hostName
    }
    $derivedHealth = "${scheme}://${healthHost}:${port}/api/health"
    if ($tls -and $wildcard -and [string]::IsNullOrWhiteSpace($HealthUrl)) {
        throw 'TLS with a wildcard listener requires -HealthUrl using a certificate-valid hostname that resolves only to this host.'
    }
    if ([string]::IsNullOrWhiteSpace($HealthUrl)) { $HealthUrl = $derivedHealth }

    $healthUri = $null
    if (-not [Uri]::TryCreate($HealthUrl, [UriKind]::Absolute, [ref]$healthUri) -or
        $healthUri.Scheme -notin @('http', 'https') -or
        $healthUri.AbsolutePath -cne '/api/health' -or
        -not [string]::IsNullOrEmpty($healthUri.UserInfo) -or
        -not [string]::IsNullOrEmpty($healthUri.Query) -or
        -not [string]::IsNullOrEmpty($healthUri.Fragment) -or
        $healthUri.Port -ne $port -or
        $healthUri.Scheme -ne $scheme -or
        -not (Test-ServerdeskLocalAddress $healthUri.DnsSafeHost)) {
        throw 'HealthUrl must match the configured scheme/port and resolve only to this host at /api/health.'
    }

    $displayHost = if ($wildcard -and $tls) {
        if ($healthUri.Host.Contains(':')) { "[$($healthUri.Host)]" } else { $healthUri.Host }
    } elseif ($wildcard) {
        $env:COMPUTERNAME
    } elseif ($hostName.Contains(':')) {
        "[$hostName]"
    } else {
        $hostName
    }
    [PSCustomObject]@{
        Config = $cfg
        Listen = $listen
        ListenHost = $hostName
        Port = $port
        Scheme = $scheme
        Exposed = $exposed
        RuntimePath = $runtimePath
        HealthUrl = $healthUri.AbsoluteUri
        DisplayUrl = "${scheme}://${displayHost}:${port}"
    }
}

function Test-ServerdeskAvcliPrerequisites {
    param(
        [Parameter(Mandatory=$true)]$Endpoint,
        [Parameter(Mandatory=$true)][string]$ConfigPath,
        [switch]$AllowDegraded
    )
    $clusters = @((Get-ServerdeskProperty $Endpoint.Config 'clusters' @()))
    if ($clusters.Count -eq 0) {
        Write-Host '[INFO] No Stratus clusters configured; AVCLI/JRE preflight is not required yet.'
        return
    }
    $bin = [string](Get-ServerdeskProperty $Endpoint.Config 'avcli_bin' 'avcli')
    if ([string]::IsNullOrWhiteSpace($bin)) { $bin = 'avcli' }
    $resolved = ''
    if ([IO.Path]::IsPathRooted($bin) -or $bin.Contains('\') -or $bin.Contains('/')) {
        $resolved = Resolve-ServerdeskConfigPath $bin $ConfigPath
        if (-not (Test-Path -LiteralPath $resolved -PathType Leaf)) { $resolved = '' }
    } else {
        # Scheduled Task runs as SYSTEM, so a current administrator's User PATH
        # is not a valid availability check. Resolve bare commands only through
        # the machine PATH inherited by the SYSTEM task.
        $resolved = Find-ServerdeskMachineCommand $bin
    }
    $problem = ''
    if ([string]::IsNullOrWhiteSpace($resolved)) {
        $problem = "AVCLI executable is unavailable: $bin"
    } else {
        Assert-ServerdeskRegularFile $resolved 'AVCLI executable'
        Assert-ServerdeskTrustedReadOnlyPath $resolved 'AVCLI executable'
    }
    if ([string]::IsNullOrWhiteSpace($problem) -and
        [IO.Path]::GetExtension($resolved).ToLowerInvariant() -in @('.bat', '.cmd')) {
        $problem = 'avcli_bin must be a direct executable, not a batch launcher.'
    } elseif ([string]::IsNullOrWhiteSpace($problem) -and
        [IO.Path]::GetFileName($resolved) -ieq 'java.exe') {
        $args = @((Get-ServerdeskProperty $Endpoint.Config 'avcli_args' @()))
        $jarIndex = [Array]::IndexOf($args, '-jar')
        if ($jarIndex -lt 0 -or $jarIndex + 1 -ge $args.Count) {
            $problem = 'Java AVCLI configuration is missing -jar <avcli.jar>.'
        } else {
            $jar = Resolve-ServerdeskConfigPath ([string]$args[$jarIndex + 1]) $ConfigPath
            if (-not (Test-Path -LiteralPath $jar -PathType Leaf)) {
                $problem = "AVCLI JAR is unavailable: $jar"
            } else {
                Assert-ServerdeskRegularFile $jar 'AVCLI JAR'
                Assert-ServerdeskTrustedReadOnlyPath $jar 'AVCLI JAR'
            }
        }
    }
    if (-not [string]::IsNullOrWhiteSpace($problem)) {
        if ($AllowDegraded) {
            Write-Warning "$problem Stratus collection will be degraded by explicit operator request."
            return
        }
        throw "$problem Provision licensed Stratus AVCLI/JRE first, or set SERVERDESK_ALLOW_DEGRADED_COLLECTION=1 to acknowledge degraded monitoring."
    }
    Write-Host "[OK] AVCLI/JRE preflight passed: $resolved"
}

function Get-ServerdeskFirewallSnapshot {
    $rules = @(Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq $script:ServerdeskFirewallName -or $_.DisplayName -eq $script:ServerdeskLegacyFirewallDisplayName
    })
    $snapshot = @()
    foreach ($rule in $rules) {
        $port = $rule | Get-NetFirewallPortFilter
        $address = $rule | Get-NetFirewallAddressFilter
        $app = $rule | Get-NetFirewallApplicationFilter
        $snapshot += [PSCustomObject]@{
            Name=$rule.Name; DisplayName=$rule.DisplayName; Enabled=$rule.Enabled
            Direction=$rule.Direction; Action=$rule.Action; Profile=$rule.Profile
            Program=$app.Program; Protocol=$port.Protocol; LocalPort=$port.LocalPort
            RemoteAddress=$address.RemoteAddress
        }
    }
    return @($snapshot)
}

function Remove-ServerdeskManagedFirewall {
    Get-NetFirewallRule -ErrorAction SilentlyContinue | Where-Object {
        $_.Name -eq $script:ServerdeskFirewallName -or $_.DisplayName -eq $script:ServerdeskLegacyFirewallDisplayName
    } | Remove-NetFirewallRule -ErrorAction Stop
}

function Restore-ServerdeskFirewall($Snapshot) {
    Remove-ServerdeskManagedFirewall
    foreach ($rule in @($Snapshot)) {
        New-NetFirewallRule -Name $rule.Name -DisplayName $rule.DisplayName -Enabled $rule.Enabled `
            -Direction $rule.Direction -Action $rule.Action -Profile $rule.Profile `
            -Program $rule.Program -Protocol $rule.Protocol -LocalPort $rule.LocalPort `
            -RemoteAddress $rule.RemoteAddress | Out-Null
    }
}

function Set-ServerdeskFirewall {
    param([Parameter(Mandatory=$true)]$Endpoint, [Parameter(Mandatory=$true)][string]$Program)
    Remove-ServerdeskManagedFirewall
    if ($Endpoint.Exposed) {
        New-NetFirewallRule -Name $script:ServerdeskFirewallName `
            -DisplayName 'Serverdesk managed inbound' -Direction Inbound -Program $Program `
            -Protocol TCP -LocalPort $Endpoint.Port -RemoteAddress LocalSubnet `
            -Profile Domain,Private -Action Allow | Out-Null
        Write-Host "[INFO] Firewall opened to LocalSubnet on TCP $($Endpoint.Port) for Domain/Private profiles."
    } else {
        Write-Host '[INFO] Loopback-only listener: no inbound firewall rule was created.'
    }
}

function Write-ServerdeskRunner([string]$Destination, [string]$CredentialDirectory) {
    $runner = @'
$ErrorActionPreference = 'Continue'
Set-Location 'C:\serverdesk'
$env:SERVERDESK_CREDENTIALS_STORE = '__SERVERDESK_CREDENTIALS_STORE__'
$logPath = 'C:\serverdesk\run.log'
$maxLogBytes = 20MB

function Rotate-ServerdeskLog {
    if (-not (Test-Path -LiteralPath $logPath -PathType Leaf)) { return }
    if ((Get-Item -LiteralPath $logPath).Length -lt $maxLogBytes) { return }
    Remove-Item -LiteralPath "$logPath.5" -Force -ErrorAction SilentlyContinue
    for ($i = 4; $i -ge 1; $i--) {
        if (Test-Path -LiteralPath "$logPath.$i") {
            Move-Item -LiteralPath "$logPath.$i" -Destination "$logPath.$($i + 1)" -Force -ErrorAction Stop
        }
    }
    Move-Item -LiteralPath $logPath -Destination "$logPath.1" -Force -ErrorAction Stop
}

function Write-ServerdeskLog([string]$Line) {
    Rotate-ServerdeskLog
    Add-Content -LiteralPath $logPath -Value $Line -Encoding UTF8 -ErrorAction Stop
}

while ($true) {
    Rotate-ServerdeskLog
    & 'C:\serverdesk\serverdesk.exe' -c 'C:\serverdesk\config.local.json' -auth 'C:\serverdesk\auth.json' 2>&1 |
        ForEach-Object { Write-ServerdeskLog ([string]$_) }
    $exitCode = $LASTEXITCODE
    Write-ServerdeskLog "[$([DateTime]::Now.ToString('s'))] serverdesk exited with code $exitCode; restarting in 5 seconds"
    Start-Sleep -Seconds 5
}
'@
    $runner = $runner.Replace('__SERVERDESK_CREDENTIALS_STORE__', $CredentialDirectory)
    [IO.File]::WriteAllText($Destination, ($runner + [Environment]::NewLine), [Text.Encoding]::ASCII)
}

function Register-ServerdeskTask([string]$ProgramDirectory, [string]$DataDirectory) {
    if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
        $DataDirectory = $ProgramDirectory
    }
    $executable = Join-Path $ProgramDirectory 'serverdesk.exe'
    $configPath = Join-Path $DataDirectory 'config.local.json'
    $authPath = Join-Path $DataDirectory 'auth.json'
    $arguments = "-c `"$configPath`" -auth `"$authPath`""
    $taskAction = New-ScheduledTaskAction -Execute $executable `
        -Argument $arguments -WorkingDirectory $ProgramDirectory
    $taskTrigger = New-ScheduledTaskTrigger -AtStartup
    $taskPrincipal = New-ScheduledTaskPrincipal -UserId 'NT AUTHORITY\LOCAL SERVICE' `
        -LogonType ServiceAccount -RunLevel Limited
    $taskSettings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName serverdesk -Action $taskAction -Trigger $taskTrigger `
        -Principal $taskPrincipal -Settings $taskSettings -Force | Out-Null
}

function Invoke-ServerdeskHealth([string]$HealthUrl) {
    $response = Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 10
    if ($response.StatusCode -ne 200) { throw "Health check returned HTTP $($response.StatusCode)." }
    return $response.StatusCode
}
