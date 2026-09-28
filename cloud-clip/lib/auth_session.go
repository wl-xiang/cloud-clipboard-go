package lib

/**
*** FILE: auth_session.go
***   登录会话层：七天免密、随时可吊销、续签轮换、活动设备可查可踢
**/

// 背景（为什么要有这一层）：
//
// 改造前，密码验证通过后签发的是一枚**无状态**令牌（HMAC 签名的 room_session，
// 见 share_token.go），服务端什么都不记。好处是简单，代价是三件事做不了：
//
//  1. **吊销**：没人ping得动一枚已经发出去的令牌 —— 「退出登录」只能靠前端把本地
//     那份删掉，服务端毫不知情。令牌一旦外泄，除了改密码（连带所有人掉线）没有别的办法。
//  2. **有效期短**：正因为吊销不了，只能靠「短命」控制暴露窗口 —— 之前是 1 小时，
//     于是用户每隔一阵就得重新输密码（浏览器一关，前端那份还存在 sessionStorage，直接没了）。
//  3. **无从追查**：不知道此刻有谁拿着谁的凭据在哪台设备上登录。
//
// 这一层把「会话」变成服务端有记录的实体：每个会话有自己的 ID（sid）、所属家族
// （family —— 同一次输密码繁衍出来的一串令牌）、绝对到期时间，以及最后活跃时间。
// 令牌本身仍然是无状态签名的（服务端重启、Cookie 里的令牌依然可用），
// 但**每次使用都会回来对一次账**：被吊销 / 过期 / 被轮换掉还在用 → 一律拒绝。
//
// 有效期与安全的关系（三条线，缺一不可）：
//
//   · **滑动有效期（sessionTTL，默认 7 天）**：每次成功使用/续签都往后推，
//     所以「连续 7 天不用才会被踢」。用户要的是这个体感。
//   · **绝对生存期（sessionLifetime，默认 30 天）**：从**第一次输密码**那刻算起，
//     到点必须重新认证。这条是防「偷到一枚令牌然后无限续签变成永久后门」。
//     它写进令牌自己的 payload（crt 字段）—— 签过名的东西改不了，所以即使会话表丢了也照样生效。
//   · **轮换 + 复用检测**：续签时会发一枚新令牌并把旧的标记为已轮换；
//     如果有人在宽限窗口（rotatedSessionGraceSeconds，给并发请求善后）之后还在用旧令牌，
//     那不是并发，是复制件 —— 整族作废（持有者两边一起掉线，需要重新输密码。宁可这样）。
//
// HttpOnly Cookie（浏览器那条路）：
// 浏览器端默认把会话令牌放进 `HttpOnly + SameSite=Lax` 的 Cookie，JS 拿不到令牌
// —— 页面上任意 XSS 都偷不走登录态（这是「令牌被盗」最常见的一条路）。代价是 Cookie 会被
// 浏览器**自动**带上，所以必须同时做跨站校验：第三方站点发起的请求不认 Cookie 里的凭据，
// 见 requestMayUseCookieAuth。非浏览器客户端（快捷指令 / curl / Android）继续用
// Authorization 头里的令牌，不受影响。
//
// 落盘范式与 rooms.json / share-log.json 一致：进程内持锁 + 整份原子写（writeFileAtomic），
// 损坏时改名留档而不是删除。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// sessionFileName 会话表落盘文件名（放在 storageDir 下，和 rooms.json 同级）
	sessionFileName = "auth-sessions.json"
	// sessionCookieName 浏览器会话 Cookie 的名字
	sessionCookieName = "cc_auth"

	// defaultSessionTTLSeconds 滑动有效期默认 **7 天**：一次登录，七天内不用再输密码。
	defaultSessionTTLSeconds = 7 * 24 * 60 * 60
	minSessionTTLSeconds     = 5 * 60
	maxSessionTTLSeconds     = 30 * 24 * 60 * 60

	// defaultSessionLifetimeSecs 绝对生存期默认 30 天：无论续签多少次，
	// 30 天后必须重新输一次密码（防「偷到令牌即永久」）。
	defaultSessionLifetimeSecs = 30 * 24 * 60 * 60
	minSessionLifetimeSecs     = 5 * 60
	maxSessionLifetimeSecs     = 90 * 24 * 60 * 60

	// rotatedSessionGraceSeconds 轮换宽限期。
	// 旧令牌被换掉之后还会活这么久，是为了放行「刷新那一刻还在飞的请求」
	// （大文件上传、正在握手重连的 WebSocket……）。窗口之后还有人来用 → 判定为复制件。
	rotatedSessionGraceSeconds = 120

	// maxSessionCookieEntries Cookie 里最多保留多少条令牌（一个房间一条）。
	// 每条 ~300 字节，浏览器单 Cookie 上限约 4KB —— 超了会被静默丢弃。
	maxSessionCookieEntries = 10

	// maxTrackedSessions 会话表上限，防止只增不减把内存吃光。
	maxTrackedSessions = 5000

	// sessionGraveyardSeconds 过期 / 已吊销的记录还要留多久才从表里抹掉。
	// 留着是因为「谁在什么时候登出过」是有用的事后线索；太久则纯占空间。
	sessionGraveyardSeconds = 7 * 24 * 60 * 60

	// 同一 IP 的连续失败登录限制：令牌从 1 小时变成 7 天之后，
	// 「猜密码」的收益变大了 —— 一次猜中就管用七天，所以必须把它变成慢活儿。
	loginFailLimit    = 10
	loginFailWindow   = 10 * time.Minute
	loginLockDuration = 15 * time.Minute

	// sessionMaintenanceIntervalSeconds 后台巡检（清理 + 落盘节流刷新）的间隔
	sessionMaintenanceIntervalSeconds = 10 * 60

	// deliveryToken / deliveryCookie：`/auth/token` 的 delivery 参数取值
	deliveryToken  = "token"
	deliveryCookie = "cookie"
)

// errSessionLifetimeExceeded 会话族已经活过绝对生存期，必须重新输密码。
var errSessionLifetimeExceeded = errors.New("session_lifetime_exceeded")

// authSession 一条登录会话（服务端留的那一份账）。
type authSession struct {
	ID       string `json:"id"`       // 本条会话的唯一 id，同时写进令牌的 sid
	FamilyID string `json:"familyId"` // 同一次输密码繁衍出来的会话族 id
	Room     string `json:"room"`     // 签发时的房间（scope=global 的记录在实现上仍然是那个房间名）
	Scope    string `json:"scope"`    // ""=房间专属，"global"=平台级
	CreatedAt int64 `json:"createdAt"`
	// ExpiresAt 这一枚令牌的到期时刻（滑动刷新会延后它）
	ExpiresAt int64 `json:"expiresAt"`
	// AbsoluteExp 整个会话族的绝对到期时刻，到点必须重新输密码
	AbsoluteExp int64 `json:"absoluteExpiresAt"`
	LastSeenAt  int64 `json:"lastSeenAt"`
	RevokedAt   int64 `json:"revokedAt,omitempty"`  // 被登出 / 被管理端踢掉
	RotatedAt   int64 `json:"rotatedAt,omitempty"`  // 续签换掉了它，宽限期后失效
	RotatedTo   string `json:"rotatedTo,omitempty"` // 换成了谁
	RefreshCount int   `json:"refreshCount"`
	// UserAgent / CreatedIP / LastIP 只用于「让你认出这是不是自己的设备」，
	// 不参与任何鉴权判定（判权只看「知不知道密码 / 持有哪枚令牌」）。
	UserAgent string `json:"userAgent,omitempty"`
	CreatedIP string `json:"createdIp,omitempty"`
	LastIP    string `json:"lastIp,omitempty"`
	Via       string `json:"via,omitempty"` // password=输密码签发；refresh=续签轮换
}

// sessionView 给管理界面看的一条会话（**不含任何凭据**，只含元信息）。
type sessionView struct {
	ID          string `json:"id"`
	Room        string `json:"room"`
	Scope       string `json:"scope"`
	CreatedAt   int64  `json:"createdAt"`
	ExpiresAt   int64  `json:"expiresAt"`
	AbsoluteExp int64  `json:"absoluteExpiresAt"`
	LastSeenAt  int64  `json:"lastSeenAt"`
	UserAgent   string `json:"userAgent,omitempty"`
	CreatedIP   string `json:"createdIp,omitempty"`
	LastIP      string `json:"lastIp,omitempty"`
	Via         string `json:"via,omitempty"`
	RevokedAt   int64  `json:"revokedAt,omitempty"`
	RefreshCount int   `json:"refreshCount"`
	Current     bool   `json:"current"` // 是不是发起这次请求的那台设备自己
}

// sessionStore 进程内的会话表 + 落盘。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*authSession
	path     string
	logger   *log.Logger
	dirty    bool // 有变更但还没落盘（最后活跃时间的更新走节流）
}

func newSessionStore(path string, logger *log.Logger) *sessionStore {
	return &sessionStore{
		sessions: make(map[string]*authSession),
		path:     path,
		logger:   logger,
	}
}

// logf 会话层的日志入口 —— logger 可能为 nil（单元测试里直接拼 ClipboardServer 的情况），
// 不能假装它一定在。
func (st *sessionStore) logf(format string, args ...interface{}) {
	if st == nil || st.logger == nil {
		return
	}
	st.logger.Printf(format, args...)
}

// load 从磁盘读回会话表。文件不存在 = 还没人登录过，不是错误。
// 文件损坏时**改名留档**再以空表启动（与 rooms.json 的处理一致）：
// 会话没了最坏是「所有人都得重新输一次密码」，比启动不起来容易接受。
func (st *sessionStore) load() error {
	if !pathExists(st.path) {
		st.logf("会话表不存在（%s），以空表启动。", st.path)
		return nil
	}
	data, err := os.ReadFile(st.path)
	if err != nil {
		return fmt.Errorf("无法读取会话表 %s: %w", st.path, err)
	}
	var records []*authSession
	if err := json.Unmarshal(data, &records); err != nil {
		quarantined := fmt.Sprintf("%s.corrupt-%s", st.path, time.Now().Format("20060102-150405"))
		if renameErr := os.Rename(st.path, quarantined); renameErr != nil {
			st.logf("会话表解析失败(%v)，改名留档也失败(%v)，本次按空表启动（未删除原文件）。", err, renameErr)
		} else {
			st.logf("会话表解析失败(%v)，已改名留档到 %s（未删除）。", err, quarantined)
		}
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, rec := range records {
		if rec == nil || strings.TrimSpace(rec.ID) == "" {
			continue
		}
		rec.ID = strings.TrimSpace(rec.ID)
		st.sessions[rec.ID] = rec
	}
	st.logf("已加载 %d 条登录会话。", len(st.sessions))
	return nil
}

// saveLocked 整份原子写。调用方必须已持有写锁。
func (st *sessionStore) saveLocked() error {
	if st.path == "" {
		st.dirty = false
		return nil
	}
	list := make([]*authSession, 0, len(st.sessions))
	for _, rec := range st.sessions {
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话表失败: %w", err)
	}
	if err := writeFileAtomic(st.path, data, 0600); err != nil {
		return fmt.Errorf("写入会话表失败: %w", err)
	}
	st.dirty = false
	return nil
}

// pruneLocked 清掉早就过期 / 早就吊销的记录；表太大时先牺牲最旧的。
// 调用方必须已持有写锁。
func (st *sessionStore) pruneLocked(now int64) {
	forcedDrop := len(st.sessions) - maxTrackedSessions
	for id, rec := range st.sessions {
		deadline := rec.ExpiresAt
		if rec.RevokedAt > 0 {
			deadline = rec.RevokedAt
		}
		if rec.AbsoluteExp > deadline && rec.RevokedAt == 0 {
			deadline = rec.AbsoluteExp
		}
		if now-deadline > sessionGraveyardSeconds {
			delete(st.sessions, id)
			st.dirty = true
		}
	}
	if forcedDrop > 0 {
		ordered := make([]*authSession, 0, len(st.sessions))
		for _, rec := range st.sessions {
			ordered = append(ordered, rec)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].LastSeenAt < ordered[j].LastSeenAt })
		for i := 0; i < forcedDrop && i < len(ordered); i++ {
			delete(st.sessions, ordered[i].ID)
			st.dirty = true
		}
	}
}

// ---- 服务端封装（以下方法对 `s.sessionStore == nil` 一律安全） ----

// sessionStorePath 会话表的落盘路径。
func (s *ClipboardServer) sessionStorePath() string {
	dir := s.storageFolder
	if s.config != nil && s.config.Server.StorageDir != "" {
		dir = s.config.Server.StorageDir
	}
	return filepath.Join(dir, sessionFileName)
}

// sessLogf 会话层的日志（logger 可能为 nil）。
func (s *ClipboardServer) sessLogf(format string, args ...interface{}) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// sessionTTLSeconds 滑动有效期（秒）。配置缺失 / 越界时钳到合法区间，
// 不因为配置文件里多打一个零就变成「一年不用登录」。
func (s *ClipboardServer) sessionTTLSeconds() int {
	ttl := defaultSessionTTLSeconds
	if s.config != nil {
		ttl = s.config.Server.SessionTTL
		if ttl <= 0 {
			ttl = defaultSessionTTLSeconds
		}
	}
	return clampInt(ttl, minSessionTTLSeconds, maxSessionTTLSeconds)
}

// sessionLifetimeSeconds 绝对生存期（秒），且不短于滑动有效期 ——
// 否则「还没到期就被判必须重登录」，配置等于没写。
func (s *ClipboardServer) sessionLifetimeSeconds() int {
	lifetime := defaultSessionLifetimeSecs
	if s.config != nil {
		lifetime = s.config.Server.SessionLifetime
		if lifetime <= 0 {
			lifetime = defaultSessionLifetimeSecs
		}
	}
	lifetime = clampInt(lifetime, minSessionLifetimeSecs, maxSessionLifetimeSecs)
	if ttl := s.sessionTTLSeconds(); lifetime < ttl {
		lifetime = ttl
	}
	return lifetime
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max || max < min {
		return max
	}
	return value
}

// issuedSession 一次签发的结果。
type issuedSession struct {
	Token       string
	SessionID   string
	FamilyID    string
	ExpiresAt   int64
	AbsoluteExp int64
	Scope       string
	Room        string
}

// sessionSeed 签发一份会话所需的上下文。空的家族信息 = 这是一次全新的登录（输密码）。
type sessionSeed struct {
	FamilyID     string
	FamilyStart  int64
	ParentID     string
	RefreshCount int
	Scope        string
	Via          string
	UserAgent    string
	IP           string
}

// openSession 签发一枚新会话令牌并在服务端登记。
//
// prev 非空表示这是**续签**：沿用同一个家族（出行不能超过绝对生存期）、
// 计数 +1，并把上一枚标记为已轮换（宽限期后作废）。
func (s *ClipboardServer) openSession(room string, seed sessionSeed) (*issuedSession, error) {
	now := time.Now().Unix()
	ttl := int64(s.sessionTTLSeconds())
	lifetime := int64(s.sessionLifetimeSeconds())

	familyID := strings.TrimSpace(seed.FamilyID)
	familyStart := seed.FamilyStart
	refreshCount := seed.RefreshCount
	if familyID == "" || familyStart <= 0 {
		id, err := newShareJTI()
		if err != nil {
			return nil, err
		}
		familyID, familyStart, refreshCount = id, now, 0
	}

	absoluteExp := familyStart + lifetime
	if now >= absoluteExp {
		return nil, errSessionLifetimeExceeded
	}
	expiresAt := now + ttl
	if expiresAt > absoluteExp {
		expiresAt = absoluteExp
	}

	normalizedRoom := normalizeRoomName(room)
	sid, err := newShareJTI()
	if err != nil {
		return nil, err
	}

	rec := &authSession{
		ID:           sid,
		FamilyID:     familyID,
		Room:         normalizedRoom,
		Scope:        strings.TrimSpace(seed.Scope),
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
		AbsoluteExp:  absoluteExp,
		LastSeenAt:   now,
		RefreshCount: refreshCount,
		UserAgent:    truncateSessionLabel(seed.UserAgent, 200),
		CreatedIP:    seed.IP,
		LastIP:       seed.IP,
		Via:          seed.Via,
	}
	if rec.Via == "" {
		rec.Via = "password"
	}

	if st := s.sessionStore; st != nil {
		st.mu.Lock()
		if seed.ParentID != "" {
			if prev := st.sessions[seed.ParentID]; prev != nil && prev.RevokedAt == 0 {
				prev.RotatedAt = now
				prev.RotatedTo = sid
				st.dirty = true
			}
		}
		st.sessions[sid] = rec
		st.pruneLocked(now)
		st.dirty = true
		if err := st.saveLocked(); err != nil {
			st.logf("警告: 会话表落盘失败: %v（会话仍在内存中生效）", err)
		}
		st.mu.Unlock()
	}

	token, err := s.signRoomSessionToken(normalizedRoom, sid, familyID, rec.Scope, familyStart, expiresAt)
	if err != nil {
		return nil, err
	}

	return &issuedSession{
		Token:       token,
		SessionID:   sid,
		FamilyID:    familyID,
		ExpiresAt:   expiresAt,
		AbsoluteExp: absoluteExp,
		Scope:       rec.Scope,
		Room:        normalizedRoom,
	}, nil
}

// sessionRecord 取一条会话的副本（避免把内部指针放出去让别人改）。
func (s *ClipboardServer) sessionRecord(sid string) *authSession {
	sid = strings.TrimSpace(sid)
	if sid == "" || s.sessionStore == nil {
		return nil
	}
	st := s.sessionStore
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.sessions[sid]
	if !ok || rec == nil {
		return nil
	}
	copied := *rec
	return &copied
}

// sessionClaimsActive 这枚会话令牌现在还作不作数。
//
// 判定顺序是有讲究的：先做**不依赖任何服务端状态**的检查（签名、有效期、绝对生存期），
// 再回头查服务端的账（吊销 / 轮换 / 会话表是否存在）。这样即便会话表丢了（换机器上、
// 清了数据卷），令牌自己携带的那些约束依然成立 —— 不会因为服务端状态缺失就放开口子。
func (s *ClipboardServer) sessionClaimsActive(claims *shareClaims) bool {
	if claims == nil {
		return false
	}
	now := time.Now().Unix()

	// 1) 绝对生存期：家族诞生时间写进令牌且签过名，改不了 —— 会话表丢了也照样算数。
	if claims.Crt > 0 && now >= claims.Crt+int64(s.sessionLifetimeSeconds()) {
		return false
	}

	// 2) 无 sid 的旧版令牌（本次改造之前签发的）：沿用「签名即有效」，到期自然消失。
	//    它们既不能续签（见 handleAuthTokenRefresh），也活不过原先的上限。
	if claims.Sid == "" {
		return true
	}

	if s.sessionStore == nil {
		return true
	}

	rec := s.sessionRecord(claims.Sid)
	if rec == nil {
		// Fail closed：会话表里查不到这一条 → 拒绝。
		// 会话表是「登上过什么」的唯一账本，查不到只可能是被清了档或者压根没签过；
		// 这时放行的代价是「一枚已经登出过的令牌仍能继续用」，与「登出即失效」的要求正相反。
		return false
	}
	if rec.RevokedAt > 0 {
		return false
	}
	if now > rec.ExpiresAt {
		return false
	}
	if rec.RotatedAt > 0 && now-rec.RotatedAt > rotatedSessionGraceSeconds {
		// 已经换掉的令牌在宽限期之后还在用 —— 基本可以断定是被复制了：
		// 正版持有人手里拿的应该已经更新成新令牌了。整族作废，逼所有人重新认证。
		s.sessLogf("安全告警: 已轮换的会话 %s 在宽限期后仍被使用（来自 IP %s），已作废整个会话族 %s",
			rec.ID, s.sessionLastIP(rec), rec.FamilyID)
		s.revokeSessionFamily(rec.FamilyID, "rotated_token_reuse")
		return false
	}

	s.touchSession(claims.Sid, "")
	return true
}

func (s *ClipboardServer) sessionLastIP(rec *authSession) string {
	if rec == nil {
		return ""
	}
	if rec.LastIP != "" {
		return rec.LastIP
	}
	return rec.CreatedIP
}

// touchSession 更新最后活跃时间。故意**不是**每次都落盘 ——
// 每个 HTTP 请求都会走到这里，逐次写盘会把机械盘 / 容器数据卷写满、也拖慢请求。
// 更新留在内存里，由后台巡检（startSessionMaintenance）节流写下去。
func (s *ClipboardServer) touchSession(sid, ip string) {
	if s.sessionStore == nil || sid == "" {
		return
	}
	now := time.Now().Unix()
	st := s.sessionStore
	st.mu.Lock()
	defer st.mu.Unlock()
	rec, ok := st.sessions[sid]
	if !ok || rec == nil {
		return
	}
	if now-rec.LastSeenAt < sessionMaintenanceIntervalSeconds/2 {
		return
	}
	rec.LastSeenAt = now
	if ip != "" {
		rec.LastIP = ip
	}
	st.dirty = true
}

// revokeSessionByID 吊销单条会话。reason 只用于日志。
func (s *ClipboardServer) revokeSessionByID(sid, reason string) bool {
	sid = strings.TrimSpace(sid)
	if sid == "" || s.sessionStore == nil {
		return false
	}
	now := time.Now().Unix()
	st := s.sessionStore
	st.mu.Lock()
	rec, ok := st.sessions[sid]
	if !ok || rec == nil || rec.RevokedAt > 0 {
		st.mu.Unlock()
		return false
	}
	rec.RevokedAt = now
	st.dirty = true
	st.mu.Unlock()

	s.sessLogf("已吊销会话 %s (%s)，家族 %s，来自 %s", sid, reason, rec.FamilyID, s.sessionLastIP(rec))
	return true
}

// revokeSessionFamily 作废整个会话族（同一次登录繁衍出的所有令牌）。
// 返回实际吊销的条数。
func (s *ClipboardServer) revokeSessionFamily(familyID, reason string) int {
	familyID = strings.TrimSpace(familyID)
	if familyID == "" || s.sessionStore == nil {
		return 0
	}
	now := time.Now().Unix()
	count := 0
	st := s.sessionStore
	st.mu.Lock()
	victims := make([]*authSession, 0, 4)
	for id, rec := range st.sessions {
		if rec.FamilyID != familyID || rec.RevokedAt > 0 {
			_ = id
			continue
		}
		rec.RevokedAt = now
		count++
		st.dirty = true
		victims = append(victims, rec)
	}
	if st.dirty {
		if err := st.saveLocked(); err != nil {
			st.logf("警告: 吊销后会话表落盘失败: %v", err)
		}
	}
	st.mu.Unlock()

	// 已经在网的连接不会因为凭据失效而自动断开 —— WebSocket 只在握手时查一次。
	// 不踢的话「登出」只是下次重连才生效，桌面上那条「看不见的后门」还开着直播。
	kicked := make([]string, 0, len(victims))
	for _, rec := range victims {
		kicked = append(kicked, rec.ID)
	}
	s.closeWebSocketSessions(kicked)

	if count > 0 {
		s.sessLogf("已作废会话族 %s（%d 条令牌，原因: %s）", familyID, count, reason)
	}
	return count
}

// revokeAllSessions 吊销全部会话（「退出所有设备」）。
func (s *ClipboardServer) revokeAllSessions(reason string) int {
	if s.sessionStore == nil {
		return 0
	}
	now := time.Now().Unix()
	count := 0
	st := s.sessionStore
	st.mu.Lock()
	kicked := make([]string, 0, 8)
	for id, rec := range st.sessions {
		_ = id
		if rec.RevokedAt > 0 {
			continue
		}
		rec.RevokedAt = now
		count++
		st.dirty = true
		kicked = append(kicked, rec.ID)
	}
	if st.dirty {
		if err := st.saveLocked(); err != nil {
			st.logf("警告: 全部登出后会话表落盘失败: %v", err)
		}
	}
	st.mu.Unlock()
	s.closeWebSocketSessions(kicked)
	s.sessLogf("已吊销全部会话（%d 条，原因: %s）", count, reason)
	return count
}

// listSessionViews 列出会话。调用方已经鉴过权（global 会话能看全部，
// 普通房间会话只能看自己那一族），这里只负责出形状。
func (s *ClipboardServer) listSessionViews(currentIDs map[string]bool, globalView bool) []sessionView {
	if s.sessionStore == nil {
		return []sessionView{}
	}
	now := time.Now().Unix()
	st := s.sessionStore
	st.mu.Lock()
	defer st.mu.Unlock()

	views := make([]sessionView, 0, len(st.sessions))
	for id, rec := range st.sessions {
		if rec.RevokedAt > 0 || (rec.ExpiresAt > 0 && now > rec.ExpiresAt) {
			continue
		}
		if !globalView && !currentIDs[rec.FamilyID] {
			continue
		}
		views = append(views, sessionView{
			ID:           id,
			Room:         rec.Room,
			Scope:        rec.Scope,
			CreatedAt:    rec.CreatedAt,
			ExpiresAt:    rec.ExpiresAt,
			AbsoluteExp:  rec.AbsoluteExp,
			LastSeenAt:   rec.LastSeenAt,
			UserAgent:    rec.UserAgent,
			CreatedIP:    rec.CreatedIP,
			LastIP:       rec.LastIP,
			Via:          rec.Via,
			RefreshCount: rec.RefreshCount,
			Current:      currentIDs[id],
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Current != views[j].Current {
			return views[i].Current
		}
		return views[i].LastSeenAt > views[j].LastSeenAt
	})
	return views
}

// startSessionMaintenance 后台巡检：清理过期记录 + 把内存里累积的最后活跃时间落盘。
func (s *ClipboardServer) startSessionMaintenance() {
	if s.sessionStore == nil {
		return
	}
	ticker := time.NewTicker(sessionMaintenanceIntervalSeconds * time.Second)
	s.sessionTicker = ticker
	go func() {
		// ⚠️ 循环里只读**局部变量**的那个 ticker，不要读 s.sessionTicker：
		// 后者会被 stopSessionMaintenance 置成 nil（停机 / 测试清理会走到），
		// 撞上那一刻 `<-s.sessionTicker.C` 就是一次 nil 指针 panic，
		// 而它发生在一个谁都没在等的 goroutine 里，直接把整个进程带走。
		for range ticker.C {
			s.runSessionMaintenance()
		}
	}()
	s.sessLogf("会话巡检已启动，间隔 %d 秒", sessionMaintenanceIntervalSeconds)
}

func (s *ClipboardServer) stopSessionMaintenance() {
	if s.sessionTicker != nil {
		s.sessionTicker.Stop()
		s.sessionTicker = nil
	}
	s.runSessionMaintenance()
}

func (s *ClipboardServer) runSessionMaintenance() {
	st := s.sessionStore
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(time.Now().Unix())
	if st.dirty {
		if err := st.saveLocked(); err != nil {
			st.logf("警告: 会话巡检落盘失败: %v", err)
		}
	}
}

// ---- 登录限速 ----

type loginFailures struct {
	windowStart int64
	count       int
	lockedUntil int64
}

type loginGuard struct {
	mu      sync.Mutex
	entries map[string]*loginFailures
}

func (s *ClipboardServer) ensureLoginGuard() *loginGuard {
	if s.loginGuard != nil {
		return s.loginGuard
	}
	s.loginGuard = &loginGuard{entries: make(map[string]*loginFailures)}
	return s.loginGuard
}

// loginLockedRemaining 这个来源现在是不是被锁着；锁着的话还剩多少秒。
// key 用「IP + 房间」：同一个 IP 换房间重试不该白得一轮机会。
func (s *ClipboardServer) loginLockedRemaining(key string) int64 {
	guard := s.ensureLoginGuard()
	now := time.Now().Unix()
	guard.mu.Lock()
	defer guard.mu.Unlock()
	entry, ok := guard.entries[key]
	if !ok {
		return 0
	}
	if entry.lockedUntil > now {
		return entry.lockedUntil - now
	}
	if now-entry.windowStart > int64(loginFailWindow.Seconds()) {
		delete(guard.entries, key)
	}
	return 0
}

func (s *ClipboardServer) recordLoginFailure(key string) {
	guard := s.ensureLoginGuard()
	now := time.Now().Unix()
	guard.mu.Lock()
	defer guard.mu.Unlock()
	entry, ok := guard.entries[key]
	if !ok || now-entry.windowStart > int64(loginFailWindow.Seconds()) {
		entry = &loginFailures{windowStart: now}
		guard.entries[key] = entry
	}
	entry.count++
	if entry.count >= loginFailLimit {
		entry.lockedUntil = now + int64(loginLockDuration.Seconds())
		s.sessLogf("登录限速: %s 在短时间内失败 %d 次，锁定 %v（令牌现在有效期 7 天，猜密码必须慢）",
			key, entry.count, loginLockDuration)
	}
}

func (s *ClipboardServer) clearLoginFailures(key string) {
	guard := s.ensureLoginGuard()
	guard.mu.Lock()
	defer guard.mu.Unlock()
	delete(guard.entries, key)
}

// ---- Cookie 通道 ----

// requestMayUseCookieAuth 这次请求能不能认 Cookie 里带的凭据。
//
// Cookie 的特点是「浏览器自动带上」，攻击者不需要知道它是什么 —— 恶意页面只要能
// 让浏览器发一个跨站请求，用户的登录态就跟着过去了（CSRF）。所以 Cookie 这条通道
// 比 Authorization 头多一道门：必须是**浏览器认为的同源请求**。
//
// Authorization 头里的令牌不受此限：那是调用方显式塞进来的，跨站页面拿不到也就伪造不了。
func requestMayUseCookieAuth(r *http.Request) bool {
	if r == nil {
		return false
	}
	// Sec-Fetch-Site 是浏览器给的同源信号，现代浏览器对所有页面发起的请求都会带。
	if site := strings.TrimSpace(strings.ToLower(r.Header.Get("Sec-Fetch-Site"))); site != "" {
		switch site {
		case "same-origin", "same-site":
			return true
		case "none":
			// 地址栏直达 / 书签：只有顶层导航属于这一类，浏览器只允许它是安全方法，
			// 所以这里也对 GET/HEAD 放行。
			return r.Method == http.MethodGet || r.Method == http.MethodHead
		default: // cross-site
			return false
		}
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return sameSiteRequestURL(origin, r)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		return sameSiteRequestURL(referer, r)
	}
	// 非浏览器客户端（curl / 快捷指令 / Android）不带这些头 —— 它们也用不上 Cookie 通道。
	return true
}

func sameSiteRequestURL(raw string, r *http.Request) bool {
	if raw == "" || r == nil {
		return false
	}
	if strings.EqualFold(raw, "null") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return hostsEqual(u.Host, r.Host)
}

// hostsEqual 只比主机名，**不比端口**。
// 这不是偷懒：Cookie 的作用域本来就忽略端口（`a.com:8080` 与 `a.com` 共享 Cookie），
// 所以比端口挡不住任何东西，却会在「反代外面换了个端口」时把合法请求判成跨站。
func hostsEqual(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(splitHostPortSafe(a)))
	b = strings.ToLower(strings.TrimSpace(splitHostPortSafe(b)))
	return a != "" && a == b
}

// splitHostPortSafe 去掉 `:端口`（兼容 IPv6 字面量），只留主机名。
func splitHostPortSafe(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if strings.HasPrefix(hostport, "[") {
		if i := strings.Index(hostport, "]"); i >= 0 {
			return hostport[:i+1]
		}
	}
	if strings.Count(hostport, ":") == 1 {
		if i := strings.LastIndex(hostport, ":"); i > 0 {
			return hostport[:i]
		}
	}
	return hostport
}

// readSessionCookieTokens 读出浏览器带来的会话令牌列表。
func readSessionCookieTokens(r *http.Request) []string {
	if r == nil {
		return nil
	}
	// 跨站请求不认 Cookie 凭据 —— 恶意页面会让浏览器自动带上它（详见函数注释）。
	if !requestMayUseCookieAuth(r) {
		return nil
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return nil
	}
	// Cookie 的值里不能有引号、分号、逗号这类字符（net/http 会直接整条丢弃），
	// 而 JSON 天生就是引号做的 —— 所以这里用 URL 安全的 base64 装一层，
	// 顺便也让 kansha 日志里看不出房间结构。
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cookie.Value))
	if err != nil {
		return nil
	}
	var tokens []string
	if err := json.Unmarshal(raw, &tokens); err != nil {
		return nil
	}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token = strings.TrimSpace(token); token != "" {
			out = append(out, token)
		}
	}
	return out
}

// sessionIDForRequest 找出这次请求实际用来通过鉴权的那条会话的 id。
//
// WebSocket 只在**握手时**查一次凭据，之后这条连接再没有复核机会 ——
// 所以必须把「这条连接用的是哪条会话」记进 connSessionMap，
// 否则用户点了退出登录，那条连接还活蹦乱跳地收着属于他的消息（见 closeWebSocketSessions）。
func (s *ClipboardServer) sessionIDForRequest(r *http.Request, room, extraToken string) string {
	tokens := extractAuthTokens(r)
	if strings.TrimSpace(extraToken) != "" {
		tokens = append(tokens, strings.TrimSpace(extraToken))
	}
	for _, token := range tokens {
		claims, ok := s.parseRoomSessionToken(token)
		if !ok || claims.Sid == "" {
			continue
		}
		if strings.TrimSpace(claims.Scope) != "global" && normalizeRoomName(claims.Room) != normalizeRoomName(room) {
			continue
		}
		if s.sessionClaimsActive(claims) {
			return claims.Sid
		}
	}
	return ""
}

// closeWebSocketSessions 掐断使用这些会话的 WebSocket 连接。
//
// 为什么非掐不可：用户的直觉是「退出登录 = 这台设备立刻断开」。
// 如果服务端只是把令牌标记成无效，已经建立的那条连接会一直活着到下一次重连 ——
// 而没人知道下一次是什么时候（WebSocket 只在断线时才重新握手）。
//
// ⚠️ 这里用的是 gorilla 的 Close()：**只关底层 TCP，不发 close 帧**。
// 发帧的版本要求「同一时刻只有一个 writer」，而这里是从吊销流程里跨 goroutine 调过去的，
// 和推送消息的那条 goroutine 抢 writer 会让连接直接 panic。
func (s *ClipboardServer) closeWebSocketSessions(sids []string) {
	if len(sids) == 0 || s.connSessionMap == nil {
		return
	}
	targets := make(map[string]bool, len(sids))
	for _, sid := range sids {
		if sid = strings.TrimSpace(sid); sid != "" {
			targets[sid] = true
		}
	}
	if len(targets) == 0 {
		return
	}

	var victims []*websocket.Conn
	s.runMutex.Lock()
	for conn, sid := range s.connSessionMap {
		if targets[sid] {
			victims = append(victims, conn)
			delete(s.connSessionMap, conn)
		}
	}
	s.runMutex.Unlock()

	for _, conn := range victims {
		s.sessLogf("WebSocket 连接 %s 使用的会话已被吊销，正在断开。", conn.RemoteAddr())
		// 后续的地图清理走既有流程：读 goroutine 会因读失败退出并调用 cleanupWebSocketConnection
		if err := conn.Close(); err != nil {
			s.sessLogf("关闭 WebSocket 连接失败: %v", err)
		}
	}
}

// sessionCookieKey 这条令牌在 Cookie 里占的坑位：
// 一个房间一个坑（进不同房间的令牌和平共处），全局令牌单独一个坑。
func sessionCookieKey(claims *shareClaims) string {
	if claims == nil {
		return ""
	}
	if strings.TrimSpace(claims.Scope) == "global" {
		return "__global__"
	}
	return normalizeRoomName(claims.Room)
}

// writeSessionCookie 把令牌写进 HttpOnly Cookie，同时保留 Cookie 里其它房间的令牌。
func (s *ClipboardServer) writeSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	token = strings.TrimSpace(token)
	if token == "" || w == nil {
		return
	}
	newClaims, ok := s.parseRoomSessionToken(token)
	if !ok {
		return
	}
	newKey := sessionCookieKey(newClaims)

	merged := []string{token}
	seenKeys := map[string]bool{newKey: true}
	for _, existing := range readSessionCookieTokens(r) {
		claims, valid := s.parseRoomSessionToken(existing)
		if !valid {
			continue // 解析不了 / 已过期的令牌没有保留价值，顺手从 Cookie 里扫地出去
		}
		key := sessionCookieKey(claims)
		if key == "" || seenKeys[key] {
			continue
		}
		seenKeys[key] = true
		merged = append(merged, strings.TrimSpace(existing))
	}
	if len(merged) > maxSessionCookieEntries {
		// 超出预算：保留到期最晚的那些（正在用的一定不会先被丢掉）
		sort.SliceStable(merged[1:], func(i, j int) bool {
			ci, oki := s.parseRoomSessionToken(merged[i+1])
			cj, okj := s.parseRoomSessionToken(merged[j+1])
			if !oki || !okj {
				return !oki
			}
			return ci.Exp > cj.Exp
		})
		merged = merged[:maxSessionCookieEntries]
	}

	data, err := json.Marshal(merged)
	if err != nil {
		return
	}
	// 引号、分号这些字符 net/http 不让出现在 Cookie 值里（会整条丢掉），
	// 也让旁观者一眼看不出里面装了几个房间。
	value := base64.RawURLEncoding.EncodeToString(data)
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure 只在 HTTPS 下加：局域网里最常见的还是 http://ip:port，
		// 加了 Secure 浏览器会直接把这个 Cookie 丢掉 —— 表现为「登录完马上又要输密码」。
		Secure: getScheme(r) == "https",
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	if w == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && getScheme(r) == "https",
	})
}

// truncateSessionLabel 截断用于展示的长字段（UA 可以有几百个字符）。
func truncateSessionLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
