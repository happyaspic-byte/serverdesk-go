// Package web 은 serverdesk 프런트의 정적 자산(index.html, css, js, fonts)을
// embed 로 제공한다. 바이너리 하나에 프런트가 내장돼 폐쇄망 배포가 단순해진다 —
// serve.py 시대의 '저장소 디렉터리 동반 배포' 요구가 사라진다.
package web

import "embed"

// FS 는 프런트 정적 자산의 루트다(index.html 이 루트에 있다).
//
//go:embed all:index.html all:css all:js all:fonts
var FS embed.FS
