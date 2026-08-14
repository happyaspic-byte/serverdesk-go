# ztC Endurance 정보 수집 가이드

이 문서는 ztC Endurance의 시스템 상태와 각 Compute Module의 BMC 상태를 읽기 전용으로 수집하기 위한 운영 기준이다. 실제 주소, 계정, 비밀번호, 일련번호는 문서나 Git에 기록하지 않는다.

## 1. 주소와 계정의 역할

Windows 기반 Endurance 설치는 일반적으로 11개 관리 주소를 사용한다.

| 구분 | 인터페이스 | 용도 |
|---|---|---|
| BMC A | LAN channel 1 / 8 | Compute Module A의 센서, SEL, FRU, KVM |
| BMC B | LAN channel 1 / 8 | Compute Module B의 센서, SEL, FRU, KVM |
| Standby A | eno1 / eno2 | Compute Module A의 Standby OS SSH |
| Standby B | eno1 / eno2 | Compute Module B의 Standby OS SSH |
| Windows host | host interface | Windows 운영체제 관리 |
| Windows management | interface 1 / 2 | ztC Endurance Console |

계정도 서로 분리된다.

- **BMC Web/IPMI 관리자**: 각 BMC의 Web, IPMI, 제한형 SSH 관리 인터페이스에 사용한다.
- **Redfish 관리자**: 설치 과정에서 별도로 설정되는 BMC Redfish 계정이다. BMC Web 계정과 같다고 가정하지 않는다.
- **Standby OS 관리자**: Standby Linux의 `zenadmin` 계정이다.
- **Windows 관리자**: Windows host 관리 계정이다.
- **ztC Endurance Console 사용자**: Console의 로컬/연동 사용자다.

BMC 비밀번호를 BMC Web에서 임의로 변경하지 않는다. ztC Endurance Console의 암호 변경 절차 또는 Stratus의 `cfgpasswd --bmcadmin` 절차를 사용해야 시스템 관리 소프트웨어의 IPMI 자격증명과 일치한다.

## 2. 권장 수집 우선순위

1. **ztC Endurance Console SNMPv3**: 시스템, Compute/Storage Module, 디스크, 경보의 정식 통합 수집 경로다.
2. **ztC Endurance Console OPC UA**: inventory, health, performance를 OPC UA 클라이언트에 제공한다. 기본 포트는 4840이다.
3. **각 BMC의 IPMI**: Console이 내려가도 온도, 전압, 팬, PSU, 전원 상태, SEL, FRU를 수집할 수 있다.
4. **각 BMC의 Web Console**: 센서, System Inventory, FRU, Audit/Event Log, KVM을 사람이 점검할 때 사용한다.
5. **BMC Redfish**: 별도 Redfish 계정을 확보한 경우에만 사용한다.
6. **Standby OS SSH**: 네트워크와 관리 서비스 진단용이다. 제품 전체 상태의 정본으로 사용하지 않는다.

두 BMC는 자기 Compute Module의 정보만 보고하므로 항상 A와 B를 모두 수집한다.

## 3. BMC IPMI 읽기 전용 수집

비밀번호를 명령행 인수에 넣지 않는다. `ipmitool`은 대화형 입력을 사용한다.

```bash
ipmitool -I lanplus -H "$BMC_IP" -U admin -a chassis status
ipmitool -I lanplus -H "$BMC_IP" -U admin -a mc info
ipmitool -I lanplus -H "$BMC_IP" -U admin -a sensor list
ipmitool -I lanplus -H "$BMC_IP" -U admin -a sdr elist all
ipmitool -I lanplus -H "$BMC_IP" -U admin -a sel elist
ipmitool -I lanplus -H "$BMC_IP" -U admin -a fru print
```

자동 수집기는 다음 주기를 권장한다.

- 전원/전체 health: 30~60초
- 온도/전압/팬/PSU: 60초
- SEL 증분: 60초
- FRU/firmware/inventory: 6~24시간

SEL을 읽는 과정에서 지우지 않는다. `sel clear`, power, reset, identify, raw write 명령은 운영 수집기에서 금지한다.

## 4. BMC Web과 SMASH SSH

BMC Web의 일반적인 읽기 전용 화면은 다음과 같다.

- Dashboard
- Sensor
- System Inventory
- FRU Information
- Logs & Reports
- Remote Control의 H5Viewer

MegaRAC Web 내부 `/api/*` 경로는 firmware별 비공개 구현이므로 장기 수집 계약으로 사용하지 않는다. API를 진단 목적으로 사용할 때도 GET/로그인/로그아웃만 허용하고, 설정·전원·firmware endpoint는 호출하지 않는다.

BMC SSH가 SMASHLITE를 제공하는 경우 안전한 탐색 명령은 다음과 같다.

```text
show /
show /system/
show /system/summary/
help
version
exit
```

`set`과 `reset` verb는 점검 중 사용하지 않는다. 일부 SMASH aggregate는 지원되지 않는 항목을 `Unknown` 또는 `Fail`로 표시할 수 있으므로 IPMI sensor health와 Web Sensor 화면으로 교차 확인한다.

## 5. Redfish

서비스 root는 인증 없이 보일 수 있지만 Systems, Chassis, Managers 등 실제 resource는 별도 Redfish 계정을 요구한다.

```bash
curl --fail --silent --show-error --user "$REDFISH_USER" \
  "https://$BMC_IP/redfish/v1/Systems"
```

- BMC Web 관리자 또는 Standby 계정이 Redfish에서도 동작한다고 가정하지 않는다.
- 자체 서명/기본 인증서를 무조건 `-k`로 우회하지 않는다.
- 운영에서는 신뢰 CA를 설치하거나 인증서 fingerprint와 대상 IP를 함께 검증한다.
- Redfish 비밀번호 변경은 ztC Endurance Console의 **Administrative Tools → Change Passwords → Redfish** 절차를 사용한다.

## 6. Standby OS SSH 진단

Standby 주소에 `zenadmin`으로 접속한 뒤 읽기 전용으로 역할과 네트워크를 확인한다.

```bash
hostnamectl
ip -brief address
ip route
systemctl --no-pager --type=service --state=running
ss -lntup
```

SNMP 구성요소가 설치된 Management VM/OS에서는 다음 위치를 확인한다.

```text
/usr/share/snmp/mibs/STRATUS-ZTC-ENDURANCE-MIB.txt
/opt/stratus/sbin/endurancesnmpagent
```

Standby OS에서 agent가 없거나 inactive인 것은 시스템 Console의 SNMP 서비스 상태와 같지 않을 수 있다. Console 설정과 실제 161/udp 응답을 함께 확인한다.

## 7. SNMP와 OPC UA

### SNMP

- 가능하면 SNMPv3 authPriv를 사용한다.
- 161/udp는 GET/WALK, 162/udp는 trap이다.
- trap source 주소, engine ID, 사용자, 시간창을 검증한다.
- community 없는 trap 또는 SNMPv2c를 고객망 기본값으로 사용하지 않는다.
- MIB는 고객 서비스 포털에서 해당 Endurance release와 일치하는 파일을 받는다.

### OPC UA

- 기본 endpoint 예: `opc.tcp://<console-management-ip>:4840/`
- 익명/None 보안은 격리된 검증망에서만 허용한다.
- 운영에서는 Console이 지원하는 가장 강한 security policy와 사용자 인증을 사용한다.

## 8. 수집 결과 정규화

수집기는 임의 값이나 demo fallback을 만들지 않는다. 응답하지 않는 항목은 `unavailable`과 마지막 성공 시각으로 표현한다.

권장 공통 필드:

```text
source              ipmi | redfish | snmp | opcua | web-diagnostic
module              compute-a | compute-b | system
collected_at         UTC timestamp
reachable            true | false
power_state          on | off | unknown
health               ok | warning | critical | unknown
metric_name          vendor 원본 이름과 정규화 이름
value / unit
thresholds
states
firmware
last_event_id
error_code / error
```

동일 지표가 충돌하면 정식 시스템 경로(SNMP/OPC UA), BMC IPMI, Web/SMASH 진단 순으로 표시하고 원본 source를 보존한다.

## 9. 보안 기준

- BMC, Standby, Windows, Console, Redfish 자격증명을 분리한다.
- 자격증명은 verifier/secret store에 보관하고 Git, config export, 로그, screenshot에 넣지 않는다.
- BMC/Console을 인터넷에 직접 공개하지 않는다.
- 관리망 ACL은 collector와 운영 jump host만 허용한다.
- Web/KVM/SSH 세션을 점검 후 종료한다.
- BMC audit log와 SEL은 중앙 로그로 보존하되 일련번호·MAC·사용자/IP가 포함된 원본은 접근을 제한한다.
- BMC 시간 동기화와 인증서 SAN을 확인한다. 시간 불일치는 SEL 상관분석을 깨뜨린다.

## 10. 장애 판정

- ARP/ICMP 실패만으로 down을 확정하지 않는다. 다중 NIC는 다른 물리망에 연결될 수 있다.
- BMC가 reachable이고 host power가 off이면 Standby/Windows/Console 주소가 내려가는 것은 자연스럽다.
- host power가 on인데 KVM이 검은 화면이고 Standby/Console 포트가 계속 닫혀 있으면 POST/부팅 상태와 SEL을 확인한다.
- Web dashboard의 `Host Offline`, SMASH aggregate `Fail`, IPMI overall health가 서로 다를 수 있으므로 센서별 상태와 SEL을 우선한다.
- 전원 상태 변경이나 SEL 삭제는 조사 범위에 포함하지 않는다.

## 공식 문서

- [Overview of the BMC Web Console](https://ztcendurancedoc.stratus.com/WIN/WIN-3.0.0.0/en-us_html/Content/Help/P02_Soft/C09_BMC/N_BMCOver.htm)
- [Connecting to the BMC Web Console](https://ztcendurancedoc.stratus.com/WIN/WIN-3.0.0.0/en-us_html/Content/Help/P02_Soft/C09_BMC/T_BMCConn.htm)
- [Changing Passwords on a ztC Endurance System](https://ztcendurancedoc.stratus.com/ORLX/ORLX-2.0.0.0/en-us_html/Content/Help/P02_Soft/C08_zenConsole/S02_Users/T_ChangePWs.htm)
- [Configuring SNMP Settings](https://ztcendurancedoc.stratus.com/E/1.1.0.3/en-us_html/Content/Help/P02_Soft/C08_zenConsole/S05_SNMP/T_ConfigSNMP.htm)
- [Configuring OPC Settings](https://ztcendurancedoc.stratus.com/E/1.1.0.3/en-us_html/Content/Help/P02_Soft/C08_zenConsole/T_ConfigOPC.htm)
- [Windows installation network fields](https://ztcendurancedoc.stratus.com/WIN/WIN-2.0.0.0/en-us_html/Content/Help/P02_Soft/C07_SoftInstall/W/S01_Inst/T_W_InstallPrep.htm)
