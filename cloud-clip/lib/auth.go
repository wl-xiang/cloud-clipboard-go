package lib

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type RoomAuthRequirement struct {
	Room     string
	Required bool
	Password string
	// FileExpire: nil=使用全局 file.expire；0=该房间文件永不过期；>0=覆盖过期秒数
	FileExpire *int64
}

// RoomAuthEntry 单个房间的认证与文件留存配置。
// 配置值支持三种 JSON 形式（UnmarshalJSON 兼容）：
//
//	"password"                       -> 仅密码（旧格式）
//	12345                            -> 数字密码
//	{"password": "x", "fileExpire": 0} -> 密码 + 文件过期覆盖（fileExpire: 0=永不过期，>0=秒数，<0=回退全局）
//	{"open": true, "fileExpire": 0}  -> **开放房间**：不要密码，即使全局 server.auth 设了也一样
//
// `open` 单独一个字段、而不是拿「空密码」当信号，有两个原因：
//  1. 空字符串在这份配置里**已经有含义**（只接受全局 auth，见 config.md）——
//     改掉它会静默改变所有现有配置的含义，某个房间会悄悄敞开且不报错。安全设置不能这么反转。
//  2. 空密码和「压根没配过这个房间」在 JSON 里长得一样，而这两者的意图正好相反。
type RoomAuthEntry struct {
	Password   string `json:"password"`
	FileExpire *int64 `json:"fileExpire"`
	Open       bool   `json:"open"`
}

func (e *RoomAuthEntry) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)

	var strVal string
	if err := json.Unmarshal(trimmed, &strVal); err == nil {
		e.Password = strings.TrimSpace(strVal)
		return nil
	}

	var numVal json.Number
	if err := json.Unmarshal(trimmed, &numVal); err == nil {
		e.Password = numVal.String()
		return nil
	}

	var obj struct {
		Password   interface{} `json:"password"`
		FileExpire *float64    `json:"fileExpire"`
		Open       bool        `json:"open"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	if obj.Password != nil {
		e.Password = normalizeAuthValue(obj.Password)
	}
	if obj.FileExpire != nil {
		v := int64(*obj.FileExpire)
		e.FileExpire = &v
	}
	e.Open = obj.Open
	return nil
}

type RoomAuthConfig map[string]RoomAuthEntry

func normalizeAuthValue(auth interface{}) string {
	switch value := auth.(type) {
	case string:
		return value
	case int:
		if value != 0 {
			return strconv.Itoa(value)
		}
	case float64:
		if value != 0 {
			return strconv.FormatFloat(value, 'f', 0, 64)
		}
	case json.Number:
		return string(value)
	}

	return ""
}

func normalizeRoomAuthConfig(roomAuth RoomAuthConfig) RoomAuthConfig {
	if len(roomAuth) == 0 {
		return RoomAuthConfig{}
	}

	normalized := make(RoomAuthConfig, len(roomAuth))
	for room, entry := range roomAuth {
		normalized[normalizeRoomName(room)] = entry
	}

	return normalized
}

func extractAuthToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return parts[1]
		}
		return authHeader
	}

	if queryToken := r.URL.Query().Get("auth"); queryToken != "" {
		return queryToken
	}

	// 浏览器那条路：会话令牌放在 HttpOnly Cookie 里（见 auth_session.go），
	// JS 读不到它，请求也不需要操心带上它。跨站请求不认这条通道（CSRF）。
	if tokens := readSessionCookieTokens(r); len(tokens) > 0 {
		return tokens[0]
	}

	return ""
}

// extractWebSocketToken 提取 WebSocket 握手使用的 token。
// 优先取 Authorization / ?auth= 以兼容旧客户端，其次取 Sec-WebSocket-Protocol 子协议，
// 避免凭据出现在 URL 中泄漏到访问日志。
func extractWebSocketToken(r *http.Request) string {
	if token := extractAuthToken(r); token != "" {
		return token
	}
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			return p
		}
	}
	return ""
}

func extractAuthTokens(r *http.Request) []string {
	tokens := []string{}
	pushToken := func(token string) {
		normalized := strings.TrimSpace(token)
		if normalized == "" {
			return
		}
		for _, existing := range tokens {
			if existing == normalized {
				return
			}
		}
		tokens = append(tokens, normalized)
	}

	pushToken(extractAuthToken(r))

	// Cookie 里的每一条都要试：一个房间一条令牌（见 sessionCookieKey），
	// 只试第一条的话，「先登录 A 房间再登录 B 房间」之后，回看 A 就变成没登录了。
	for _, token := range readSessionCookieTokens(r) {
		pushToken(token)
	}

	extraHeader := strings.TrimSpace(r.Header.Get("X-Room-Auth-Tokens"))
	if extraHeader == "" {
		return tokens
	}

	var parsed []string
	if err := json.Unmarshal([]byte(extraHeader), &parsed); err == nil {
		for _, token := range parsed {
			pushToken(token)
		}
		return tokens
	}

	for _, token := range strings.Split(extraHeader, ",") {
		pushToken(token)
	}

	return tokens
}

func (s *ClipboardServer) resolveRoomAuth(room string) RoomAuthRequirement {
	normalizedRoom := normalizeRoomName(room)
	globalPassword := normalizeAuthValue(s.config.Server.Auth)

	// 用户自建房间（rooms.json）**优先**：它的密码是创建者在界面上定的，
	// 部署者不该在配置文件里再给它配一个（创建时也拒绝与 roomAuth 重名，所以不会撞）。
	if s.roomRegistry != nil {
		if managed, ok := s.roomRegistry.get(normalizedRoom); ok && managed.Password != "" {
			return RoomAuthRequirement{Room: normalizedRoom, Required: true, Password: managed.Password}
		}
	}

	entry, hasEntry := s.config.Server.RoomAuth[normalizedRoom]

	// 房间自己带密码 → 用它。全局密码**仍然有效**（见 tokenMatchesRoom），
	// 所以 roomAuth 是「多给一把钥匙」，不是「换锁」。
	if hasEntry && entry.Password != "" {
		// ⚠️ 同时写了 open 和 password 是配置写错了。**密码优先** ——
		// 宁可多要一次密码，也不能因为配置里多打了一个字段就把房间敞开。
		return RoomAuthRequirement{Room: normalizedRoom, Required: true, Password: entry.Password, FileExpire: entry.FileExpire}
	}

	// 显式开放：**不**回落全局 auth。这就是「全局加密 + 个别房间开放」的表达方式。
	if hasEntry && entry.Open {
		return RoomAuthRequirement{Room: normalizedRoom, FileExpire: entry.FileExpire}
	}

	// 没配过、或配了个空密码 → 回落全局 auth（旧行为，别改回去）。
	if globalPassword != "" {
		return RoomAuthRequirement{Room: normalizedRoom, Required: true, Password: globalPassword, FileExpire: entry.FileExpire}
	}

	return RoomAuthRequirement{Room: normalizedRoom, FileExpire: entry.FileExpire}
}

// resolveFileExpireSeconds 返回指定房间文件上传生效的过期秒数：0 表示永不过期，>0 为秒数
func (s *ClipboardServer) resolveFileExpireSeconds(room string) int64 {
	global := int64(s.config.File.Expire)
	fileExpire := s.resolveRoomAuth(room).FileExpire
	if fileExpire == nil {
		return global
	}
	if *fileExpire < 0 {
		s.logger.Printf("配置警告: 房间 '%s' 的 roomAuth.fileExpire 为负数 (%d)，已回退为全局 file.expire", normalizeRoomName(room), *fileExpire)
		return global
	}
	return *fileExpire
}

func (s *ClipboardServer) tokenMatchesRoom(room string, token string) bool {
	if token == "" {
		return false
	}

	if s.validateRoomSessionToken(room, token) {
		return true
	}

	// ⚠️ 全局密码是**总钥匙**：它能开任何房间，包括自己带密码的那种。
	// 这是既有语义（见 resolveRoomAuth 的注释：roomAuth 是「多给一把钥匙」，不是换锁），
	// 曾经在重写这个函数时丢掉过，被 TestOpenRoomOverridesGlobalAuth 逮住。
	if globalPassword := normalizeAuthValue(s.config.Server.Auth); globalPassword != "" && token == globalPassword {
		return true
	}

	// 房间密码**统一从 resolveRoomAuth 取**，而不是直接翻 config.Server.RoomAuth ——
	// 那样写的话，「自建房间」（rooms.json）的密码就漏在判定之外了：
	// 界面能创建成功，但拿着它的密码连不上 WebSocket（只认全局密码）。
	// 一处判定，两条来源（配置 / 注册表）都在它里面。
	if roomPassword := s.resolveRoomAuth(room).Password; roomPassword != "" && token == roomPassword {
		return true
	}

	return false
}

func (s *ClipboardServer) canAccessRoom(room string, token string) bool {
	requirement := s.resolveRoomAuth(room)
	if !requirement.Required {
		return true
	}

	return s.tokenMatchesRoom(room, token)
}

// ⚠️ 这里曾经有个 hasRoomAuthEntry（「配置里有没有这一项」）。它和「这个房间要不要密码」
// **不是一回事**：显式 `{open: true}` 的房间在配置里有这一项，但**不要**密码。
// 两个调用点（/server 的 roomProtected、/rooms 的 isProtected）都改成
// resolveRoomAuth(...).Required 之后它就没人用了，删掉。

// isGlobalAdmin 这个凭据是不是全局密码。
//
// ⚠️ 全局密码在本设计里被定义为**管理员凭据**。这不是代码能决定的事，是部署约定：
// README 把 AUTH_PASSWORD 描述成「全局访问密码」，如果部署者把它给全家共用，
// 那在这个模型里「所有人都是管理员」。
//
// 房间会话令牌**不算**管理员：它是按房间签发的（见 validateRoomSessionToken），
// 拿它当管理员等于把「能进这个房间」放大成「能管所有房间」。
func (s *ClipboardServer) isGlobalAdmin(token string) bool {
	globalPassword := normalizeAuthValue(s.config.Server.Auth)
	return globalPassword != "" && token == globalPassword
}

// isGlobalSessionToken 这个令牌是不是「用全局密码换来的」会话令牌（`scope: "global"`）。
//
// ⚠️ 为什么必须认它：`isGlobalAdmin` 只认**明文**全局密码，而管理页**不存明文密码** ——
// 它拿的是 `/auth/token` 换来的会话令牌（见 handler.go 的 handleAuthToken：
// 用全局密码登录时 `scope = "global"`）。只认明文的话，「管理员」在管理页里等于**不存在**，
// 所有标着「管理员才能做」的能力都会**悄悄失效**（配额不限、跨房间引用、任务归属……），
// 而界面上看不出任何异常 —— 用户只会觉得「配了没反应」。
//
// 这不放大权限：`canAccessRoom` 本来就认这种令牌对所有房间有效
// （见 share_token.go 的 validateRoomSessionToken），而它就是用那个密码换来的 ——
// 本来就是同一回事。
//
// ⚠️ 已经吊销的令牌不算数：全局令牌能进所有房间、还能做房间管理，
// 「登出」如果不管它，等于退了管理员还活着。
func (s *ClipboardServer) isGlobalSessionToken(token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	claims, ok := s.parseRoomSessionToken(token)
	return ok && claims.Scope == "global" && s.sessionClaimsActive(claims)
}

func (s *ClipboardServer) getUploadedFileRoom(uuid string) (string, bool) {
	s.runMutex.Lock()
	defer s.runMutex.Unlock()

	fileInfo, ok := s.uploadFileMap[uuid]
	if !ok {
		return "", false
	}

	return normalizeRoomName(fileInfo.Room), true
}

func (s *ClipboardServer) inferRequestRoom(r *http.Request) string {
	filePrefix := s.config.Server.Prefix + "/file/"
	chunkPrefix := s.config.Server.Prefix + "/upload/chunk/"
	finishPrefix := s.config.Server.Prefix + "/upload/finish/"

	var uuid string
	switch {
	case strings.HasPrefix(r.URL.Path, filePrefix):
		pathPart := strings.TrimPrefix(r.URL.Path, filePrefix)
		uuid = strings.SplitN(pathPart, "/", 2)[0]
	case strings.HasPrefix(r.URL.Path, chunkPrefix):
		uuid = strings.TrimPrefix(r.URL.Path, chunkPrefix)
	case strings.HasPrefix(r.URL.Path, finishPrefix):
		uuid = strings.TrimPrefix(r.URL.Path, finishPrefix)
	}

	// 文件已经登记过就以它自己记录的房间为准，**不能信客户端传的 ?room=**。
	// 否则 `GET /file/<uuid>/<name>?room=default` 就能把受保护房间的文件读出来：
	// 鉴权会去查 default 房间的策略，而 default 往往没设密码，直接放行。
	// ?room= 只留给「文件还没登记」的情况（例如刚创建、尚未落 map 的上传），
	// 那种情况下 handle_file 自己也找不到文件，会 404，不会泄字节。
	if uuid != "" {
		if room, ok := s.getUploadedFileRoom(uuid); ok {
			return room
		}
	}

	if _, hasRoom := r.URL.Query()["room"]; hasRoom {
		return normalizeRoomName(r.URL.Query().Get("room"))
	}

	return "default"
}

// 错误响应统一走 handler.go 的 writeError —— 这里不再单独实现一份，
// 免得同一个状态码在两条路径上给出不同的响应体形状。
