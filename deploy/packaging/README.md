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

The Windows package now installs executable code under `C:\Program Files\Serverdesk`, writable state
under `C:\ProgramData\Serverdesk`, and runs the task as `NT AUTHORITY\LOCAL SERVICE`. Commercial GA
still requires real Windows Server migration, DPAPI, update/rollback/uninstall, ACL, and reboot UAT.

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

After installation, maintenance scripts are copied to `C:\Program Files\Serverdesk`:

```powershell
powershell -ExecutionPolicy Bypass -File "C:\Program Files\Serverdesk\update.ps1" -Binary C:\path\serverdesk-windows-amd64.exe
powershell -ExecutionPolicy Bypass -File "C:\Program Files\Serverdesk\uninstall.ps1"
powershell -ExecutionPolicy Bypass -File "C:\Program Files\Serverdesk\uninstall.ps1" -Full
```

Windows and Linux setup require no password input. A fresh install creates unique administrator
credentials and stores the CLI's `ADMIN_USERNAME`/`ADMIN_PASSWORD` output in a restricted
`initial-login.txt`: `C:\serverdesk\initial-login.txt` on Windows, or
`/var/lib/serverdesk/initial-login.txt` owned by the `serverdesk` account with mode `0600` on Linux.
Securely record the credentials, then delete that file or rotate the password. Reinstalling or updating
preserves the existing `auth.json`; a credential change invalidates existing sessions on the next
protected request. Windows setup resolves relative `runtime_dir` values below the protected
`C:\serverdesk` root, creates a DPAPI managed credential store at `C:\serverdesk\credentials`, and
registers the startup task with `C:\serverdesk` as its working directory. The firewall follows the
configured listen port and is created only for non-loopback listeners on Domain/Private LocalSubnet.
The runner retains five rotated 20 MiB logs, restarts after five seconds, and Task Scheduler provides
a one-minute fallback.

Stratus AVCLI, its JRE, and vendor MIBs are not bundled. Provision artifacts obtained under the
customer's vendor entitlement separately, configure `avcli_bin`/`avcli_args`, and place licensed
MIBs in `C:\serverdesk\mibs` or `/opt/serverdesk/mibs`. Configured Stratus clusters fail installation
preflight when AVCLI/JRE is unavailable unless an operator explicitly acknowledges degraded
monitoring with `SERVERDESK_ALLOW_DEGRADED_COLLECTION=1`.

## 보안 하드닝 (Linux)

### 프로세스 인자(argv) 시크릿 노출 완화

`avcli` 연동(`internal/avcli/client.go`)은 CLI 특성상 `-p <암호>` 인자 외에 암호 전달 수단을 지원하지 않아, `ps`, `top`, `/proc/<pid>/cmdline`, `journalctl` 등을 통해 FT 클러스터 장비 비밀번호가 노출될 위험이 있다.

운영 환경에서는 다음 완화책을 적용한다:

1. **호스트 전체 `proc`에 `hidepid=2` 적용**
   - 일반 사용자가 다른 사용자의 프로세스 정보(`/proc/<pid>`)를 조회하지 못하도록 차단한다.
   - `/etc/fstab` 설정 예시:
     ```text
     proc    /proc    proc    defaults,nosuid,nodev,noexec,relatime,hidepid=2    0    0
     ```
   - **주의**: `hidepid` 옵션은 배포 전 다른 데몬/모니터링 도구와의 호환성 사전 테스트를 반드시 수행하고, fstab 반영 후 시스템 재부팅(또는 `mount -o remount,hidepid=2 /proc`)을 통해 적용해야 한다.
   - Stratus cluster가 있으면 installer가 service namespace 밖의 실제 `/proc` mount와 login account를
     확인한다. 다른 interactive account가 있는데 host-wide `hidepid=2`가 없으면 설치를 중단하며
     packaged install에는 bypass가 없다. 수동 실행의 `-allow-argv-exposure`는 위험을 인수하는
     break-glass일 뿐 production acceptance에는 사용할 수 없다. Unit-private `ProtectProc`는 host
     사용자의 접근을 막는 증거가 아니다.
2. **전용 비특권 계정(`serverdesk`) 운영**
   - 서비스를 root나 다목적 공용 계정으로 실행하지 않고, 전용 비특권 계정(`serverdesk`)으로 제한하여 프로세스 및 상태 디렉터리(`~/.serverdesk-go` / `/var/lib/serverdesk`, 권한 `0700`/`0600`)에 대한 타 계정 접근을 원천 차단한다.
