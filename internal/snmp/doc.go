// Package snmp 는 everRun / ztC Edge 감시에 필요한 SNMPv2c GET 과
// SNMPv1/v2c 트랩 수신을 표준 라이브러리만으로 구현한 패키지다.
//
// 폐쇄망 배포가 요구사항이라 pysnmp/net-snmp 류의 외부 모듈 없이
// BER 인코딩/디코딩을 직접 수행한다(파이썬 poller.py / trap_receiver.py
// 에서 포팅). 트랩은 '상태'가 아니라 '이벤트'로 취급한다 — 헬스 판정에는
// 쓰지 않고 이벤트 피드(meta.traps[])로만 소비하는 것이 원칙이다.
package snmp
