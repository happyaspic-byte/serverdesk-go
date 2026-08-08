# serverdesk uninstaller - run in an elevated PowerShell:
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1          (keeps config + data)
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1 -Full    (removes everything)
param([switch]$Full)
$ErrorActionPreference = 'Stop'
$dst = 'C:\serverdesk'

Get-Process serverdesk -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
schtasks /Delete /TN serverdesk /F 2>$null | Out-Null
Remove-NetFirewallRule -DisplayName 'serverdesk 6005' -ErrorAction SilentlyContinue

if ($Full) {
    Remove-Item $dst -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "[OK] removed completely (task + firewall rule + C:\serverdesk)"
} else {
    if (Test-Path $dst) {
        Get-ChildItem $dst -Exclude 'data','config.local.json' | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    }
    Write-Host "[OK] removed program files - kept config.local.json and data\ (use -Full to remove everything)"
}
