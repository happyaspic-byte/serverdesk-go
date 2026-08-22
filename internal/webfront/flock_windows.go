//go:build windows

package webfront

import "os"

// Windows 에는 stdlib flock 이 없다(x/sys 도입은 stdlib-only 원칙 위배).
// 프로세스 내 mutex(sf.mu)는 그대로라 단일 프로세스 운용에서는 직렬화가 유지된다.
// 다만 같은 상태 파일을 여는 두 프로세스 간 상호 배제는 Unix 만큼 강하지 않다 —
// Windows 배포는 단일 인스턴스 전제로 쓴다.
func lockFile(fd *os.File) bool { return true }
func unlockFile(fd *os.File)    {}
