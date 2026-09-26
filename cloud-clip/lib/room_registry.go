package lib

// 运行时创建的房间（自建房间）与其密码。
//
// 为什么需要它 —— 之前「房间」是**隐式**存在的：
//   · 有消息 / 有连接 / 有统计，那个房间就算存在；
//   · 密码只能写在配置文件的 `server.roomAuth` 里（部署者预先写死）。
// 结果就是：用户没法自己建房间，也没法删掉不用的房间 —— 只能等自动清理把「空且无连接」
// 的房间回收掉，而且那一刻房间的密码也随之消失（因为密码本来就不在任何地方存着）。
//
// 这个注册表补上缺失的一半：**用户自建、带密码、可删除**的房间，落盘到
// `<storageDir>/rooms.json`。
//
// 与配置里 roomAuth 的关系（两者并存，不是替换）：
//   · `roomAuth`  = 部署者预置的房间（配置文件里的，重启不变，不该被界面改掉）；
//   · 注册表      = 用户通过界面建的房间（可增删）。
//   查密码时**注册表优先**，但创建时会拒绝与 `roomAuth` 重名 —— 所以实际不会冲突。
//   （拒绝而不是「静默覆盖」，是因为部署者的配置文件是他的意图，不该被界面悄悄改掉。）
//
// 落盘范式与 share-log.json / tasks.json 一致：进程内持锁 + 整份原子写。

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ManagedRoom 一个由用户创建的房间。
type ManagedRoom struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	// CreatedAt / CreatedBy 只是运维线索（「这个房间谁建的、什么时候建的」），
	// **不参与鉴权** —— 权限一律按「知不知道这个房间的密码」判定，见 handleRoomItem。
	CreatedAt int64  `json:"createdAt"`
	CreatedBy string `json:"createdBy,omitempty"`
}

// roomNamePattern 自建房间名的合法字符集。
//
// 为什么收紧（而不是原样接受任意字符串）：房间名会出现在 URL 查询参数、房间列表 JSON、
// 以及用户在别处手输的「房间名」里。放开到任意字符的话，`?room=a&x=1`、换行、
// 控制字符这类输入迟早会有人踩到。允许：任意语言的字母、数字、点、下划线、连字符，
// 长度 1~32 —— 中文房间名（`财务`、`测试房间`）照常可用。
//
// ⚠️ 这条限制**只作用于新建的房间**：配置文件里已有的、以及历史上隐式产生的房间名
// （可能含空格等）不受影响，否则升级会把老房间挡在门外。
var roomNamePattern = regexp.MustCompile(`^[\p{L}\p{N}._-]{1,32}$`)

// roomRegistry 自建房间的进程内副本 + 落盘。
type roomRegistry struct {
	mu     sync.RWMutex
	rooms  map[string]ManagedRoom // key 是 normalizeRoomName 之后的名字
	path   string
	logger *log.Logger
}

func newRoomRegistry(path string, logger *log.Logger) *roomRegistry {
	return &roomRegistry{
		rooms:  make(map[string]ManagedRoom),
		path:   path,
		logger: logger,
	}
}

// load 从磁盘读回注册表。文件不存在 = 还没有人建过房间，不是错误。
// 文件损坏时**留档改名**而不是删除（与 history.json 的处理一致），
// 然后以空表启动 —— 宁可让用户重建房间，也不要静默把他们的房间配置删掉。
func (r *roomRegistry) load() error {
	if !pathExists(r.path) {
		r.logger.Printf("房间注册表不存在（%s），以空表启动。", r.path)
		return nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("无法读取房间注册表 %s: %w", r.path, err)
	}
	var rooms []ManagedRoom
	if err := json.Unmarshal(data, &rooms); err != nil {
		quarantined := fmt.Sprintf("%s.corrupt-%s", r.path, time.Now().Format("20060102-150405"))
		if renameErr := os.Rename(r.path, quarantined); renameErr != nil {
			r.logger.Printf("房间注册表解析失败(%v)，改名留档也失败(%v)，原文件保持不动。", err, renameErr)
		} else {
			r.logger.Printf("房间注册表解析失败(%v)，已改名留档到 %s（未删除）。", err, quarantined)
		}
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, room := range rooms {
		name := normalizeRoomName(room.Name)
		if name == "" || name == defaultRoomKey {
			// 公共房间不是「自建房间」，不该出现在注册表里。
			continue
		}
		room.Name = name
		r.rooms[name] = room
	}
	r.logger.Printf("已加载 %d 个自建房间。", len(r.rooms))
	return nil
}

// saveLocked 整份原子写。调用方必须已持有写锁。
func (r *roomRegistry) saveLocked() error {
	list := make([]ManagedRoom, 0, len(r.rooms))
	for _, room := range r.rooms {
		list = append(list, room)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化房间注册表失败: %w", err)
	}
	if err := writeFileAtomic(r.path, data, 0644); err != nil {
		return fmt.Errorf("写入房间注册表 %s 失败: %w", r.path, err)
	}
	return nil
}

func (r *roomRegistry) get(room string) (ManagedRoom, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.rooms[normalizeRoomName(room)]
	return entry, ok
}

func (r *roomRegistry) list() []ManagedRoom {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]ManagedRoom, 0, len(r.rooms))
	for _, room := range r.rooms {
		list = append(list, room)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// add 新建一个房间。名字非法 / 重名 / 空密码都返回错误（错误信息直接给用户看）。
func (r *roomRegistry) add(room ManagedRoom) (ManagedRoom, error) {
	name := normalizeRoomName(room.Name)
	if name == "" || name == defaultRoomKey {
		return ManagedRoom{}, fmt.Errorf("房间名不能为空")
	}
	if !roomNamePattern.MatchString(name) {
		return ManagedRoom{}, fmt.Errorf("房间名只能用字母、数字、点、下划线、连字符，长度 1~32（中文可用）")
	}
	if utf8.RuneCountInString(name) > 32 {
		return ManagedRoom{}, fmt.Errorf("房间名过长（最多 32 个字符）")
	}
	if strings.TrimSpace(room.Password) == "" {
		return ManagedRoom{}, fmt.Errorf("房间必须设置密码")
	}
	if utf8.RuneCountInString(room.Password) > 128 {
		return ManagedRoom{}, fmt.Errorf("房间密码过长（最多 128 个字符）")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rooms[name]; exists {
		return ManagedRoom{}, fmt.Errorf("房间 %q 已存在", name)
	}
	room.Name = name
	room.Password = strings.TrimSpace(room.Password)
	if room.CreatedAt == 0 {
		room.CreatedAt = time.Now().Unix()
	}
	r.rooms[name] = room
	if err := r.saveLocked(); err != nil {
		// 落盘失败就把内存也回滚 —— 否则重启后房间凭空消失，
		// 而当前进程里它还在（用户以为建好了）。
		delete(r.rooms, name)
		return ManagedRoom{}, err
	}
	return room, nil
}

// remove 删除一个房间并落盘。
func (r *roomRegistry) remove(room string) error {
	name := normalizeRoomName(room)
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.rooms[name]
	if !exists {
		return fmt.Errorf("房间 %q 不在注册表里", name)
	}
	delete(r.rooms, name)
	if err := r.saveLocked(); err != nil {
		r.rooms[name] = previous // 同上：落盘失败就回滚
		return err
	}
	return nil
}

// has 房间名是否已被注册表占用（创建前用于查重，避免与 roomAuth 撞名）。
func (r *roomRegistry) has(room string) bool {
	_, ok := r.get(room)
	return ok
}

// roomRegistryPath 自建房间注册表的落点：与 history.json 同目录。
func (s *ClipboardServer) roomRegistryPath() string {
	dir := s.storageFolder
	if s.config != nil && s.config.Server.StorageDir != "" {
		dir = s.config.Server.StorageDir
	}
	return filepath.Join(dir, "rooms.json")
}

// isPlatformAdmin 这次请求带的是不是「平台管理员」凭据。
//
// 两种都算：明文全局密码（curl / 脚本），
// 以及用它换来的会话令牌（界面 —— 那边刻意不存明文密码）。
func (s *ClipboardServer) isPlatformAdmin(token string) bool {
	return s.isGlobalAdmin(token) || s.isGlobalSessionToken(token)
}

// platformAuthRequired 这台服务器有没有开平台级密码。
// 没开 = 谁都能建房间（自托管常见）；开了 = 必须持有平台凭据。
func (s *ClipboardServer) platformAuthRequired() bool {
	return normalizeAuthValue(s.config.Server.Auth) != ""
}
