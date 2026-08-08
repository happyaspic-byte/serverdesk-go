package edge

import "time"

// 폴싱 주기 상수 — Python edge_devices.py 와 동일 값을 유지한다.
// 프런트 히스토리 길이·정적 재조회 주기가 이 값에 맞춰 튜닝돼 있다.
const (
	fastSec     = 60 * time.Second // 생존 + 상태 라운드 주기
	staticEvery = 5                // N 라운드마다 정적 정보(모델·시리얼·토너 등) 재조회
	histLen     = 48               // cpu/mem 히스토리 포인트 수
	daySec      = 86400
	kstOffset   = 9 * time.Hour
)

// SNMP OID — MIB-2 / HOST-RESOURCES / Printer-MIB (표준).
const (
	oSysDescr  = "1.3.6.1.2.1.1.1.0"
	oSysName   = "1.3.6.1.2.1.1.5.0"
	oSysUptime = "1.3.6.1.2.1.1.3.0"

	oPrtDevStatus = "1.3.6.1.2.1.25.3.2.1.5.1"  // 2=running 3=warning 5=down
	oPrtStatus    = "1.3.6.1.2.1.25.3.5.1.1.1"  // 3=idle 4=printing 5=warmup
	oPrtErrState  = "1.3.6.1.2.1.25.3.5.1.2.1"  // 비트마스크 octets
	oPrtModel     = "1.3.6.1.2.1.25.3.2.1.3.1"  // hrDeviceDescr
	oPrtSerial    = "1.3.6.1.2.1.43.5.1.1.17.1" // prtGeneralSerialNumber
	oPrtPages     = "1.3.6.1.2.1.43.10.2.1.4.1.1"
	oPrtSupDesc   = "1.3.6.1.2.1.43.11.1.1.6.1."
	oPrtSupMax    = "1.3.6.1.2.1.43.11.1.1.8.1."
	oPrtSupLvl    = "1.3.6.1.2.1.43.11.1.1.9.1."
	oPrtTrayMax   = "1.3.6.1.2.1.43.8.2.1.10.1."
	oPrtTrayLvl   = "1.3.6.1.2.1.43.8.2.1.11.1."
	oPrtTrayName  = "1.3.6.1.2.1.43.8.2.1.13.1."
)

// prtErrBits — hrPrinterDetectedErrorState 첫 옥텟의 비트→이름 (MSB first).
var prtErrBits = []string{"Low paper", "No paper", "Low toner", "No toner",
	"Door open", "Jammed", "Offline", "Service requested"}

// prtErrCrit — 인쇄를 즉시 멈추는 원인. 출력물이 곧 생산물(점검 보고서·라벨)인
// 현장에서 '인쇄 불가'는 예고(warning)가 아니라 장애(critical)다.
// 잔량 부족(Low *)·도어 열림 등 예고성은 warning 유지.
var prtErrCrit = map[string]bool{"No paper": true, "No toner": true, "Jammed": true}

// Synology MIB (6574) + UCD (2021).
const (
	oSynStatus    = "1.3.6.1.4.1.6574.1.1.0"   // 1=normal 2=failed
	oSynTemp      = "1.3.6.1.4.1.6574.1.2.0"   // 시스템 온도(°C)
	oSynPower     = "1.3.6.1.4.1.6574.1.3.0"   // 1=normal 2=failed
	oSynFanSys    = "1.3.6.1.4.1.6574.1.4.1.0" // 1=normal 2=failed
	oSynFanCPU    = "1.3.6.1.4.1.6574.1.4.2.0" // 1=normal 2=failed
	oSynModel     = "1.3.6.1.4.1.6574.1.5.1.0"
	oSynSerial    = "1.3.6.1.4.1.6574.1.5.2.0"
	oSynDSM       = "1.3.6.1.4.1.6574.1.5.3.0"
	oSynUpgrade   = "1.3.6.1.4.1.6574.1.5.4.0" // 1=available
	oSynDiskID    = "1.3.6.1.4.1.6574.2.1.1.2."
	oSynDiskModel = "1.3.6.1.4.1.6574.2.1.1.3."
	oSynDiskStat  = "1.3.6.1.4.1.6574.2.1.1.5." // 1=normal 5=crashed
	oSynDiskTemp  = "1.3.6.1.4.1.6574.2.1.1.6."
	oSynRaidName  = "1.3.6.1.4.1.6574.3.1.1.2."
	oSynRaidStat  = "1.3.6.1.4.1.6574.3.1.1.3." // 1=normal 11=degraded 12=crashed

	oUCDCPUIdle  = "1.3.6.1.4.1.2021.11.11.0"  // ssCpuIdle(%)
	oUCDLA1      = "1.3.6.1.4.1.2021.10.1.3.1" // laLoad 1분
	oUCDLA5      = "1.3.6.1.4.1.2021.10.1.3.2" // laLoad 5분
	oUCDLA15     = "1.3.6.1.4.1.2021.10.1.3.3" // laLoad 15분
	oUCDMemTot   = "1.3.6.1.4.1.2021.4.5.0"    // memTotalReal(KB)
	oUCDMemAvail = "1.3.6.1.4.1.2021.4.6.0"    // memAvailReal(KB)
	oUCDMemBuf   = "1.3.6.1.4.1.2021.4.14.0"   // memBuffer(KB)
	oUCDMemCache = "1.3.6.1.4.1.2021.4.15.0"   // memCached(KB)
	oHRMemKB     = "1.3.6.1.2.1.25.2.2.0"      // hrMemorySize(KB) — Windows 포함
)

// synRaidStatusText — Synology RAID 상태 코드→텍스트 (Synology MIB 정의).
var synRaidStatusText = map[int64]string{
	1: "normal", 2: "repairing", 3: "migrating", 4: "expanding",
	5: "deleting", 6: "creating", 7: "syncing", 8: "parity checking",
	9: "assembling", 10: "canceling", 11: "degraded", 12: "crashed",
}
