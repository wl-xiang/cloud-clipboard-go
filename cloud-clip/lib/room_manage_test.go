package lib

// 房间管理的回归测试：自建房间的持久化、密码判定、列表可见性、
// 创建 / 删除 / 清理的权限边界。
//
// 为什么值得一整份测试：这套东西的每一条规则都是**安全边界**，而且失败方式全都无声 ——
//   · 公共房间被删掉 → 所有人下次打开落到一个不存在的房间；
//   · 删除没校验密码 → 任何人都能清掉别人的房间；
//   · 列表又把「进不去的房间」过滤掉 → 用户看不到房间，也就无从切换（需求明确要求可见）；
//   · 自建房间的密码没接进鉴权链 → 界面建得出来、却怎么都连不上（最迷惑的一种）。
// 这些都不会崩溃、不会报错，只会在某个时刻表现成「功能坏了」。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// newRoomTestServer 起一个「房间列表开着、存储落在临时目录」的实例。
//
// 与 newStorageTestServer 的区别：那边刻意关掉 RoomList（避免起清理 goroutine），
// 这边正好相反 —— 房间管理接口在 RoomList 关闭时一律 403，关掉就什么都测不了。
// RoomCleanup 保持 0：startRoomCleanup 见到 <=0 会直接返回，不会留下后台 goroutine。
func newRoomTestServer(t *testing.T, configJSON string) *ClipboardServer {
	t.Helper()

	dir := t.TempDir()
	cfg := parseTestConfig(t, configJSON)
	cfg.Server.StorageDir = dir
	cfg.Server.HistoryFile = dir + "/history.json"
	cfg.Server.RoomList = true
	cfg.Server.RoomCleanup = 0
	cfg.Server.History = 50

	s, err := NewClipboardServer(cfg)
	if err != nil {
		t.Fatalf("构造服务器失败: %v", err)
	}
	s.logger = log.New(io.Discard, "", 0)
	if s.roomRegistry == nil {
		t.Fatal("roomRegistry 没有初始化")
	}
	return s
}

func parseTestConfig(t *testing.T, configJSON string) *Config {
	t.Helper()
	cfg := &Config{}
	if strings.TrimSpace(configJSON) == "" {
		configJSON = "{}"
	}
	if err := json.Unmarshal([]byte(configJSON), cfg); err != nil {
		t.Fatalf("解析测试配置失败: %v", err)
	}
	return cfg
}

// doJSON 直接调某个 handler 并返回状态码 + 解析后的 JSON。
func doJSON(t *testing.T, handler http.HandlerFunc, method, target, token string, body string) (int, map[string]interface{}) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler(rec, req)

	out := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func createRoom(t *testing.T, s *ClipboardServer, token, managePassword, name, password string) (int, map[string]interface{}) {
	t.Helper()
	payload := fmt.Sprintf(`{"name":%q,"password":%q}`, name, password)
	req := httptest.NewRequest(http.MethodPost, "/rooms", strings.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if managePassword != "" {
		req.Header.Set("X-Room-Manage-Password", managePassword)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleRooms(rec, req)

	out := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func deleteRoom(t *testing.T, s *ClipboardServer, token, name string) (int, map[string]interface{}) {
	t.Helper()
	return doJSON(t, s.handleRoomItem, http.MethodDelete, "/rooms/"+name, token, "")
}

func listRooms(t *testing.T, s *ClipboardServer, token string) []RoomInfo {
	t.Helper()
	code, body := doJSON(t, s.handleRooms, http.MethodGet, "/rooms", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /rooms 返回 %d，期望 200", code)
	}
	raw, ok := body["rooms"].([]interface{})
	if !ok {
		t.Fatalf("响应里没有 rooms 数组: %v", body)
	}
	// 借 JSON 再解一层，避免手写类型断言。
	encoded, _ := json.Marshal(raw)
	var rooms []RoomInfo
	if err := json.Unmarshal(encoded, &rooms); err != nil {
		t.Fatalf("解析 rooms 失败: %v", err)
	}
	return rooms
}

func findRoom(rooms []RoomInfo, name string) (RoomInfo, bool) {
	for _, room := range rooms {
		if room.Name == name {
			return room, true
		}
	}
	return RoomInfo{}, false
}

// appendTextInRoom 往指定房间塞一条文本条目（房间列表要算消息数）。
// roomMessageCountInTest 数一下某个房间还剩几条消息。
func roomMessageCountInTest(s *ClipboardServer, room string) int {
	count := 0
	s.messageQueue.Lock()
	defer s.messageQueue.Unlock()
	for _, msg := range s.messageQueue.List {
		if normalizeRoomName(msg.Data.Room()) == normalizeRoomName(room) {
			count++
		}
	}
	return count
}

func appendTextInRoom(s *ClipboardServer, room, content string) {
	s.messageQueue.Append(&PostEvent{
		Event: "text",
		Data: ReceiveHolder{TextReceive: &TextReceive{
			ReceiveBase: ReceiveBase{Type: "text", Room: room, Timestamp: 1},
			Content:     content,
		}},
	})
}

// ── 注册表：落盘与重载 ────────────────────────────────────────────────

func TestManagedRoomPersistsAndConfiguresAuth(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass"}}`)

	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("创建房间返回 %d: %v", code, body)
	}

	requirement := s.resolveRoomAuth("财务")
	if !requirement.Required || requirement.Password != "fin-pass" {
		t.Fatalf("自建房间鉴权配置不对: %+v", requirement)
	}

	// 重开一个实例（同一个存储目录）—— 房间必须还在。
	// 这一步才是「落盘」的真正验证：只查内存的话，rooms.json 写没写成都测不出来。
	cfg2 := &Config{}
	cfg2.Server.StorageDir = s.config.Server.StorageDir
	cfg2.Server.HistoryFile = s.config.Server.HistoryFile
	cfg2.Server.RoomList = true
	cfg2.Server.Auth = "global-pass"
	restored, err := NewClipboardServer(cfg2)
	if err != nil {
		t.Fatalf("重启实例失败: %v", err)
	}
	restored.logger = log.New(io.Discard, "", 0)

	entry, ok := restored.roomRegistry.get("财务")
	if !ok {
		t.Fatal("重启后自建房间丢了 —— rooms.json 没有落盘或没有加载")
	}
	if entry.Password != "fin-pass" {
		t.Fatalf("重启后密码丢失: %q", entry.Password)
	}
	if entry.CreatedAt == 0 {
		t.Fatal("创建时间没有被记录")
	}
}

// ── 鉴权：房间密码有效，全局密码仍然是总钥匙 ──────────────────────────

func TestManagedRoomPasswordAndGlobalMasterKey(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass"}}`)
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}

	cases := []struct {
		token string
		want  bool
		why   string
	}{
		{"fin-pass", true, "房间自己的密码"},
		{"global-pass", true, "全局密码是总钥匙（既有语义，别弄丢）"},
		{"", false, "没带凭据"},
		{"wrong", false, "错误密码"},
	}
	for _, c := range cases {
		if got := s.canAccessRoom("财务", c.token); got != c.want {
			t.Errorf("canAccessRoom(财务, %q) = %v，期望 %v（%s）", c.token, got, c.want, c.why)
		}
	}
}

// ── 列表：所有人可见（这正是「切换房间」的前提）────────────────────────

func TestRoomListVisibleToEveryone(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass", "roomAuth": {"vault": {"password": "vault-pass"}}}}`)
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}

	// 一个凭据都不带的调用方：列表里必须**同时**看到公共房间、配置房间、自建房间 ——
	// 看不到房间就无从「切换房间」，这是需求明确要求的。
	rooms := listRooms(t, s, "")
	for _, name := range []string{"", "vault", "财务"} {
		room, ok := findRoom(rooms, name)
		if !ok {
			t.Fatalf("未认证的调用方看不到房间 %q，列表是 %+v", name, rooms)
		}
		if !room.IsProtected {
			t.Errorf("房间 %q 应当标记为需要密码", name)
		}
		if room.CanManage {
			t.Errorf("房间 %q 不该对无凭据的调用方开放删除", name)
		}
	}

	// 公共房间必须被标出来（前端据此隐藏删除入口），且永远不可管理。
	if public, ok := findRoom(rooms, ""); !ok || !public.IsDefault {
		t.Errorf("公共房间没有标记 isDefault: %+v", public)
	}

	// 管理员：自建房间可管理，公共房间仍然不可。
	adminRooms := listRooms(t, s, "global-pass")
	if room, _ := findRoom(adminRooms, "财务"); !room.CanManage {
		t.Error("管理员应当能删自建房间")
	}
	if room, _ := findRoom(adminRooms, ""); room.CanManage {
		t.Error("公共房间不允许删除 —— canManage 必须是 false")
	}
	// 配置里的房间不属于「自建」，界面上也不该给删除入口。
	if room, _ := findRoom(adminRooms, "vault"); room.CanManage {
		t.Error("配置里预置的房间不该允许在界面上删除")
	}
}

// ── 新建：校验与权限 ──────────────────────────────────────────────────

func TestCreateRoomValidationAndPermission(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass", "roomManagePassword": "manage-pass", "roomAuth": {"vault": {"password": "v"}}}}`)

	// ① 房间管理密码是**第一道闸门**：没有它 / 给错了，连校验都到不了。
	if code, _ := createRoom(t, s, "global-pass", "", "无密码房间", "p"); code != http.StatusForbidden {
		t.Errorf("不带管理密码创建房间应当 403，实际 %d", code)
	}
	if code, _ := createRoom(t, s, "global-pass", "wrong", "无密码房间", "p"); code != http.StatusForbidden {
		t.Errorf("管理密码给错应当 403，实际 %d", code)
	}
	// 平台管理员身份**不能**替代管理密码 —— 两把钥匙各管各的
	// （把平台密码给全家共用时，房间管理这把钥匙仍然只在管理员手里）。
	if code, _ := createRoom(t, s, "global-pass", "", "无密码房间2", "p"); code != http.StatusForbidden {
		t.Errorf("持平台密码但无管理密码应当 403，实际 %d", code)
	}

	// ② 字段校验（管理密码给对了之后才轮得到）。
	bad := []struct {
		name     string
		roomName string
		password string
		want     int
		why      string
	}{
		{"空名字", "", "p", http.StatusBadRequest, "房间名不能为空"},
		{"保留名", "default", "p", http.StatusBadRequest, "公共房间是内置的"},
		{"非法字符", "a b", "p", http.StatusBadRequest, "带空格的名字会污染 URL 与列表"},
		{"斜杠", "a/b", "p", http.StatusBadRequest, "斜杠会破坏 /rooms/{name} 路由"},
		{"超长", strings.Repeat("x", 33), "p", http.StatusBadRequest, "超过 32 字符"},
		{"空密码", "新房间", "", http.StatusBadRequest, "房间必须有密码"},
		{"与配置撞名", "vault", "p", http.StatusConflict, "配置里的房间不该被界面改掉"},
	}
	for _, c := range bad {
		code, _ := createRoom(t, s, "global-pass", "manage-pass", c.roomName, c.password)
		if code != c.want {
			t.Errorf("%s: 期望 %d，实际 %d（%s）", c.name, c.want, code, c.why)
		}
	}

	// ③ 合法创建，以及重名 / 中文名。
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("合法创建失败 %d: %v", code, body)
	}
	if code, _ := createRoom(t, s, "global-pass", "manage-pass", "财务", "another"); code != http.StatusBadRequest {
		t.Error("重名房间应当被拒绝")
	}
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "测试房间", "p"); code != http.StatusOK {
		t.Fatalf("中文房间名创建失败 %d: %v", code, body)
	}
}

// ── 删除：权限边界 ────────────────────────────────────────────────────

func TestDeleteRoomPermissionBoundaries(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass", "roomManagePassword": "manage-pass", "roomAuth": {"vault": {"password": "v"}}}}`)
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "人事", "hr-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}

	// ① 公共房间：管理员也不行。它是所有人的兜底落脚点。
	if code, _ := deleteRoom(t, s, "global-pass", "default"); code != http.StatusForbidden {
		t.Errorf("删除公共房间应当 403，实际 %d", code)
	}
	if code, _ := deleteRoom(t, s, "global-pass", ""); code != http.StatusForbidden {
		t.Errorf("删除公共房间（空名字）应当 403，实际 %d", code)
	}
	// ② 配置里的房间：不是自建，不给删。
	if code, _ := deleteRoom(t, s, "global-pass", "vault"); code != http.StatusForbidden {
		t.Errorf("删除配置房间应当 403，实际 %d", code)
	}
	// ③ 自建房间：无凭据 / 错误密码都不行。
	if code, _ := deleteRoom(t, s, "", "财务"); code != http.StatusUnauthorized {
		t.Errorf("无凭据删除应当 401，实际 %d", code)
	}
	if code, _ := deleteRoom(t, s, "wrong-pass", "财务"); code != http.StatusUnauthorized {
		t.Errorf("错误密码删除应当 401，实际 %d", code)
	}
	// ④ 拿**另一个房间**的密码也不行 —— 密码只对它自己那个房间有效。
	if code, _ := deleteRoom(t, s, "hr-pass", "财务"); code != http.StatusUnauthorized {
		t.Errorf("拿别的房间密码删除应当 401，实际 %d", code)
	}
	// ⑤ 该房间自己的密码：可以。
	appendTextInRoom(s, "财务", "待清理")
	if code, body := deleteRoom(t, s, "fin-pass", "财务"); code != http.StatusOK {
		t.Fatalf("用房间密码删除应当成功，实际 %d: %v", code, body)
	}
	if s.roomRegistry.has("财务") {
		t.Error("删除后注册表里还留着")
	}
	if n := roomMessageCountInTest(s, "财务"); n != 0 {
		t.Errorf("删除房间时没有清掉它的消息（还剩 %d 条）", n)
	}
	// ⑥ 管理员：可以删别人的房间。
	if code, _ := deleteRoom(t, s, "global-pass", "人事"); code != http.StatusOK {
		t.Errorf("管理员删除应当成功，实际 %d", code)
	}
	// ⑦ 房间管理密码：同样可以删（这是需求要的第三把钥匙）。
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "运营", "ops-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}
	if code, body := deleteRoom(t, s, "", "运营"); code != http.StatusUnauthorized {
		t.Fatalf("不带任何凭据删除应当 401，实际 %d: %v", code, body)
	}
	if code, body := doJSON(t, s.handleRoomItem, http.MethodDelete, "/rooms/%E8%BF%90%E8%90%A5?managePassword=manage-pass", "", ""); code != http.StatusOK {
		t.Fatalf("用管理密码删除应当成功，实际 %d: %v", code, body)
	}
	if s.roomRegistry.has("运营") {
		t.Error("用管理密码删除后注册表里还留着")
	}
	// 删完之后列表里也不该再有。
	if _, ok := findRoom(listRooms(t, s, "global-pass"), "人事"); ok {
		t.Error("已删除的房间仍在列表里")
	}
}

// ── 删除：有设备在线时拒绝 ────────────────────────────────────────────

func TestDeleteRoomRefusedWhileInUse(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass"}}`)
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "财务", "fin-pass"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}

	// 造一个「有人在这个房间里」的状态：room_ws 与 connDeviceIDMap 都有记录。
	// 用 nil 的 *websocket.Conn 当 key —— 可比较，而且这两个 map 只被当集合用，
	// 测试里不会去调它上面的任何方法。
	var conn *websocket.Conn
	s.runMutex.Lock()
	s.room_ws[conn] = "财务"
	s.connDeviceIDMap[conn] = "device-1"
	s.runMutex.Unlock()

	if code, body := deleteRoom(t, s, "fin-pass", "财务"); code != http.StatusConflict {
		t.Fatalf("房间有人在用时应当 409，实际 %d: %v", code, body)
	}
	if !s.roomRegistry.has("财务") {
		t.Error("被拒绝的删除不该把房间删掉")
	}

	// 人走了之后就可以删了。
	s.runMutex.Lock()
	delete(s.room_ws, conn)
	delete(s.connDeviceIDMap, conn)
	s.runMutex.Unlock()

	if code, _ := deleteRoom(t, s, "fin-pass", "财务"); code != http.StatusOK {
		t.Errorf("设备离开后应当可以删除，实际 %d", code)
	}
}

// ── 清理：只清「没用过」的自建房间 ────────────────────────────────────

func TestRoomCleanupOnlyRemovesUnusedManagedRooms(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass", "roomManagePassword": "manage-pass"}}`)
	for _, room := range []struct{ name, pass string }{{"空房", "p1"}, {"有消息", "p2"}} {
		if code, body := createRoom(t, s, "global-pass", "manage-pass", room.name, room.pass); code != http.StatusOK {
			t.Fatalf("创建 %s 失败 %d: %v", room.name, code, body)
		}
	}
	appendTextInRoom(s, "有消息", "别删我")

	// 管理密码给错 → 403（清理与新建是同一把钥匙，规则不该分叉）。
	if code, _ := doJSON(t, s.handleRoomCleanup, http.MethodPost, "/rooms/cleanup", "fin-pass", ""); code != http.StatusForbidden {
		t.Errorf("管理密码不对清理房间应当 403，实际 %d", code)
	}
	// 手输的房间密码也顶替不了管理密码。
	if code, _ := doJSON(t, s.handleRoomCleanup, http.MethodPost, "/rooms/cleanup?managePassword=p1", "", ""); code != http.StatusForbidden {
		t.Errorf("用房间密码清理房间应当 403，实际 %d", code)
	}

	code, body := doJSON(t, s.handleRoomCleanup, http.MethodPost, "/rooms/cleanup?managePassword=manage-pass", "global-pass", "")
	if code != http.StatusOK {
		t.Fatalf("清理返回 %d: %v", code, body)
	}
	removed, _ := body["removed"].([]interface{})
	removedSet := map[string]bool{}
	for _, item := range removed {
		if name, ok := item.(string); ok {
			removedSet[name] = true
		}
	}
	if !removedSet["空房"] {
		t.Errorf("空房间应当被清理，实际清掉了 %v", removed)
	}
	if removedSet["有消息"] {
		t.Error("还有消息的房间不该被清理")
	}
	if removedSet["default"] {
		t.Error("公共房间永远不该被清理")
	}
	if !s.roomRegistry.has("有消息") {
		t.Error("有消息的自建房间被误删了")
	}
}

// ── 自动清理不能碰自建房间 ────────────────────────────────────────────

func TestPeriodicCleanupNeverTouchesManagedRooms(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass"}}`)
	if code, body := createRoom(t, s, "global-pass", "manage-pass", "长期空房", "p"); code != http.StatusOK {
		t.Fatalf("创建房间失败 %d: %v", code, body)
	}

	// 自动清理只回收「房间统计」，不碰注册表 —— 自建房间要由用户自己删。
	// RoomCleanup=0 会让阈值取 0，任何空闲统计都会被清掉，正好用来验证「注册表不受影响」。
	s.roomStatsMutex.Lock()
	s.roomStats["长期空房"] = &RoomStat{LastActive: 1}
	s.roomStatsMutex.Unlock()

	s.cleanupEmptyRooms()

	if !s.roomRegistry.has("长期空房") {
		t.Fatal("自动清理把自建房间删掉了 —— 自建房间只能由用户显式删除")
	}
	if s.resolveRoomAuth("长期空房").Password != "p" {
		t.Error("自动清理之后自建房间的密码丢了")
	}
}

// ── 清空房间内容 ──────────────────────────────────────────────────────

func TestPurgeRoomContentRemovesOnlyThatRoom(t *testing.T) {
	s := newRoomTestServer(t, `{"server": {"auth": "global-pass"}}`)
	appendTextInRoom(s, "财务", "a")
	appendTextInRoom(s, "财务", "b")
	appendTextInRoom(s, "人事", "c")

	if removed := s.purgeRoomContent("财务"); removed != 2 {
		t.Errorf("应当清掉 2 条，实际 %d", removed)
	}
	if removed := s.purgeRoomContent("财务"); removed != 0 {
		t.Errorf("重复清理应当清掉 0 条，实际 %d", removed)
	}
	if n := roomMessageCountInTest(s, "人事"); n != 1 {
		t.Errorf("不该动到别的房间的消息（人事 应为 1 条，实际 %d）", n)
	}
}
