# serverdesk uninstaller - run in an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1          (keeps config + data)
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1 -Full    (removes everything)
param([switch]$Full)
$ErrorActionPreference = 'Stop'
$dst = 'C:\serverdesk'

Stop-ScheduledTask -TaskName serverdesk -ErrorAction SilentlyContinue
Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
schtasks /Delete /TN serverdesk /F 2>$null | Out-Null
Get-NetFirewallRule -DisplayName 'serverdesk 6005' -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue

if ($Full) {
    Set-Location $env:TEMP
    Remove-Item $dst -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item 'C:\serverdesk-setup.exe','C:\serverdesk-setup.download' -Force -ErrorAction SilentlyContinue
    Write-Host "[OK] removed completely (task + firewall + C:\serverdesk + setup exe)"
} else {
    if (Test-Path $dst) {
        Get-ChildItem $dst -Exclude 'auth.json','data','config.local.json','update.ps1','uninstall.ps1' | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }
    Write-Host "[OK] removed program files - kept auth.json, config, data, update.ps1, and uninstall.ps1"
    Write-Host "     use -Full to remove everything"
}
