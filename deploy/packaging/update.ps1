param([string]$Binary)

# serverdesk updater - run in an elevated Windows PowerShell 5.1 session.
# Replaces the installed executable, preserves state, migrates the legacy bundled
# avcli batch launcher, and rolls back both binary and config if health fails.
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$dst = 'C:\serverdesk'
$exe = "$dst\serverdesk.exe"
$configPath = "$dst\config.local.json"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$new = if ($Binary) {
    $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Binary)
} else {
    "$src\serverdesk-windows-amd64.exe"
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

if (-not (Test-Path -LiteralPath $new -PathType Leaf)) {
    Write-Host "[FAIL] new serverdesk executable not found: $new"
    Write-Host '       pass -Binary <path> or run update.ps1 from a new package folder'
    exit 1
}
Assert-RegularNonReparseFile $new 'New serverdesk executable'
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
    Write-Host '[INFO] not installed yet - running the full installer instead'
    & "$src\install-windows.ps1"
    exit $LASTEXITCODE
}
Assert-RegularNonReparseFile $exe 'Installed serverdesk executable'
Assert-RegularNonReparseFile $configPath 'Installed config'

$cfg = Get-Content $configPath -Raw | ConvertFrom-Json
$originalConfigBytes = $null
$configMigrationNeeded = $false
$avcliBin = [string]$cfg.avcli_bin
$avcliExtension = [IO.Path]::GetExtension($avcliBin)
if ($avcliExtension -ieq '.bat' -or $avcliExtension -ieq '.cmd') {
    $expectedLegacy = [IO.Path]::GetFullPath("$dst\avcli\avcli.bat")
    if ([IO.Path]::GetFullPath($avcliBin) -ine $expectedLegacy) {
        throw "Refusing to migrate unrecognized avcli batch launcher: $avcliBin"
    }
    $jar = "$dst\avcli\avcli.jar"
    $jre = Get-ChildItem "$dst\jre" -Directory | Sort-Object FullName | Select-Object -First 1
    if ($null -eq $jre) {
        throw 'Legacy avcli config requires the bundled JRE; run the full installer.'
    }
    $java = "$($jre.FullName)\bin\java.exe"
    Assert-RegularNonReparseFile $java 'Bundled Java executable'
    Assert-RegularNonReparseFile $jar 'Bundled avcli JAR'
    $originalConfigBytes = [IO.File]::ReadAllBytes($configPath)
    $cfg.avcli_bin = $java
    $avcliArgs = @('-XX:+IgnoreUnrecognizedVMOptions', '-jar', $jar)
    if ($cfg.PSObject.Properties['avcli_args']) {
        $cfg.avcli_args = $avcliArgs
    } else {
        $cfg | Add-Member -NotePropertyName avcli_args -NotePropertyValue $avcliArgs
    }
    $configMigrationNeeded = $true
}

Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
Copy-Item $exe "$exe.bak" -Force

try {
    Copy-Item $new $exe -Force
    if ($configMigrationNeeded) {
        $configTemp = "$configPath.update.tmp"
        [IO.File]::WriteAllText($configTemp, (($cfg | ConvertTo-Json -Depth 20) + [Environment]::NewLine), $utf8NoBom)
        Move-Item $configTemp $configPath -Force
    }
    foreach ($name in @('update.ps1', 'uninstall.ps1')) {
        $from = "$src\$name"
        $to = "$dst\$name"
        if ((Test-Path $from) -and ([IO.Path]::GetFullPath($from) -ne [IO.Path]::GetFullPath($to))) {
            Copy-Item $from $to -Force
        }
    }
    Start-ScheduledTask -TaskName serverdesk
    Start-Sleep -Seconds 6
    $code = (Invoke-WebRequest http://127.0.0.1:6005/api/health -UseBasicParsing -TimeoutSec 8).StatusCode
    Write-Host "[OK] updated and healthy ($code) - previous build kept as serverdesk.exe.bak"
} catch {
    Write-Host '[FAIL] update failed - rolling back binary and config'
    Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
    Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    Copy-Item "$exe.bak" $exe -Force
    if ($null -ne $originalConfigBytes) {
        [IO.File]::WriteAllBytes($configPath, $originalConfigBytes)
    }
    Remove-Item "$configPath.update.tmp" -Force -ErrorAction SilentlyContinue
    Start-ScheduledTask -TaskName serverdesk
    Write-Host '[INFO] rollback started - see C:\serverdesk\run.log'
    exit 1
}
