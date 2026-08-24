# 배포 스크립트 정본 — dist/serverdesk-pkg/ 조립 시 여기서 복사한다.
# 수정은 이 디렉터리에서 하고, dist 는 결과물(gitignore)이다.

## Linux package

Linux payload에는 `serverdesk-linux-amd64`, `config.example.json`, `deploy/serverdesk.service`,
`deploy/serverdesk-net.service`와 이 디렉터리의 `serverdesk-network.sh` /
`serverdesk-network.env.example`이 모두 포함되어야 한다. 설치·업데이트는 같은 transactional
installer를 사용한다.

```bash
sh deploy/packaging/validate-deployment.sh
sudo sh deploy/packaging/install-linux.sh
sudo sh deploy/packaging/update-linux.sh
```

rootless validation은 특정 고객 IP/NIC의 재유입, unit sandbox/capability 회귀, network config
injection과 systemd unit 문법을 검사한다. 자세한 production 절차는
[`docs/DEPLOYMENT.md`](../../docs/DEPLOYMENT.md)를 참고한다.

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

## 보안 하드닝 (Linux)

### 프로세스 인자(argv) 시크릿 노출 완화

`avcli` 연동(`internal/avcli/client.go`)은 CLI 특성상 `-p <암호>` 인자 외에 암호 전달 수단을 지원하지 않아, `ps`, `top`, `/proc/<pid>/cmdline`, `journalctl` 등을 통해 FT 클러스터 장비 비밀번호가 노출될 위험이 있다.

운영 환경에서는 다음 완화책을 적용한다:

1. **`proc` `hidepid=2` 마운트 옵션 적용**
   - 일반 사용자가 다른 사용자의 프로세스 정보(`/proc/<pid>`)를 조회하지 못하도록 차단한다.
   - `/etc/fstab` 설정 예시:
     ```text
     proc    /proc    proc    defaults,nosuid,nodev,noexec,relatime,hidepid=2    0    0
     ```
   - **주의**: `hidepid` 옵션은 배포 전 다른 데몬/모니터링 도구와의 호환성 사전 테스트를 반드시 수행하고, fstab 반영 후 시스템 재부팅(또는 `mount -o remount,hidepid=2 /proc`)을 통해 적용해야 한다.
2. **전용 비특권 계정(`serverdesk`) 운영**
   - 서비스를 root나 다목적 공용 계정으로 실행하지 않고, 전용 비특권 계정(`serverdesk`)으로 제한하여 프로세스 및 상태 디렉터리(`~/.serverdesk-go` / `/var/lib/serverdesk`, 권한 `0700`/`0600`)에 대한 타 계정 접근을 원천 차단한다.
