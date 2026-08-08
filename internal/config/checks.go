package config

import (
	"fmt"
	"os"
	"os/user"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// CheckPerms 는 비밀번호가 든 설정 파일이 group/other 에게 읽기·쓰기로 열여 있는지
// 검사한다(poller.py check_config_perms: S_IROTH|S_IWOTH|S_IRGRP).
// 느슨하면 nil 이 아닌 에러를 돌려주니 호출자가 warn 로그로 남기면 된다
// (기동을 막지는 않는다 — poller.py 도 경고만 한다).
func CheckPerms(path string) error {
	// Windows 는 POSIX 권한 비트가 없어 Perm() 이 항상 느슨하게 보인다 — 점검 자체를 건너뛴다.
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if st.Mode().Perm()&0o046 != 0 {
		return fmt.Errorf("설정 파일 권한이 느슨합니다(chmod 600 권장): %s", path)
	}
	return nil
}

var hidepidRe = regexp.MustCompile(`\b(?:hidepid=(\w+)|subset=(\w+))`)

// parseProcMounts 는 /proc/mounts 내용에서 /proc 의 hidepid/subset 마운트 옵션을
// 읽는다. 없으면 "". poller.py 와 같이 첫 번째 /proc 엔트리만 본다.
func parseProcMounts(content string) string {
	for _, ln := range strings.Split(content, "\n") {
		p := strings.Fields(ln)
		if len(p) >= 4 && p[1] == "/proc" && p[2] == "proc" {
			if m := hidepidRe.FindStringSubmatch(p[3]); m != nil {
				if m[1] != "" {
					return m[1]
				}
				return "subset=" + m[2]
			}
			return ""
		}
	}
	return ""
}

func procHidepid() string {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	return parseProcMounts(string(data))
}

// parsePasswd 는 /etc/passwd 내용에서 폴러 소유자·시스템 계정을 뺀
// "로그인 가능한" 일반 계정(uid 1000~65533, 셸이 nologin/false/sync 로 끝나지 않음)
// 목록을 돌려준다. poller.py _other_local_users 와 동일 기준.
func parsePasswd(content, me string) []string {
	var out []string
	for _, ln := range strings.Split(content, "\n") {
		p := strings.Split(ln, ":")
		if len(p) < 7 {
			continue
		}
		uid, err := strconv.Atoi(p[2])
		if err != nil {
			continue
		}
		if uid < 1000 || uid >= 65534 {
			continue
		}
		if p[0] == me {
			continue
		}
		sh := p[6]
		if strings.HasSuffix(sh, "nologin") || strings.HasSuffix(sh, "false") || strings.HasSuffix(sh, "sync") {
			continue
		}
		out = append(out, p[0])
	}
	return out
}

func otherLocalUsers() []string {
	me := os.Getenv("USER")
	if me == "" {
		if u, err := user.Current(); err == nil {
			me = u.Username
		}
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil
	}
	return parsePasswd(string(data), me)
}

// evalArgvExposure 는 CheckArgvExposure 의 순수 판정부다(테스트용으로 분리).
func evalArgvExposure(hidepid string, protected bool, others []string, allow bool) (warn string, err error) {
	if protected {
		return fmt.Sprintf("argv 노출 점검: /proc 이 보호됨(%s)", hidepid), nil
	}
	if len(others) == 0 {
		return "/proc 에 hidepid 가 없습니다. 현재는 다른 로그인 계정이 없어 노출 위험이 낮지만, " +
			"운영 배치 전에 hidepid=2 를 반드시 적용하고 everRun 에 폴링 전용 읽기 전용 계정을 만드십시오(README §7.9).", nil
	}
	shown := others
	if len(shown) > 5 {
		shown = shown[:5]
	}
	msg := fmt.Sprintf("보안: avcli 호출 시 EAC admin 암호가 명령행에 노출되는데 /proc 에 "+
		"hidepid=2(또는 subset=pid)가 없고 다른 로그인 계정이 존재합니다(%s). "+
		"이 호스트의 임의 사용자가 `ps -eo args` 로 암호를 읽을 수 있습니다. "+
		"hidepid=2 로 /proc 을 재마운트하고 폴러 전용 UID 를 쓰십시오.", strings.Join(shown, ", "))
	if allow {
		return msg + " — --allow-argv-exposure 가 지정되어 강제로 계속합니다.", nil
	}
	return "", fmt.Errorf("ERROR: %s 그래도 기동하려면 --allow-argv-exposure 를 지정하십시오", msg)
}

// CheckArgvExposure 는 avcli 자격증명이 명령행(argv)에 노출되는 배포 조건을 점검한다.
//
// avcli 는 `-p <암호>` 외에 stdin/환경변수로 암호를 받는 수단이 없다(--help 확인).
// 따라서 폴링 중 `ps -eo args` 에 EAC admin 암호가 그대로 보인다. 코드로는 없앨 수
// 없으므로 배포 조건(/proc hidepid=2 또는 다른 로그인 계정 부재)을 강제한다.
// 노드 root 암호는 SSH_ASKPASS+환경변수로 넘어가 argv 에 남지 않는다(poller.py 주석 인용).
//
// 반환: warn 은 기동은 가능하지만 남길 경고/정보 메시지, err 는 기동 거부 사유.
// allow=true 면 위험 조건에서도 err 대신 warn 으로 돌려준다(--allow-argv-exposure).
func CheckArgvExposure(allow bool) (warn string, err error) {
	// /proc·/etc/passwd 기반 점검은 Unix 전용 — Windows 는 프로세스 모델이 달라 생략한다.
	if runtime.GOOS == "windows" {
		return "", nil
	}
	hp := procHidepid()
	protected := hp == "2" || hp == "invisible" || hp == "ptraceable" || hp == "subset=pid"
	return evalArgvExposure(hp, protected, otherLocalUsers(), allow)
}
