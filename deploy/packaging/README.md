# 배포 스크립트 정본 — dist/serverdesk-pkg/ 조립 시 여기서 복사한다.
# 수정은 이 디렉터리에서 하고, dist 는 결과물(gitignore)이다.

## Windows 7z SFX

Use the full installer module `7zSD.sfx`. Do not use `7zS2.sfx` or
`7zS2con.sfx`; the small modules ignore installer config and auto-select
`setup.bat`, whose final `pause` prevents unattended completion.

Build the payload with `serverdesk-pkg/` as its top-level directory, then
concatenate `7zSD.sfx`, this UTF-8 config, and the payload archive:

```text
;!@Install@!UTF-8!
Title="serverdesk setup"
Directory=""
RunProgram="powershell.exe -NoProfile -ExecutionPolicy Bypass -File \"%%T\\serverdesk-pkg\\install-windows.ps1\""
;!@InstallEnd@!
```

After installation, maintenance scripts are copied to `C:\serverdesk`:

```powershell
powershell -ExecutionPolicy Bypass -File C:\serverdesk\update.ps1 -Binary C:\path\serverdesk-windows-amd64.exe
powershell -ExecutionPolicy Bypass -File C:\serverdesk\uninstall.ps1
powershell -ExecutionPolicy Bypass -File C:\serverdesk\uninstall.ps1 -Full
```

Windows and Linux setup require no password input. A fresh install creates unique administrator
credentials and stores the CLI's `ADMIN_USERNAME`/`ADMIN_PASSWORD` output in a restricted
`initial-login.txt`: `C:\serverdesk\initial-login.txt` on Windows, or
`/var/lib/serverdesk/initial-login.txt` owned by the `serverdesk` account with mode `0600` on Linux.
Securely record the credentials, then delete that file or rotate the password. Reinstalling or updating
preserves the existing `auth.json`; a credential change invalidates existing sessions on the next
protected request. Windows setup also normalizes relative `runtime_dir` values to
`C:\serverdesk\data`, copies bundled MIB assets, executes bundled Java/avcli directly without a
batch shell, restricts the firewall rule to the installed binary and local Domain/Private networks,
and registers the startup task with `C:\serverdesk` as its working directory. A local runner restarts
the process after five seconds; Task Scheduler provides a one-minute fallback restart.
