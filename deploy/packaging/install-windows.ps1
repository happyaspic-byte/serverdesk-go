# serverdesk Windows installer - run in an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File install-windows.ps1
# Steps: create C:\serverdesk -> copy exe/config -> allow firewall 6005 -> register + start Scheduled Task.
# Optional: if avcli.zip + jre.zip sit next to this script, Stratus collection (avcli) is installed too.
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$dst = 'C:\serverdesk'

New-Item -ItemType Directory -Force -Path $dst | Out-Null
# stop the running instance first - a running exe locks the file and Copy-Item fails
Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
Copy-Item "$src\serverdesk-windows-amd64.exe" "$dst\serverdesk.exe" -Force

$freshInstall = -not (Test-Path "$dst\config.local.json")
if ($freshInstall) {
    Copy-Item "$src\config.example.json" "$dst\config.local.json"
    # put runtime data under C:\serverdesk\data - the example omits runtime_dir and SYSTEM
    # would otherwise land it in the system profile dir.
    $cfg = Get-Content "$dst\config.local.json" -Raw | ConvertFrom-Json
    if (-not $cfg.runtime_dir) {
        $cfg | Add-Member -NotePropertyName runtime_dir -NotePropertyValue 'C:\serverdesk\data'
        $cfg | ConvertTo-Json -Depth 20 | Set-Content "$dst\config.local.json" -Encoding ascii
    }
    Write-Host "[INFO] config.local.json created - edit credentials and device addresses."
    Write-Host "       NOTE: save it as UTF-8 *without* BOM (VS Code default). Notepad UTF-8 BOM is rejected by the Go JSON parser."
} else {
    Write-Host "[INFO] keeping existing config.local.json"
}

# optional Stratus collection: avcli.zip (avcli.jar set) + jre.zip (Windows JRE)
if ((Test-Path "$src\avcli.zip") -and (Test-Path "$src\jre.zip")) {
    Expand-Archive "$src\avcli.zip" -DestinationPath "$dst" -Force
    Expand-Archive "$src\jre.zip" -DestinationPath "$dst\jre" -Force
    $jre = (Get-ChildItem "$dst\jre" -Directory | Select-Object -First 1).FullName
    $bat = "@echo off`r`n`"$jre\bin\java.exe`" -XX:+IgnoreUnrecognizedVMOptions -jar `"$dst\avcli\avcli.jar`" %*`r`n"
    [IO.File]::WriteAllText("$dst\avcli\avcli.bat", $bat)
    if ($freshInstall) {
        $cfg = Get-Content "$dst\config.local.json" -Raw | ConvertFrom-Json
        $cfg | Add-Member -NotePropertyName avcli_bin -NotePropertyValue "$dst\avcli\avcli.bat"
        $cfg | ConvertTo-Json -Depth 20 | Set-Content "$dst\config.local.json" -Encoding ascii
    }
    Write-Host "[INFO] avcli + JRE installed - Stratus collection enabled"
}

New-NetFirewallRule -DisplayName 'serverdesk 6005' -Direction Inbound -Protocol TCP -LocalPort 6005 -Action Allow -ErrorAction SilentlyContinue | Out-Null

$taskCmd = 'cmd /c C:\serverdesk\serverdesk.exe -c C:\serverdesk\config.local.json >> C:\serverdesk\run.log 2>&1'
schtasks /Create /TN serverdesk /TR $taskCmd /SC ONSTART /RU SYSTEM /F | Out-Null

schtasks /Run /TN serverdesk | Out-Null
Start-Sleep -Seconds 6

try {
    $code = (Invoke-WebRequest http://127.0.0.1:6005/api/health -UseBasicParsing -TimeoutSec 8).StatusCode
    Write-Host "[OK] serverdesk is up - http://<this-server-ip>:6005  (health $code)"
} catch {
    Write-Host "[FAIL] health check failed - see C:\serverdesk\run.log"
    exit 1
}
