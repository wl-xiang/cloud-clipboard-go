package lib

// 房间管理的 HTTP 面：列表 / 新建 / 删除 / 清理。
//
// 权限模型（三句话）：
//   1. **公共房间不允许删除** —— 它是所有人的兜底落脚点。
//   2. **只能删除自建房间**（rooms.json 里的）—— 配置里预置的房间属于部署者，
//      不该被界面悄悄删掉。想删就去改配置文件。
//   3. 删自建房间要**持有它的密码**，或者持有**平台管理员凭据**
//      （平台全局密码，或用它换来的会话令牌）。
//
// 「查看列表」是对所有人开放的（要能看到有哪些房间才谈得上切换），
// 但**进入**每个房间仍然要那个房间的密码 —— 列表里 isProtected 就是给这个用的。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// roomCORS 三个房间接口共用的 CORS 头。DELETE 也要放行 —— 少了它，
// 浏览器会在预检阶段就把删除请求拦掉，而现象是「点了没反应」（控制台才有红字）。
func (s *ClipboardServer) roomCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Room-Auth-Tokens")
}

// requestIsPlatformAdmin 这次请求带的是不是平台管理员凭据。
//
// 凭据来源与其它接口一致：`Authorization: Bearer <token>`（前端 axios 拦截器自动带）
// 或 `?auth=`。两处都收，curl / 快捷指令才不用为了管理功能换一套写法。
func (s *ClipboardServer) requestIsPlatformAdmin(r *http.Request) bool {
	for _, token := range extractAuthTokens(r) {
		if s.isPlatformAdmin(token) {
			return true
		}
	}
	return false
}

// roomInfoFor 组装单个房间的展示信息。
//
// 与 getRoomList 里那段是**同一套字段**，所以抽出来给「刚创建的房间」复用 ——
// 新建完直接把这条返回给前端，省一次整表拉取。
func (s *ClipboardServer) roomInfoFor(room string, tokens []string, admin bool) RoomInfo {
	normalized := normalizeRoomName(room)

	deviceCount := 0
	s.runMutex.Lock()
	for conn, connRoom := range s.room_ws {
		if normalizeRoomName(connRoom) == normalized {
			if _, ok := s.connDeviceIDMap[conn]; ok {
				deviceCount++
			}
		}
	}
	s.runMutex.Unlock()

	messageCount := 0
	s.messageQueue.Lock()
	for _, msg := range s.messageQueue.List {
		if normalizeRoomName(msg.Data.Room()) == normalized {
			messageCount++
		}
	}
	s.messageQueue.Unlock()

	var lastActive int64
	s.roomStatsMutex.RLock()
	if stat, ok := s.roomStats[normalized]; ok {
		lastActive = stat.LastActive
	}
	s.roomStatsMutex.RUnlock()
	if deviceCount > 0 {
		lastActive = time.Now().Unix()
	}

	displayRoom := normalized
	isDefault := normalized == defaultRoomKey
	if isDefault {
		displayRoom = ""
	}

	return RoomInfo{
		Name:         displayRoom,
		MessageCount: messageCount,
		DeviceCount:  deviceCount,
		LastActive:   lastActive,
		IsActive:     deviceCount > 0,
		IsProtected:  s.resolveRoomAuth(normalized).Required,
		IsDefault:    isDefault,
		CanManage:    s.canManageRoom(normalized, tokens, admin),
		CreatedAt:    s.roomCreatedAt(normalized),
	}
}

func (s *ClipboardServer) roomCreatedAt(room string) int64 {
	if s.roomRegistry == nil {
		return 0
	}
	if managed, ok := s.roomRegistry.get(room); ok {
		return managed.CreatedAt
	}
	return 0
}

// canManageRoom 当前请求方能不能删这个房间。
//
// ⚠️ 这只是**给界面用的提示**，不是权限边界 —— handleRoomItem 里会重新判一次。
// 前端隐藏按钮，服务端照样要拦（UI 隐藏 ≠ 权限）。
func (s *ClipboardServer) canManageRoom(room string, tokens []string, admin bool) bool {
	normalized := normalizeRoomName(room)
	if normalized == defaultRoomKey {
		return false // 公共房间永远不可删
	}
	if s.roomRegistry == nil || !s.roomRegistry.has(normalized) {
		// 不是自建房间（配置预置的、或历史上隐式产生的）→ 界面不给删。
		return false
	}
	if admin {
		return true
	}
	for _, token := range tokens {
		if token != "" && s.tokenMatchesRoom(normalized, token) {
			return true
		}
	}
	return false
}

// roomManagePasswordOK 这次请求出示的是不是**房间管理密码**。
//
// 它与 server.auth 是两把不同的钥匙：auth 进平台，这把管房间。
// 凭据来源（任一）：`X-Room-Manage-Password` 请求头、`?managePassword=` 查询参数、
// 请求体里的 `managePassword` 字段。
//
// ⚠️ 部署时把该值留空 = 房间管理**不设防**（自托管单用户的合理选择）。
// 默认配置会带一把 newroom123，所以默认是设防的。
func (s *ClipboardServer) roomManagePasswordOK(r *http.Request, bodyPassword string) bool {
	expected := strings.TrimSpace(s.config.Server.RoomManagePassword)
	if expected == "" {
		return true
	}
	candidates := []string{
		r.Header.Get("X-Room-Manage-Password"),
		r.URL.Query().Get("managePassword"),
		bodyPassword,
	}
	for _, given := range candidates {
		if strings.TrimSpace(given) == expected {
			return true
		}
	}
	return false
}

// roomManageGate 房间管理动作（新建 / 删除 / 清理）共用的闸门。
// 返回 false 时已经写好响应，调用方直接 return。
func (s *ClipboardServer) roomManageGate(w http.ResponseWriter, r *http.Request, bodyPassword string) bool {
	if s.roomManagePasswordOK(r, bodyPassword) {
		return true
	}
	writeError(w, http.StatusForbidden, "manage_password_required", "Room manage password required",
		"需要房间管理密码才能执行这个操作")
	return false
}

// handleRooms 房间列表（GET）与新建房间（POST）。
func (s *ClipboardServer) handleRooms(w http.ResponseWriter, r *http.Request) {
	s.roomCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if !s.config.Server.RoomList {
		writeError(w, http.StatusForbidden, "room_list_disabled", "Room list disabled", "房间列表功能未启用")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tokens := extractAuthTokens(r)
		admin := s.requestIsPlatformAdmin(r)
		roomList := s.getRoomList(tokens, admin)
		s.logger.Printf("返回房间列表，包含 %d 个房间（管理员: %v）", len(roomList), admin)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(RoomListResponse{Rooms: roomList}); err != nil {
			s.logger.Printf("错误: 编码房间列表响应失败: %v", err)
		}
	case http.MethodPost:
		s.handleRoomCreate(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET/POST is allowed", "仅允许 GET / POST 请求")
	}
}

// handleRoomCreate 新建一个自建房间。密码是**必填**的 ——
// 「房间准入都要有密码，跟公共房间一样」是这次需求的核心，所以这里不给「先建后补」的口子。
func (s *ClipboardServer) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	// 房间管理密码：新建房间必须出示它（除非部署时把该值留空）。
	// 不再接受「平台管理员身份」替代 —— 把平台密码给全家共用时，
	// 房间管理这把钥匙仍然只在管理员手里，这正是配置两把钥匙的意义。
	if !s.roomManageGate(w, r, "") {
		return
	}
	if s.roomRegistry == nil {
		writeError(w, http.StatusInternalServerError, "room_registry_unavailable", "Registry unavailable", "房间注册表不可用")
		return
	}

	var payload struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	// 限长读 body：这个接口只收两个短字段，不设上限等于给了一个免费的放大点。
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid body", "请求格式不正确")
		return
	}

	name := normalizeRoomName(payload.Name)
	if name == defaultRoomKey {
		writeError(w, http.StatusBadRequest, "reserved_room_name", "Reserved name", "「公共房间」是内置房间，不能新建同名房间")
		return
	}
	// 与配置里的预置房间查重：部署者的配置文件是他的意图，不该被界面悄悄改掉，
	// 更不该出现「配置里一个密码、注册表里另一个密码」这种双头状态。
	if _, exists := s.config.Server.RoomAuth[name]; exists {
		writeError(w, http.StatusConflict, "room_exists_in_config", "Defined in server config",
			"该房间名已在服务端配置里定义，请换一个名字")
		return
	}

	room, err := s.roomRegistry.add(ManagedRoom{
		Name:      name,
		Password:  payload.Password,
		CreatedBy: get_remote_ip(r),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "room_create_failed", "Create failed", err.Error())
		return
	}

	s.logger.Printf("已创建自建房间: %s（创建者 IP: %s）", room.Name, room.CreatedBy)
	tokens := extractAuthTokens(r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"room": s.roomInfoFor(room.Name, tokens, s.requestIsPlatformAdmin(r)),
	})
}

// handleRoomItem 删除指定房间（DELETE /rooms/<name>）。
func (s *ClipboardServer) handleRoomItem(w http.ResponseWriter, r *http.Request) {
	s.roomCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only DELETE is allowed", "仅允许 DELETE 请求")
		return
	}
	if s.roomRegistry == nil {
		writeError(w, http.StatusInternalServerError, "room_registry_unavailable", "Registry unavailable", "房间注册表不可用")
		return
	}

	raw := strings.TrimPrefix(r.URL.Path, s.config.Server.Prefix+"/rooms/")
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	name := normalizeRoomName(raw)

	// ① 公共房间：永远不可删。它不是「一个房间」，是所有人的兜底落脚点。
	if name == defaultRoomKey {
		writeError(w, http.StatusForbidden, "default_room_protected", "Default room protected",
			"公共房间是默认房间，不允许删除")
		return
	}
	// ② 只能删自建房间。配置预置的 / 历史隐式产生的房间不在这里删 ——
	// 前者属于部署者（去改配置文件），后者该由「清空 + 自动清理」回收。
	if !s.roomRegistry.has(name) {
		writeError(w, http.StatusForbidden, "room_not_managed", "Not a user-created room",
			"这个房间不是自助创建的，不能在这里删除（服务端配置的房间请改配置文件）")
		return
	}
	// ③ 凭据：房间管理密码 / 该房间自己的密码 / 平台管理员 —— 任一即可。
	// 手输的管理密码放在查询参数里（Authorization 已被拦截器占住，塞不进第二个值）。
	managePassword := r.URL.Query().Get("managePassword")
	if !s.roomManagePasswordOK(r, managePassword) && !s.canManageRoom(name, extractAuthTokens(r), s.requestIsPlatformAdmin(r)) {
		writeError(w, http.StatusUnauthorized, "room_delete_forbidden", "Not allowed to delete",
			"无权删除该房间：需要该房间的密码，或管理员权限")
		return
	}
	// ④ 还有设备在里面 → 先不删。
	//    删掉一个正被使用的房间，房间里的人不会收到任何提示（连接还开着），
	//    他们只会在某次刷新后突然落到公共房间 —— 那比「删不掉」难解释得多。
	if devices := s.roomDeviceCount(name); devices > 0 {
		writeError(w, http.StatusConflict, "room_in_use", "Room in use",
			"房间当前还有设备在线，请先让它们离开再删除")
		return
	}

	if err := s.roomRegistry.remove(name); err != nil {
		writeError(w, http.StatusInternalServerError, "room_delete_failed", "Delete failed", err.Error())
		return
	}
	removed := s.purgeRoomContent(name)
	s.logger.Printf("已删除自建房间: %s（同时清理 %d 条消息）", name, removed)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":           true,
		"name":         name,
		"removedCount": removed,
	})
}

// handleRoomCleanup 批量清理「无用房间」（POST /rooms/cleanup，管理员）。
//
// 判定「无用」= **没有消息 + 没有设备在线**。
// 只清自建房间（注册表）与空的房间统计，公共房间永远跳过。
// 这里**不等** roomCleanup 那个时间间隔：用户是主动点「清理」的，
// 他的意图就是「现在清掉」，而不是「等一小时后清掉」。
func (s *ClipboardServer) handleRoomCleanup(w http.ResponseWriter, r *http.Request) {
	s.roomCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed", "仅允许 POST 请求")
		return
	}
	// 与「新建」同一把钥匙：清理也是改房间这件事，不该用不同的规则。
	if !s.roomManageGate(w, r, "") {
		return
	}
	if s.roomRegistry == nil {
		writeError(w, http.StatusInternalServerError, "room_registry_unavailable", "Registry unavailable", "房间注册表不可用")
		return
	}

	active := s.activeRoomSet()
	withMessages := s.roomSetWithMessages()

	var removed []string
	for _, room := range s.roomRegistry.list() {
		if room.Name == defaultRoomKey || active[room.Name] || withMessages[room.Name] {
			continue
		}
		if err := s.roomRegistry.remove(room.Name); err != nil {
			s.logger.Printf("清理房间 %s 失败: %v", room.Name, err)
			continue
		}
		removed = append(removed, room.Name)
	}

	// 顺带回收空的房间统计（这些是历史上隐式产生的房间留下的壳）。
	s.roomStatsMutex.Lock()
	for room, stat := range s.roomStats {
		if room == defaultRoomKey || active[room] || withMessages[room] {
			continue
		}
		delete(s.roomStats, room)
		_ = stat
		removed = append(removed, room)
	}
	s.roomStatsMutex.Unlock()

	sort.Strings(removed)
	s.logger.Printf("房间清理完成，共清理 %d 个无用房间", len(removed))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "removed": removed})
}

// purgeRoomContent 清掉某个房间在内存里的全部内容（消息 + 文件 + 统计）并落盘。
// 返回清掉的消息条数。
//
// 刻意**不删**物理上传文件：文件可能已经通过分享链接被外面引用，
// 物理回收交给原有的过期清理（file expire）。删房间删掉的是「这个房间里的条目」。
func (s *ClipboardServer) purgeRoomContent(room string) int {
	normalized := normalizeRoomName(room)

	s.messageQueue.Lock()
	kept := s.messageQueue.List[:0]
	removed := 0
	for _, msg := range s.messageQueue.List {
		if normalizeRoomName(msg.Data.Room()) == normalized {
			removed++
			continue
		}
		kept = append(kept, msg)
	}
	s.messageQueue.List = kept
	s.messageQueue.Unlock()

	s.runMutex.Lock()
	for uuid, fileInfo := range s.uploadFileMap {
		if normalizeRoomName(fileInfo.Room) == normalized {
			delete(s.uploadFileMap, uuid)
		}
	}
	s.runMutex.Unlock()

	s.roomStatsMutex.Lock()
	delete(s.roomStats, normalized)
	s.roomStatsMutex.Unlock()

	s.saveHistoryData()
	return removed
}

// roomDeviceCount 房间当前在线的设备数。
func (s *ClipboardServer) roomDeviceCount(room string) int {
	normalized := normalizeRoomName(room)
	count := 0
	s.runMutex.Lock()
	for conn, connRoom := range s.room_ws {
		if normalizeRoomName(connRoom) == normalized {
			if _, ok := s.connDeviceIDMap[conn]; ok {
				count++
			}
		}
	}
	s.runMutex.Unlock()
	return count
}

// activeRoomSet 有设备在线的房间集合。
func (s *ClipboardServer) activeRoomSet() map[string]bool {
	set := make(map[string]bool)
	s.runMutex.Lock()
	for _, room := range s.room_ws {
		set[normalizeRoomName(room)] = true
	}
	s.runMutex.Unlock()
	return set
}

// roomSetWithMessages 还有消息的房间集合。
func (s *ClipboardServer) roomSetWithMessages() map[string]bool {
	set := make(map[string]bool)
	s.messageQueue.Lock()
	for _, msg := range s.messageQueue.List {
		set[normalizeRoomName(msg.Data.Room())] = true
	}
	s.messageQueue.Unlock()
	return set
}
