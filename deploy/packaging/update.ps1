# serverdesk updater - run in an elevated PowerShell from the NEW package folder:
#   powershell -ExecutionPolicy Bypass -File update.ps1
# Replaces C:\serverdesk\serverdesk.exe with the exe next to this script.
# Keeps config and data. Rolls back automatically if the new build fails the health check.
$ErrorActionPreference = 'Stop'
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$dst = 'C:\serverdesk'
$exe = "$dst\serverdesk.exe"
$new = "$src\serverdesk-windows-amd64.exe"

if (-not (Test-Path $new)) { Write-Host "[FAIL] serverdesk-windows-amd64.exe not found next to this script"; exit 1 }
if (-not (Test-Path $exe)) {
    Write-Host "[INFO] not installed yet - running the full installer instead"
    & "$src\install-windows.ps1"
    exit $LASTEXITCODE
}

Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
Copy-Item $exe "$exe.bak" -Force
Copy-Item $new $exe -Force
schtasks /Run /TN serverdesk | Out-Null
Start-Sleep -Seconds 6

try {
    $code = (Invoke-WebRequest http://127.0.0.1:6005/api/health -UseBasicParsing -TimeoutSec 8).StatusCode
    Write-Host "[OK] updated and healthy ($code) - previous build kept as serverdesk.exe.bak"
} catch {
    Write-Host "[FAIL] health check failed - rolling back to serverdesk.exe.bak"
    Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    Copy-Item "$exe.bak" $exe -Force
    schtasks /Run /TN serverdesk | Out-Null
    Write-Host "[INFO] rollback started - see C:\serverdesk\run.log"
    exit 1
}
