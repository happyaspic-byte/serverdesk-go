// Package edge — 실장비 엣지 디바이스 폴로(프린터·Synology NAS·옴론 PLC·
// Proxmox VE·일반 랙서버).
//
// sim_devices 가 만들던 것과 동일한 프런트 device 스키마(serverdesk/device@1)를
// 실측으로 채운다. /api/devices 에서 실 FT 클러스터 뒤에 append 된다.
//
// 읽기전용 계약(절대 규칙):
//   - SNMP 는 GET(v2c) 만 사용. SET 금지.
//   - FINS 는 READ 명령만 사용 — 0501(기종), 0601(상태), 0602(사이클 읽기),
//     0701(시계), 2101(에러 로그 읽기). 쓰기 계열(메모리 쓰기 0102, 모드 변경
//     0402, 사이클 초기화, 로그 클리어 2103, 강제 세트 2301 등) 절대 금지.
//   - EtherNet/IP 는 ListIdentity + Get_Attribute_Single 만 사용.
//   - Proxmox API·Redfish·SyncThru 는 GET 만 사용(POST 는 PVE 티켓 발급뿐).
//
// avcli 읽기전용 -info 원칙과 동일한 이유: 모니터링이 설비를 건드리면 안 된다.
//
// Python edge_devices.py 의 Go 이식판 — 폴 로직·상태 판정·메타 스키마·
// 센티널 값(-2 미보고, -3 잔량있음)을 원본과 같게 유지한다.
package edge
