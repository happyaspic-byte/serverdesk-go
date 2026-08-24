package sshmetrics

// MetricsScript 는 노드에서 그대로 실행되는 원격 셸 스크립트다. Python 폴리의
// METRICS_SCRIPT 를 **바이트 단위 그대로** 옮겼다 — 이 스크립트는 로컬이 아니라
// 원격 노드의 /bin/sh 에서 돌아가므로(그리고 그 출력 형식에 파서가 맞춰져 있으므로)
// 공백 하나라도 바꾸면 안 된다. testdata/metrics_script.golden 과의 동일성을
// TestMetricsScriptMatchesGolden 이 강제한다.
//
// 운영상 주의(Python 주석에서 이어받음):
//   - @stat 의 intr 행은 수 KB 라 grep 에서 반드시 제외한다.
//   - @disk 는 loop 마운트 ISO(항상 100%) 오탐 방지로 iso9660/udf 를 제외한다.
//   - @link 는 파이프 고정 5필드다. 공백 구분이면 speed 가 비었을 때 필드가 밀려
//     phy/virt 마커 위치가 틀어져 물리 NIC 판별이 조용히 깨진다.
//   - @spine 은 원격에서 fork 를 만들지 않으려고 read/echo 빌트인만 쓴다.
//   - @tz 는 avcli alert 시각(TZ 없는 노드 로컬시각)의 UTC 환산에 필요하다.
const MetricsScript = `echo "T=$(date +%s)";echo "@stat"; grep -E "^(cpu |ctxt|procs_running|procs_blocked)" /proc/stat;echo "@load"; cat /proc/loadavg;echo "@up"; cat /proc/uptime;echo "@mem"; grep -E "^(MemTotal|MemFree|MemAvailable|Buffers|Cached|SwapTotal|SwapFree|Dirty):" /proc/meminfo;echo "@disk"; df -PB1 -x tmpfs -x devtmpfs -x squashfs -x iso9660 -x udf 2>/dev/null | tail -n +2;echo "@diskio"; grep -E " (sd[a-z]|nvme[0-9]n[0-9]|md[0-9]) " /proc/diskstats;echo "@net"; tail -n +3 /proc/net/dev;echo "@temp"; for d in /sys/class/hwmon/hwmon*; do n=$(cat $d/name 2>/dev/null); for f in $d/temp*_input; do [ -e "$f" ] || continue; echo "$n:$(cat ${f%_input}_label 2>/dev/null || basename $f)=$(cat $f)"; done; done;echo "@link"; for i in /sys/class/net/*; do echo "$(basename $i)|$(cat $i/operstate 2>/dev/null)|$(cat $i/speed 2>/dev/null)|$([ -e $i/device ] && echo phy || echo virt)|$(cat $i/mtu 2>/dev/null)"; done;echo "@spine"; S=/etc/opt/ft/spine; for f in /etc/opt/ft/node-config-uuid $S/networks/*/name $S/networks/*/role $S/networks/*/ordinal $S/networks/*/mtu $S/networks/*/bridge_name $S/nodes/*/networks/*/name $S/nodes/*/networks/*/parent_uuid; do if [ -f "$f" ]; then v=""; read -r v < "$f"; echo "F|$f|$v"; fi; done;echo "@md"; grep -E "^(md|.*\[)" /proc/mdstat;echo "@drbd"; grep -E "^ *[0-9]+: cs:" /proc/drbd 2>/dev/null;echo "@vm"; virsh --readonly list --name 2>/dev/null | grep -v "^$";echo "@tz"; date +%z; date +%Z;echo "@plat"; if [ -f /etc/opt/ft/is_ztc ]; then echo ztc; else echo everrun; fi
`
