package lib

/**
*** FILE: auth_session_test.go
***   覆盖「七天免密 + 登出即失效 + 令牌被盗的止血手段」这几条承诺
**/

// 这批测试钉的是**行为**而不是实现：
//   · 验证一次密码，七天内不用再输（TestSessionIssuedForSevenDays）；
//   · 登出之后，已经发出去的令牌立刻不能再用（TestLogoutRevokesSessionImmediately）；
//   · 续签会把旧令牌换掉 —— 复制件在正版持有人下一次续签之后失灵（TestSessionRefreshRotates）；
//   · 令牌有这么长寿命，所以必须有绝对上限，到点必须重新输密码（TestSessionAbsoluteLifetime）；
//   · 有人在令牌被换掉很久之后还在用它 → 判定为复制件，整族作废（TestRotatedTokenReuseKillsFamily）；
//   · Cookie 通道不会被第三方站点借走（TestCookieRequiresSameSiteRequest）；
//   · 一次猜中就能用七天，所以猜密码必须变慢（TestLoginThrottle）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newSessionTestServer 造一台带会话表的服务器。
// 走 NewClipboardServer（而不是直接拼结构体）是因为**会话表要落盘**：
// 只有真正跑一次启动流程，才能顺带验到「会话能从磁盘读回来」这条路径。
func newSessionTestServer(t *testing.T, password string) *ClipboardServer {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Server.Auth = password
	cfg.Server.StorageDir = dir
	cfg.Server.HistoryFile = filepath.Join(dir, "history.json")

	s, err := NewClipboardServer(cfg)
	if err != nil {
		t.Fatalf("创建服务器失败: %v", err)
	}
	t.Cleanup(s.stopSessionMaintenance)
	return s
}

type authTokenResponse struct {
	Token       string `json:"token"`
	Delivery    string `json:"delivery"`
	ExpiresAt   int64  `json:"expiresAt"`
	AbsoluteExp int64  `json:"absoluteExpiresAt"`
	Scope       string `json:"scope"`
	SessionID   string `json:"sessionId"`
}

// requestToken 走一遍真实的登录接口（而不是直接调内部函数）——
// 「七天有效期」这条承诺，用户感知到的就是这一个 HTTP 响应。
func requestToken(t *testing.T, s *ClipboardServer, room, password, delivery string) (authTokenResponse, *httptest.ResponseRecorder) {
	t.Helper()
	if room == "" {
		room = "default"
	}
	body := map[string]interface{}{"password": password}
	if delivery != "" {
		body["delivery"] = delivery
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/token?room="+room, strings.NewReader(string(payload)))
	w := httptest.NewRecorder()
	s.handleAuthToken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败，状态码 %d: %s", w.Code, w.Body.String())
	}
	var resp authTokenResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}
	return resp, w
}

func TestSessionIssuedForSevenDays(t *testing.T) {
	s := newSessionTestServer(t, "pw-7d")
	now := time.Now().Unix()

	resp, _ := requestToken(t, s, "default", "pw-7d", "")

	if resp.ExpiresAt <= now {
		t.Fatal("会话应当在未来某个时刻才过期")
	}
	days := float64(resp.ExpiresAt-now) / 86400
	if days < 6.9 || days > 7.1 {
		t.Fatalf("默认应该是 7 天有效期，实际 %.2f 天", days)
	}
	if resp.AbsoluteExp <= resp.ExpiresAt {
		t.Fatalf("绝对生存期(%d) 必须晚于滑动有效期(%d)", resp.AbsoluteExp, resp.ExpiresAt)
	}
	if resp.SessionID == "" {
		t.Fatal("会话必须带上 sid —— 吊销和轮换都靠它")
	}
	if resp.Scope != "global" {
		t.Fatal("平台密码换来的应当是全局会话")
	}

	// 会话要真的落到服务端：否则「登出」无从谈起
	if s.sessionRecord(resp.SessionID) == nil {
		t.Fatal("会话没有登记到服务端")
	}
	if !s.validateRoomSessionToken("default", resp.Token) {
		t.Fatal("刚签发的会话令牌应当可用")
	}
	if !s.validateRoomSessionToken("finance", resp.Token) {
		t.Fatal("全局会话令牌应当对所有房间有效")
	}
}

func TestLogoutRevokesSessionImmediately(t *testing.T) {
	s := newSessionTestServer(t, "pw-logout")

	resp, _ := requestToken(t, s, "default", "pw-logout", "")
	if !s.validateRoomSessionToken("default", resp.Token) {
		t.Fatal("前提：登录之后令牌应当可用")
	}

	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(`{}`))
	logout.Header.Set("Authorization", "Bearer "+resp.Token)
	w := httptest.NewRecorder()
	s.handleAuthLogout(w, logout)
	if w.Code != http.StatusOK {
		t.Fatalf("登出应当返回 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Revoked int `json:"revoked"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("解析登出响应失败: %v", err)
	}
	if result.Revoked == 0 {
		t.Fatal("登出必须真的吊销到会话")
	}

	// 关键断言：同一枚令牌，登出之后立刻不能用
	if s.validateRoomSessionToken("default", resp.Token) {
		t.Fatal("登出之后这枚令牌必须立刻失效")
	}
	// 已经吊销的令牌不能拿来续签（否则登出形同虚设）
	refresh := httptest.NewRequest(http.MethodPost, "/auth/token/refresh?room=default", strings.NewReader(`{}`))
	refresh.Header.Set("Authorization", "Bearer "+resp.Token)
	rw := httptest.NewRecorder()
	s.handleAuthTokenRefresh(rw, refresh)
	if rw.Code == http.StatusOK {
		t.Fatal("已吊销的令牌不该还能续签")
	}
}

func TestSessionRefreshRotates(t *testing.T) {
	s := newSessionTestServer(t, "pw-rotate")

	original, _ := requestToken(t, s, "default", "pw-rotate", "")

	req := httptest.NewRequest(http.MethodPost, "/auth/token/refresh?room=default", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+original.Token)
	w := httptest.NewRecorder()
	s.handleAuthTokenRefresh(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("续签应当成功，实际 %d: %s", w.Code, w.Body.String())
	}
	var refreshed authTokenResponse
	if err := json.NewDecoder(w.Body).Decode(&refreshed); err != nil {
		t.Fatalf("解析续签响应失败: %v", err)
	}
	if refreshed.Token == "" {
		t.Fatal("续签应当返回新令牌")
	}
	if refreshed.SessionID == original.SessionID {
		t.Fatal("续签必须换一枚新的会话 id —— 不换就无法区分「谁的令牌被复制了」")
	}
	if !s.validateRoomSessionToken("default", refreshed.Token) {
		t.Fatal("新令牌应当可用")
	}
	// 续签不延长绝对生存期
	if refreshed.AbsoluteExp != original.AbsoluteExp {
		t.Fatalf("绝对生存期不该因为续签而变长: %d -> %d", original.AbsoluteExp, refreshed.AbsoluteExp)
	}
	// 旧令牌被标记成「已轮换」，但**仍在宽限期内**：此刻还放行，
	// 这样刷新那一刻正在上传的文件不会被腰斩。
	if parent := s.sessionRecord(original.SessionID); parent == nil || parent.RotatedAt == 0 {
		t.Fatal("旧会话应当被标记为已轮换")
	}
	if !s.validateRoomSessionToken("default", original.Token) {
		t.Fatal("宽限期内的旧令牌应当仍然可用")
	}
}

func TestRotatedTokenReuseKillsFamily(t *testing.T) {
	s := newSessionTestServer(t, "pw-theft")

	original, _ := requestToken(t, s, "default", "pw-theft", "")
	req := httptest.NewRequest(http.MethodPost, "/auth/token/refresh?room=default", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+original.Token)
	w := httptest.NewRecorder()
	s.handleAuthTokenRefresh(w, req)
	var refreshed authTokenResponse
	if err := json.NewDecoder(w.Body).Decode(&refreshed); err != nil {
		t.Fatalf("解析续签响应失败: %v", err)
	}

	// 把「旧令牌被换掉」的时刻往前挪，模拟：宽限期早就过了，还有人拿着旧令牌来。
	// 正版持有人此刻手里的令牌已经是 refreshed.Token 了。
	st := s.sessionStore
	st.mu.Lock()
	if parent := st.sessions[original.SessionID]; parent != nil {
		parent.RotatedAt = time.Now().Unix() - rotatedSessionGraceSeconds - 10
		st.dirty = true
	} else {
		st.mu.Unlock()
		t.Fatal("找不到旧会话记录")
	}
	st.mu.Unlock()

	if s.validateRoomSessionToken("default", original.Token) {
		t.Fatal("宽限期之后还在用旧令牌，不该被放行")
	}
	if s.validateRoomSessionToken("default", refreshed.Token) {
		t.Fatal("检测到令牌被复制之后应当整个会话族作废（正版持有人也只能重新登录一次）")
	}
}

func TestSessionAbsoluteLifetime(t *testing.T) {
	s := newSessionTestServer(t, "pw-lifetime")

	// 家族诞生时间写早一点 → 已经活过绝对生存期，必须拒绝续签。
	// 这条不依赖服务端会话表有没有（它写在令牌自己的 payload 里，签过名改不了）。
	_, err := s.openSession("default", sessionSeed{
		FamilyID:    "ancient-family",
		FamilyStart: time.Now().Unix() - int64(s.sessionLifetimeSeconds()) - 60,
		Scope:       "global",
		Via:         "refresh",
	})
	if err != errSessionLifetimeExceeded {
		t.Fatalf("超过绝对生存期应当被拒绝，实际 err=%v", err)
	}

	// 还在期限内 → 正常签发，但到期时刻不会越过绝对上限
	issued, err := s.openSession("default", sessionSeed{
		FamilyID:    "fresh-family",
		FamilyStart: time.Now().Unix() - 10,
		Scope:       "global",
	})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if issued.AbsoluteExp-time.Now().Unix() > int64(s.sessionLifetimeSeconds()) {
		t.Fatal("会话到期时刻不该越过绝对生存期")
	}
}

func TestCookieDeliveryAndSameSite(t *testing.T) {
	s := newSessionTestServer(t, "pw-cookie")

	resp, loginRec := requestToken(t, s, "default", "pw-cookie", deliveryCookie)
	if resp.Token != "" {
		t.Fatal("Cookie 模式不该把令牌再塞进响应体 —— 浏览器用不着，多一条路径就多一处泄露风险")
	}
	if resp.Delivery != deliveryCookie {
		t.Fatalf("delivery 应当回显 cookie，实际 %q", resp.Delivery)
	}

	setCookie := loginRec.Header().Get("Set-Cookie")
	if setCookie == "" {
		t.Fatal("Cookie 模式必须下发 Set-Cookie")
	}
	if !strings.Contains(setCookie, "HttpOnly") {
		t.Fatal("会话 Cookie 必须是 HttpOnly：否则页面上任意 XSS 都能把七天登录态偷走")
	}
	if !strings.Contains(setCookie, "SameSite=Lax") {
		t.Fatal("会话 Cookie 必须是 SameSite=Lax")
	}
	// 局域网最常见的是 http://ip:port，这里不能加 Secure，否则浏览器直接丢掉 Cookie
	if strings.Contains(setCookie, "Secure") {
		t.Fatal("HTTP 下不该加 Secure")
	}

	// 把 Set-Cookie 原样装回请求 —— 这就是浏览器下个请求的样子
	cookies := (&http.Response{Header: loginRec.Header()}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("解析 Set-Cookie 失败")
	}
	legit := httptest.NewRequest(http.MethodGet, "/file/u/a", nil)
	legit.AddCookie(cookies[0])
	legit.Header.Set("Sec-Fetch-Site", "same-origin")
	if !s.canAccessFile(legit, "default", "u") {
		t.Fatal("带着 Cookie 的同源请求应当通过鉴权")
	}

	// 第三方站点让浏览器发出的请求：Cookie 不会带来任何权限
	evil := httptest.NewRequest(http.MethodPost, "/text?room=default", strings.NewReader("stolen"))
	evil.AddCookie(cookies[0])
	evil.Header.Set("Sec-Fetch-Site", "cross-site")
	evil.Header.Set("Origin", "https://evil.example")
	if s.canAccessRoom("default", extractAuthToken(evil)) {
		t.Fatal("跨站请求不能借 Cookie 里的会话令牌")
	}
}

func TestLoginThrottle(t *testing.T) {
	s := newSessionTestServer(t, "pw-throttle")

	for i := 0; i < loginFailLimit; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/token?room=default", strings.NewReader(`{"password":"wrong"}`))
		s.handleAuthToken(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错密码应当返回 401，实际 %d", i+1, w.Code)
		}
	}

	locked := httptest.NewRecorder()
	s.handleAuthToken(locked, httptest.NewRequest(http.MethodPost, "/auth/token?room=default", strings.NewReader(`{"password":"wrong"}`)))
	if locked.Code != http.StatusTooManyRequests {
		t.Fatalf("连续猜错应当被限速，实际 %d", locked.Code)
	}

	// 锁着的时候，**正确的密码也不该通** —— 否则限速只是摆设
	right := httptest.NewRecorder()
	s.handleAuthToken(right, httptest.NewRequest(http.MethodPost, "/auth/token?room=default", strings.NewReader(`{"password":"pw-throttle"}`)))
	if right.Code == http.StatusOK {
		t.Fatal("限锁定期间不该允许任何登录（含正确密码）")
	}
}

func TestAuthSessionsListAndRevoke(t *testing.T) {
	s := newSessionTestServer(t, "pw-sessions")

	first, _ := requestToken(t, s, "default", "pw-sessions", "")
	second, _ := requestToken(t, s, "default", "pw-sessions", "")

	listReq := httptest.NewRequest(http.MethodGet, "/auth/sessions", nil)
	listReq.Header.Set("Authorization", "Bearer "+first.Token)
	w := httptest.NewRecorder()
	s.handleAuthSessions(w, listReq)
	if w.Code != http.StatusOK {
		t.Fatalf("列出会话应当成功，实际 %d: %s", w.Code, w.Body.String())
	}
	var listed struct {
		Sessions []sessionView `json:"sessions"`
		TTL      int           `json:"ttl"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("解析会话列表失败: %v", err)
	}
	if len(listed.Sessions) < 2 {
		t.Fatalf("应当看得到两条会话，实际 %d", len(listed.Sessions))
	}
	if listed.TTL != defaultSessionTTLSeconds {
		t.Fatalf("列表应当回显当前 ttl，实际 %d", listed.TTL)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/auth/sessions?sid="+second.SessionID, nil)
	delReq.Header.Set("Authorization", "Bearer "+first.Token)
	dw := httptest.NewRecorder()
	s.handleAuthSessions(dw, delReq)
	if dw.Code != http.StatusOK {
		t.Fatalf("吊销指定会话应当成功，实际 %d: %s", dw.Code, dw.Body.String())
	}

	if s.validateRoomSessionToken("default", second.Token) {
		t.Fatal("被踢掉的那台设备应当立刻掉线")
	}
	if !s.validateRoomSessionToken("default", first.Token) {
		t.Fatal("踢掉别人不该把自己也踢下去")
	}
}

// TestSessionCookieKeepsOtherRooms Cookie 里一个房间一个坑。
// 只留一条的话，「先登录公共房间、再进财务房间」会把第一个挤掉 ——
// 用户切回去又要输一次密码，正是这次要消灭的体感。
func TestSessionCookieKeepsOtherRooms(t *testing.T) {
	s := newSessionTestServer(t, "pw-rooms")
	s.config.Server.RoomAuth = RoomAuthConfig{
		"private": RoomAuthEntry{Password: "priv-pass"},
		"finance": RoomAuthEntry{Password: "fin-pass"},
	}

	// 两间房都用**房间专属**密码登录 —— 这样令牌是房间专属的，
	// 于是「能不能进某个房间」就能反过来证明 Cookie 里到底留着哪几条令牌。
	_, loginOne := requestToken(t, s, "private", "priv-pass", deliveryCookie)
	cookies := (&http.Response{Header: loginOne.Header()}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("第一间房登录后应当下发 Cookie")
	}

	// 第二间房：带着第一间的 Cookie 去登录 —— 服务端要把两条合进同一个 Cookie
	loginTwo := httptest.NewRequest(http.MethodPost, "/auth/token?room=finance", strings.NewReader(`{"password":"fin-pass","delivery":"cookie"}`))
	loginTwo.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	s.handleAuthToken(w, loginTwo)
	if w.Code != http.StatusOK {
		t.Fatalf("第二间房登录失败: %d %s", w.Code, w.Body.String())
	}
	merged := (&http.Response{Header: w.Header()}).Cookies()
	if len(merged) == 0 {
		t.Fatal("合并后应当仍只有一个 Cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/file/u/a", nil)
	req.AddCookie(merged[0])
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if tokens := readSessionCookieTokens(req); len(tokens) != 2 {
		t.Fatalf("Cookie 里应当保留两间房的令牌，实际 %d 条", len(tokens))
	}

	okPrivate, okFinance := false, false
	for _, token := range extractAuthTokens(req) {
		if s.canAccessRoom("private", token) {
			okPrivate = true
		}
		if s.canAccessRoom("finance", token) {
			okFinance = true
		}
	}
	if !okPrivate || !okFinance {
		t.Fatalf("两间房都应当还能进（private=%v finance=%v）", okPrivate, okFinance)
	}
	// 反证：Cookie 里没有全局令牌，所以第三个房间进不去 ——
	// 这条挡住「为了合并方便，把房间令牌统统升级成全局令牌」那种偷懒实现。
	if s.canAccessRoom("default", extractAuthToken(req)) {
		t.Fatal("房间专属令牌不该顺带打通别的房间")
	}
}

// TestSessionsSurviveRestart 会话要能从磁盘读回来 ——
// 容器重启一次（哪怕只是升级镜像）就让所有人重新输密码，那七天承诺是假的。
func TestSessionsSurviveRestart(t *testing.T) {
	s := newSessionTestServer(t, "pw-persist")
	issued, err := s.openSession("default", sessionSeed{Scope: "global", Via: "password", UserAgent: "curl/8", IP: "10.0.0.9"})
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if !pathExists(s.sessionStorePath()) {
		t.Fatalf("会话表没有落到磁盘: %s", s.sessionStorePath())
	}

	reloaded := newSessionStore(s.sessionStorePath(), s.logger)
	if err := reloaded.load(); err != nil {
		t.Fatalf("重新加载会话表失败: %v", err)
	}
	s.sessionStore = reloaded
	if s.sessionRecord(issued.SessionID) == nil {
		t.Fatal("重启之后应当还能查到这条会话")
	}
	if !s.validateRoomSessionToken("default", issued.Token) {
		t.Fatal("重启之后令牌应当继续有效")
	}
}
