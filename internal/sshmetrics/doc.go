// Package sshmetrics 는 everRun/ztc 노드의 OS 메트릭을 SSH 로 수집한다.
// Python 폴리(everrun-poller/poller.py)의 SshRunner + METRICS_SCRIPT +
// parse_metrics + 델타 계산을 옮긴 Go 포트다.
//
// 핵심 운영 규약(Python 과 동일):
//
//   - 수집은 원격 셸 스크립트(MetricsScript) 1회 실행으로 끝낸다. 스크립트는
//     원격 노드에서 돌기 때문에 바이트 단위로 고정이다(testdata 골든으로 강제).
//   - CPU/네트워크/디스크IO 는 누적 카운터의 폴 간 델타다. 첫 샘플은 기준이
//     없어 파생 메트릭을 내지 않는다.
//   - SSH 가 실패하면 Collect 는 error 만 반환하고 Metrics 를 만들지 않는다.
//     마지막 성공 값을 계속 보여주면 몇 시간 전 값이 '현재값'인 척 노출되므로,
//     파생 메트릭의 폐기(정체성 필드만 유지)는 호출자가 error 를 받았을 때
//     수행해야 할 계약이다. 직전 샘플(델타 기준)과 리부트 시각은 실패 때도
//     지우지 않는다 — 재접속 후 dt 가 긴 델타와 리부트 감지가 계속 동작해야 한다.
//
// Python 과의 의도적 차이: 깨진 숫자 행이 있으면 Python 은 예외로 샘플 전체를
// 버리지만, 여기서는 그 행만 건너뛰고 나머지 섹션은 살린다 — NIC 한 줄 쓰레기
// 때문에 CPU/메모리/디스크 전부가 null 이 되는 것보다 낫다. 정상 입력의 결과는 동일하다.
package sshmetrics
