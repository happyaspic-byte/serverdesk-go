package webauth

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
)

type loginPageData struct {
	Error string
	Next  string
}

var loginPageTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="ko">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#171310">
<title>로그인 — serverdesk</title>
<style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f1ede4;color:#251d18}
*{box-sizing:border-box}
body{min-height:100vh;margin:0;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at 20% 10%,rgba(184,92,51,.16),transparent 34%),linear-gradient(145deg,#f8f5ef,#e8e0d5)}
.login{width:min(100%,400px);padding:34px;border:1px solid #d2c5b6;border-radius:18px;background:rgba(255,253,249,.94);box-shadow:0 24px 70px rgba(52,36,25,.16)}
.brand{display:flex;align-items:center;gap:12px;margin-bottom:30px}.mark{width:38px;height:38px;border-radius:11px;display:grid;place-items:center;background:#b85c33;color:white}.mark svg{width:21px;height:21px}.brand-name{font-size:20px;font-weight:750;letter-spacing:-.02em}.brand-sub{margin-top:2px;color:#77685d;font-size:12px}
h1{font-size:23px;letter-spacing:-.035em;margin:0 0 8px}.intro{margin:0 0 24px;color:#6d5f54;font-size:14px;line-height:1.55}
label{display:block;margin:0 0 7px;font-size:13px;font-weight:650}.field{width:100%;height:44px;border:1px solid #cbbdaf;border-radius:9px;background:#fffdfa;color:#251d18;padding:0 12px;font:inherit;outline:none}.field:focus{border-color:#b85c33;box-shadow:0 0 0 3px rgba(184,92,51,.14)}.group{margin-bottom:17px}
.error{margin:0 0 18px;padding:11px 12px;border:1px solid #db9a8a;border-radius:9px;background:#fff0ec;color:#8b2d1b;font-size:13px;line-height:1.45}
button{width:100%;height:45px;border:0;border-radius:9px;background:#b85c33;color:white;font:inherit;font-weight:700;cursor:pointer}button:hover{background:#a6502d}button:focus-visible{outline:3px solid rgba(184,92,51,.32);outline-offset:2px}
.foot{margin:22px 0 0;text-align:center;color:#8a7b70;font-size:11px}
@media(prefers-color-scheme:dark){:root{background:#171310;color:#f4ede6}body{background:radial-gradient(circle at 20% 10%,rgba(184,92,51,.2),transparent 36%),linear-gradient(145deg,#1b1613,#100d0b)}.login{border-color:#3d312b;background:rgba(34,27,23,.96);box-shadow:0 24px 70px rgba(0,0,0,.42)}.brand-sub,.intro{color:#ac9d92}.field{border-color:#53443b;background:#181310;color:#f4ede6}.error{border-color:#78483c;background:#351c17;color:#ffb8a6}.foot{color:#8f8076}}
</style>
</head>
<body>
<main class="login">
  <div class="brand">
    <span class="mark" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="2" width="20" height="8" rx="2"></rect><rect x="2" y="14" width="20" height="8" rx="2"></rect><circle cx="6" cy="6" r=".7" fill="currentColor"></circle><circle cx="6" cy="18" r=".7" fill="currentColor"></circle></svg></span>
    <span><span class="brand-name">serverdesk</span><span class="brand-sub">FT infrastructure console</span></span>
  </div>
  <h1>관리자 로그인</h1>
  <p class="intro">등록 장비와 운영 정보를 보려면 로그인하세요.</p>
  {{if .Error}}<div class="error" role="alert">{{.Error}}</div>{{end}}
  <form method="post" action="/login">
    <input type="hidden" name="next" value="{{.Next}}">
    <div class="group">
      <label for="username">아이디</label>
      <input class="field" id="username" name="username" type="text" value="admin" autocomplete="username" autocapitalize="none" spellcheck="false" required>
    </div>
    <div class="group">
      <label for="password">비밀번호</label>
      <input class="field" id="password" name="password" type="password" autocomplete="current-password" autofocus required>
    </div>
    <button type="submit">로그인</button>
  </form>
  <p class="foot">Roobicom serverdesk</p>
</main>
</body>
</html>`))

func (m *Manager) writeLoginPage(w http.ResponseWriter, r *http.Request, status int, errorMessage, next string) {
	var body bytes.Buffer
	if err := loginPageTemplate.Execute(&body, loginPageData{Error: errorMessage, Next: next}); err != nil {
		http.Error(w, "로그인 화면을 만들 수 없습니다.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// no-referrer는 Chromium의 일반 form POST Origin을 null로 바꿔 동일 출처 로그인을 막는다.
	// same-origin은 외부로 Referer를 보내지 않으면서 로그인 POST에는 실제 Origin을 유지한다.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}
