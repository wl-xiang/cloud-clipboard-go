package lib

/**
*** FILE: config.go
***   handle config.json <===> Config
**/

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	Server struct {
		Host        interface{} `json:"host"`        //done
		Port        int         `json:"port"`        //done
		Prefix      string      `json:"prefix"`      //done
		History     int         `json:"history"`     //done
		HistoryFile string      `json:"historyFile"` // 添加历史文件路径
		StorageDir  string      `json:"storageDir"`  // 添加存储目录路径
		// Auth    string `json:"auth"`
		Auth     interface{}    `json:"auth"` //done
		RoomAuth RoomAuthConfig `json:"roomAuth"`
		Cert     string         `json:"cert"`
		Key      string         `json:"key"`

		// 添加房间相关配置
		RoomList    bool `json:"roomList"`    // 是否启用房间列表功能
		RoomCleanup int  `json:"roomCleanup"` // 房间清理间隔（秒）
		// RoomManagePassword 房间管理密码（新建 / 删除 / 清理房间都要它）。
		// 与 server.auth 是**两把不同的钥匙**：auth 进平台，这把管房间 ——
		// 把平台密码给全家共用时，房间管理这把钥匙仍然只在管理员手里。
		RoomManagePassword string         `json:"roomManagePassword"`

		// SessionTTL 登录会话的**滑动有效期**（秒）：验证一次密码之后，这么久之内不必再输。
		// 默认 7 天（Docker 侧由 SESSION_TTL_HOURS 覆盖）。合法区间 5 分钟 ~ 30 天。
		SessionTTL int `json:"sessionTTL"`
		// SessionLifetime 会话族的**绝对生存期**（秒）：从第一次输密码那刻算起，
		// 到点必须重新认证 —— 无论中间续签过多少次。
		// 这条是「偷到一枚令牌就能无限续签」的解药，默认 30 天（SESSION_LIFETIME_HOURS）。
		// ⚠️ 它必须 ≥ SessionTTL，否则还没到滑动期限就被判过期，配了等于没配（代码会夹紧）。
		SessionLifetime int `json:"sessionLifetime"`
	} `json:"server"`
	Text struct {
		Limit int `json:"limit"` //done
	} `json:"text"`
	File struct {
		Expire int `json:"expire"` //done
		Chunk  int `json:"chunk"`  //done, but no limit
		Limit  int `json:"limit"`  //done
	} `json:"file"`
}

// var config_path = "config.json"

func load_config(configPath string) (*Config, error) {
	if configPath == "" {
		configPath = "config.json" // 默认配置文件名
	}
	log.Printf("尝试从以下路径加载配置文件: %s\n", configPath)

	defaultConf := defaultConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("无法读取配置文件 '%s': %v. 将使用默认配置.\n", configPath, err)
		// 将默认配置写入文件，如果它不存在或无法读取
		defaultData, marshalErr := json.MarshalIndent(defaultConf, "", "  ")
		if marshalErr == nil {
			if writeErr := os.WriteFile(configPath, defaultData, 0644); writeErr == nil {
				log.Printf("已将默认配置写入: %s\n", configPath)
			} else {
				log.Printf("无法写入默认配置文件 '%s': %v\n", configPath, writeErr)
			}
		}
		return defaultConf, nil // 返回默认配置，不视为致命错误
	}

	// 使用默认配置作为基础，然后用文件中的值覆盖它
	conf := defaultConfig()
	if err := json.Unmarshal(data, conf); err != nil {
		log.Printf("无法解析配置文件 '%s': %v. 将使用默认配置.\n", configPath, err)
		return defaultConf, nil // 返回默认配置，不视为致命错误
	}

	log.Printf("配置文件 %s 加载成功。\n", configPath)
	return conf, nil
}

func defaultConfig() *Config {
	// 检测是否在 OpenWrt 环境中运行
	historyFile := "history.json"
	storageDir := "./uploads"

	// 如果配置文件路径包含 /etc/cloud-clipboard，则认为是 OpenWrt 环境
	if isOpenWrtEnv() {
		historyFile = "/etc/cloud-clipboard/data/history.json"
		storageDir = "/etc/cloud-clipboard/data/upload"
	}

	return &Config{
		Server: struct {
			Host        interface{}    `json:"host"`
			Port        int            `json:"port"`
			Prefix      string         `json:"prefix"`
			History     int            `json:"history"`
			HistoryFile string         `json:"historyFile"`
			StorageDir  string         `json:"storageDir"`
			Auth        interface{}    `json:"auth"`
			RoomAuth    RoomAuthConfig `json:"roomAuth"`
			Cert        string         `json:"cert"`
			Key         string         `json:"key"`
			RoomList    bool           `json:"roomList"`
			RoomCleanup int            `json:"roomCleanup"`
			// 房间管理密码：新建 / 删除 / 清理房间都要它（与 server.auth 两把钥匙）。
			RoomManagePassword string `json:"roomManagePassword"`
			// 登录会话：7 天内免密（滑动），30 天后必须重新输一次密码（绝对）。
			SessionTTL      int `json:"sessionTTL"`
			SessionLifetime int `json:"sessionLifetime"`
		}{
			Host:        []string{"0.0.0.0"},
			Port:        9501,
			Prefix:      "",
			History:     100, // 默认历史条数（Docker 侧由 MESSAGE_NUM 覆盖，见 entrypoint.sh）
			HistoryFile: historyFile,
			StorageDir:  storageDir,
			Auth:        "root1234", // 默认开启访问密码：不带密码进不了平台（Docker 侧由 AUTH_PASSWORD 覆盖）
			RoomAuth:    RoomAuthConfig{},
			Cert:        "",
			Key:         "",
			RoomList:    true, // 默认**开启**：房间列表是「创建/切换/删除房间」的唯一入口，
			// 关掉它等于把房间管理整块藏起来（Docker 侧由 ROOM_LIST 覆盖）
			RoomCleanup: 3600, // 默认1小时清理一次空房间
			// 默认 newroom123：房间管理（新建/删除/清理）必须出示这把钥匙，
			// 否则任何能进平台的人都能随手建房间、删掉别人的房间。
			RoomManagePassword: "newroom123",
			// 7 天：一次验证，七天内不必再输密码（家庭 / 自用场景最常见的诉求）。
			// 之所以敢给这么长，是因为它是**可吊销**的：服务端记账（见 auth_session.go），
			// 登出 / 踢设备立即生效；同时也带着 30 天的绝对上限。
			SessionTTL: defaultSessionTTLSeconds,
			// 30 天：不管续签多少次，一个月总要认一次 「你还是你」。
			SessionLifetime: defaultSessionLifetimeSecs,
		},
		Text: struct {
			Limit int `json:"limit"`
		}{
			// 9000：长文本（多行代码 / 日志片段）贴进来不该被砍。
			// 前端输入框跟着这个值走（app.config.text.limit），组件那边用 max-rows 封顶，
			// 不会因为放长就把发送按钮顶出视口。
			Limit: 9000,
		},
		File: struct {
			Expire int `json:"expire"`
			Chunk  int `json:"chunk"`
			Limit  int `json:"limit"`
		}{
			Expire: 3600,
			Chunk:  1 * _MB,
			Limit:  1024 * _MB, // 默认单文件上限 1GB（Docker 侧由 FILE_LIMIT 覆盖）
		},
	}
}

// normalizeSessionConfig 把会话的两个有效期钳到合法区间（见 Server.SessionTTL 的注释）。
//
// 为什么要单独归一一次，而不是等读取时再钳：这类设置写到 json 里、应到环境变量上，
// 错值会被一个人抄给另一个人。启动时就把**实际生效的值**写回配置对象，
// 后续所有读取点（签发、续签、会话列表）看到的都是同一个数字，不会再各自解释一遍。
func normalizeSessionConfig(cfg *Config, logger *log.Logger) {
	if cfg == nil {
		return
	}
	ttl := cfg.Server.SessionTTL
	if ttl <= 0 {
		ttl = defaultSessionTTLSeconds
	}
	ttl = clampInt(ttl, minSessionTTLSeconds, maxSessionTTLSeconds)

	lifetime := cfg.Server.SessionLifetime
	if lifetime <= 0 {
		lifetime = defaultSessionLifetimeSecs
	}
	lifetime = clampInt(lifetime, minSessionLifetimeSecs, maxSessionLifetimeSecs)
	if lifetime < ttl {
		lifetime = ttl
	}

	cfg.Server.SessionTTL = ttl
	cfg.Server.SessionLifetime = lifetime
	if logger != nil {
		logger.Printf("登录会话: 滑动有效期 %d 秒（%.1f 天），绝对生存期 %d 秒（%.1f 天）",
			ttl, float64(ttl)/86400, lifetime, float64(lifetime)/86400)
	}
}

// 检测是否在 OpenWrt 环境
func isOpenWrtEnv() bool {
	// 方法1: 检查特定文件是否存在
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}

	// 方法2: 检查环境变量
	if os.Getenv("OPENWRT_ENV") == "1" {
		return true
	}

	// 方法3: 检查是否从标准路径启动
	if _, err := os.Stat("/etc/config/cloud-clipboard"); err == nil {
		return true
	}

	return false
}
