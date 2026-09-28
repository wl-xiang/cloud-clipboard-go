

### 配置文件说明

`//` 开头的部分是注释，**并不需要写入配置文件中**，否则会导致读取失败。

```json
{
    "server": {
        // 监听的 IP 地址，省略或设为 null 则会监听所有网卡的IP地址
        "host": [
            "127.0.0.1"
        ],
        "port": 9501, // 端口号，falsy 值表示不监听
        "prefix": "", // 部署时的URL前缀，例如想要在 http://localhost/prefix/ 访问，则将这一项设为 /prefix
        "history": 100, // 消息历史记录的数量（默认 100 条）
        "auth": "root1234", // 全局入口密码。**默认开启**（root1234），只要设置了就对所有房间生效；
        // 前端会先弹出全屏密码闸门，验证通过才能进入平台。填 false 表示不启用。
        "roomAuth": {
            "private": "", // 空字符串表示该房间只接受全局 auth
            "finance": "finance-pass", // 非空字符串表示该房间额外接受独立密码
            "public": {"open": true}, // 显式声明该房间**开放**：不要密码，即使全局 auth 设了也一样
            "keep": {"password": "kp", "fileExpire": 0}, // 对象形式：password 为房间密码；fileExpire: 0=该房间文件永不过期，>0=覆盖 file.expire 秒数，不填=使用全局 file.expire
            "archive": {"fileExpire": 604800}, // 也可以只配置 fileExpire（无独立密码）
            "home": {"password": "hp"},
            "lobby": {"open": true} // 显式开放 + 文件与全局一致
        },
        "historyFile": null, // 自定义历史记录存储路径，默认为当前目录的 history.json
        "storageDir": null, // 自定义文件存储目录，默认为临时文件夹的.cloud-clipboard-storage目录
        "roomList": true, // 房间列表 / 房间管理开关，默认 true。
        // 关掉它会把「创建 / 切换 / 删除房间」的入口整块藏起来，一般不要关。
        "roomCleanup": 3600, //房间清理周期(秒)，清理消息数0的房间
        "sessionTTL": 604800, // 登录会话的**滑动有效期**（秒，默认 7 天）：验证一次密码之后这么久之内不必再输
        "sessionLifetime": 2592000 // 会话族的**绝对生存期**（秒，默认 30 天）：从第一次输密码算起，到点必须重新认证
    },
    "text": {
        "limit": 9000 // 文本的长度限制（默认 9000 字符）
    },
    "file": {
        "expire": 3600, // 上传文件的有效期，超过有效期后自动删除，单位为秒
        "chunk": 1048576, // 上传文件的分片大小，不能超过 5 MB，单位为 byte
        "limit": 1073741824 // 上传文件的大小限制，单位为 byte（默认 1GB）
    },
}
```
> HTTPS 的说明：
>
> 建议使用 nginx/caddy 来反向代理
>
> “密码认证”的说明：
>
> 如果启用“密码认证”，只有输入正确的密码才能连接到服务端并查看对应房间的剪贴板内容。
> 可以将 `server.auth` 字段设为 `true`（随机生成密码）或字符串（自定义全局密码）来启用这个功能。
> 如果设置了 `server.auth`，它始终作为全局入口密码，对所有房间生效。
> `server.roomAuth` 不会让 `server.auth` 失效；它只是给指定房间增加一个额外可用密码。
> `server.roomAuth` 中值为空字符串时，该房间只接受全局 `server.auth`；值为非空字符串时，该房间同时接受全局 `server.auth` 和该房间自己的密码。
> 想让某个房间在**全局加密**的前提下保持开放，用 `{ "open": true }`：该房间不要密码，且**不**回落全局密码。这样两种混合都能表达 —— 全局加密 + 个别房间开放，或全局开放 + 个别房间加密。
> `open` 是单独一个字段，不是「把值留空」：空字符串在这份配置里**已经有含义**（只接受全局密码），改掉它会静默改变现有配置 —— 某个房间会悄悄敞开。而且 `open` 还能和 `fileExpire` 一起用（`{"open": true, "fileExpire": 0}` = 开放且文件永不过期），空字符串表达不了这个。
> 同时写了 `open` 和非空 `password` 属于配置写错：**按需要密码处理**（宁可多要一次密码，也不能因为多打了一个字段把房间敞开）。
> 值也可以是对象 `{ "password": "xx", "fileExpire": N }`：`fileExpire` 为 `0` 表示该房间上传的文件永不过期；大于 `0` 表示覆盖全局 `file.expire`（秒）；不填表示沿用全局。注意 `fileExpire` 只影响修改配置之后上传的文件；历史条数轮转删除不受其影响。
> 未通过认证的用户不会在房间列表里看到受保护房间。
>
> “登录会话”的说明：
>
> 密码验证通过之后，服务端签发一枚会话令牌，浏览器把它放在 `HttpOnly` Cookie 里（页面里的脚本读不到），
> 凭据默认 **7 天**内不必再输（`server.sessionTTL`）。之所以敢给这么长，是因为它是**可吊销**的：
> 用户点「退出登录」或者踢掉某台设备，服务端会立刻把对应会话标记为失效，连已经建立的 WebSocket 连接也会一并断开。
> 三个上限同时生效，缺一个都不可：
>   1. **滑动有效期** `sessionTTL`（默认 604800 秒 = 7 天，合法区间 300 ~ 2592000）。正常使用会自动续期，连续七天不用才会被踢。
>   2. **绝对生存期** `sessionLifetime`（默认 2592000 秒 = 30 天）。从**第一次输密码**那刻算起，到点必须重新认证，续签也不会延长它 —— 这是「偷到一枚令牌就能无限续签」的解药。
>      它写进令牌自己的签名里，所以即便会话表（`auth-sessions.json`）被清了，这条约束依然成立。
>   3. **服务端可吊销**（见下面 `POST /auth/logout`）。令牌发出去就收不回来这件事，只在这一层有记账才做得到。
>
> `sessionLifetime` 必须 ≥ `sessionTTL`，否则还没到滑动期限就被判过期（代码会把小的那个提上来）。
> 会话记录在 `<storageDir>/auth-sessions.json`；把这个文件删掉 = 所有人重新输一次密码。
> 另外，连续输错密码会触发登录限速（同一来源短时间内 10 次失败即锁定 15 分钟）——
> 令牌能用七天意味着「猜中一次收益更大」，所以猜的过程必须慢下来。
>
### HTTP API

#### 获取内容

- 方式一: 
```
http://localhost:9501/content/latest  永远返回最新一条内容
http://localhost:9501/content/latest?room=test 永远返回指定房间的最新一条内容
```
- 方式二: 
```
http://localhost:9501/content/1   根据ID访问
http://localhost:9501/content/1?room=test   指定房间
```

#### 发送文本

```console
$ curl -H "Content-Type: text/plain" --data-binary "foobar" http://localhost:9501/text
{"id":"1","type":"text","url":"http://localhost:9501/content/1"}

$ curl http://localhost:9501/content/1
123

$ curl http://localhost:9501/content/1?json=true
{"content":"123","id":"1","timestamp":1748143093,"type":"text"}
```

注意：请求头中不能缺少 `Content-Type: text/plain`

#### 发送文件

```console
$ curl -F file=@image.png http://localhost:9501/upload
{"id":"2","type":"image","url":"http://localhost:9501/content/2"}

$ curl http://localhost:9501/content/2
<a href="http://localhost:9501/file/530a16de-07cb-4835-ba26-64f5e8e1f300/image.png">Found</a>.

$ curl http://localhost:9501/content/2?json=true
{"id":"2","name":"image.png","size":11361,"timestamp":1748175032,"type":"image","url":"http://localhost:9501/file/530a16de-07cb-4835-ba26-64f5e8e1f300","uuid":"530a16de-07cb-4835-ba26-64f5e8e1f300"}

$ curl -L http://localhost:9501/content/2
Warning: Binary output can mess up your terminal. Use "--output -" to tell curl to output it to your terminal anyway,
Warning: or consider "--output <FILE>" to save to a file.
```

#### 在设定房间的情况下发送文本或文件

```console
$ curl -H "Content-Type: text/plain" --data-binary @package.json http://localhost:9501/text?room=reisen-8fce
{"id":"3","type":"text","url":"http://localhost:9501/content/46?room=reisen-8fce"}

$ curl http://localhost:9501/content/3
Not Found

$ curl http://localhost:9501/content/3?room=suika-51ba
Not Found

$ curl http://localhost:9501/content/3?room=reisen-8fce
{
  "name": "cloud-clipboard-server-node",
  ...
}
```

#### 声明设备名称

发送文本、文件或连接 WebSocket（`/push`）时，可以用 `name` 参数声明发送端的显示名；不传则按 `User-Agent` 推断。

```console
$ curl -H "Content-Type: text/plain" --data-binary "来自树莓派" "http://localhost:9501/text?name=RaspberryPi"
$ curl -F file=@image.png "http://localhost:9501/upload?name=RaspberryPi"
```

名字会写进消息的 `senderDevice.name`，Web 端优先显示它，没有才回落 `os` / `type`。主要给快捷指令、脚本这类 `User-Agent` 认不出来的来源用。

- 最多 32 个字符，超出按字符截断（不会截坏多字节字符）
- 控制字符会被剔除，首尾空白会被裁掉
- 名字由客户端随每次请求带着走，服务端不存储、不记忆

#### 密码认证

```console
$ curl -H "Content-Type: text/plain" --data-binary "foobar" http://localhost:9501/text
{"code":"unauthorized","error":"Authentication required","message":"需要认证令牌"}

$ curl -H "Authorization: Bearer xxxx" -H "Content-Type: text/plain" --data-binary "foobar" http://localhost:9501/text
{"id":"7","type":"text","url":"http://localhost:9501/content/7"}

$ curl http://localhost:9501/content/1
{"code":"unauthorized","error":"Authentication required","message":"需要认证令牌"}

$ curl -H "Authorization: Bearer xxxx" http://localhost:9501/content/1
foobar

$ curl  http://localhost:9501/content/1?auth=xxx
foobar
```

> 推荐 API / 脚本使用 `Authorization: Bearer`。`?auth=` 仅为兼容保留，不建议把房间密码写进可分享 URL。

#### 内容格式（`/content/*`）

`/content/<id>` 和 `/content/latest` 用 `?format=raw|json` 决定返回什么：

```console
$ curl "http://localhost:9501/content/7?format=json"
{"id":"7","type":"text","content":"foobar","timestamp":1758000000}

$ curl "http://localhost:9501/content/7?format=raw"
foobar
```

判定优先级（高到低）：

1. **`?format=raw|json`** —— 显式指定，压过其他一切信号
2. `.json` 路径后缀 —— `/content/latest.json`（Android 捷径在用，**不能动**）
3. `?json=1` / `?json=true` —— 旧信号，保留兼容
4. `Accept` 头含 `application/json` —— **只对文本生效**
5. 默认 —— `raw`（纯文本或文件字节）

两点要注意：

- **`Accept` 头对文件不生效**。下载链路上的 Accept 太不可靠（浏览器、下载器、脚本五花八门），
  所以文件分支只认显式信号。否则「浏览器直接点开文件链接」会突然收到一坨 JSON。
- **不认识的 `format` 值返回 400**（`code: unsupported_format`），不会静默回落成 raw ——
  客户端以为拿到 HTML、实际拿到原文，是要出事的。

#### 错误响应

**所有**错误路径都返回同一种形状，`Content-Type: application/json; charset=utf-8`，状态码保持常规语义：

```json
{"code": "text_too_long", "error": "Text too long", "message": "文本内容超出限制 (最大 9000 字符)"}
```

| 字段 | 用途 |
|---|---|
| `code` | 机器码（snake_case），给程序判断。**发布后不要改** |
| `error` | 英文人话，给日志和英文用户看 |
| `message` | 中文人话，给人看 |

Go 与 Cloudflare Worker 两个实现共用这一份契约。**不要按 `Accept` 头分叉成两种响应体**：Apple 快捷指令的「获取URL内容」既不发 `Accept`、也不把 HTTP 状态码暴露给捷径，客户端只能读响应体 —— 同一状态码两种形状等于要求每个客户端各写两套解析逻辑。

常见 `code`：`text_too_long`、`file_too_large`、`content_not_found`、`no_content`、`file_expired`、`unauthorized`、`unauthorized_invalid_token`、`room_forbidden`、`method_not_allowed`、`invalid_request_body`。

**文件/图片是两次请求，两次都要带凭据。** `/content/*` 返回的 `url` 只是一个地址，不含凭据；
客户端必须自己把凭据加在这一次请求上。文本没有这一步（内容内联在 JSON 里），
所以漏带凭据的表现非常像「文本正常、文件/图片 401」。

```console
# 第一次：拿元数据（带凭据）
$ curl -H "Authorization: Bearer xxxx" "http://localhost:9501/content/latest?room=default&json=1"
{"type":"image","name":"m.png","uuid":"<uuid>","url":"http://localhost:9501/file/<uuid>/m.png",...}

# 第二次：按 url 取字节（同样要带凭据，否则 401）
$ curl -H "Authorization: Bearer xxxx" "http://localhost:9501/file/<uuid>/m.png"
```

`/file/<uuid>/<name>` 按**文件自己记录的房间**鉴权，不看你传的 `?room=` —— 传了也不作数。

#### 登录会话（七天免密）

Web 前端用密码换取**会话令牌**，只保存令牌而不保存密码，到期前自动续签。
默认 7 天不必再输密码（详见上面「登录会话」的说明）。

```console
# 用密码换取会话令牌（默认 7 天有效）
$ curl -H "Content-Type: application/json" \
  -d '{"password":"room-pass"}' \
  "http://localhost:9501/auth/token?room=default"
{"token":"...","expiresAt":1710607200,"absoluteExpiresAt":1713213600,"scope":"global","sessionId":"..."}

# 浏览器推荐走 Cookie：令牌进 HttpOnly Cookie，页面脚本读不到（也就不可能被 XSS 偷走）
$ curl -c cookies.txt -H "Content-Type: application/json" \
  -d '{"password":"room-pass","delivery":"cookie"}' \
  "http://localhost:9501/auth/token?room=default"
{"token":"","delivery":"cookie","expiresAt":1710607200,"absoluteExpiresAt":1713213600,"scope":"global"}

# 用仍有效的令牌续签（无需密码），浏览器会提前自动调用；每续一次都会换发新令牌
$ curl -H "Authorization: Bearer <token>" \
  -X POST "http://localhost:9501/auth/token/refresh?room=default"
{"token":"...","expiresAt":1710610800,"absoluteExpiresAt":1713213600,"scope":"global"}

# 退出登录：服务端立刻吊销，**不需要知道令牌是什么也能清掉 Cookie**
$ curl -b cookies.txt -X POST "http://localhost:9501/auth/logout"
{"revoked":1,"all":false}
$ curl -b cookies.txt -X POST -d '{"all":true}' "http://localhost:9501/auth/logout"  # 所有设备下线

# 登录设备管理：看看现在谁在线，把不认识的踢掉
$ curl -b cookies.txt "http://localhost:9501/auth/sessions"
{"sessions":[{"id":"...","room":"default","scope":"global","createdAt":...,"expiresAt":...,
  "lastSeenAt":...,"userAgent":"Mozilla/5.0 ...","createdIp":"10.0.0.9","current":true}],"ttl":604800,"lifetime":2592000}
$ curl -b cookies.txt -X DELETE "http://localhost:9501/auth/sessions?sid=<会话 id>"
{"revoked":1}
```

说明：
- `POST /auth/token` 接受 JSON `{"password":"...","delivery":"token"|"cookie"}`，密码正确返回 `token`、`expiresAt`、`absoluteExpiresAt`、`sessionId`
- `delivery=cookie` 时令牌**不出现在响应体**里，只写进 `HttpOnly + SameSite=Lax` 的 Cookie（`cc_auth`）；HTTPS 下额外加 `Secure`
- Cookie 这条通道只在**同源请求**上生效：跨站请求的 `Sec-Fetch-Site`/`Origin` 不对时，Cookie 里的令牌一律不认（防 CSRF）。`Authorization` 头不受此限，非浏览器客户端照旧
- Cookie 里一个房间占一格：先登录 A 房间再登录 B 房间不会互相挤掉（上限 10 条）
- `scope` 字段表示令牌作用域：用**全局密码**（`server.auth`）登录返回 `"global"`，该令牌对所有房间有效；用**房间专属密码**（`roomAuth`）登录返回 `""`（房间专属）。续签时作用域不变
- `POST /auth/token/refresh` 会**轮换**：签发新令牌的同时把旧的作废（留 2 分钟给还在飞的请求）。有人在该窗口之后还拿着旧令牌来用 → 判定为令牌被复制，**整个会话族作废**
- 续签不能突破 `absoluteExpiresAt`（默认 30 天）；到点这里返回 401 `reauth_required`，要求重新输密码
- `POST /auth/logout` 认所有凭据来源（Body 令牌 / Cookie / Authorization）；`{"all":true}` 需要平台级凭据
- `GET /auth/sessions` 的可见范围有硬边界：平台级会话看得到全部，房间会话只看得到**自己这一族**；`DELETE` 同理 —— 拿房间令牌踢不掉别人的会话
- 吊销会一并掐断对应的 WebSocket 连接（否则要等到下一次重连才生效）
- 连续输错密码会触发限速：同一来源短时间 10 次失败后锁定 15 分钟，期间**正确密码也不放行**（返回 429 + `Retry-After`）
- 会话令牌等价于对应房间密码，仅用于本地保存，**勿放入可分享 URL**
- WebSocket 握手（`/push`）优先使用 `Sec-WebSocket-Protocol` 子协议传递令牌（避免凭据进入 URL/访问日志）；浏览器走 Cookie 时连这个都不需要 —— 握手会自动带上

#### 短期分享链接

用于浏览器复制链接 / 二维码 / `<a href>` 下载，**不暴露房间密码**。  
现有 API 下载方式不变：继续可用房间密码（Header 或 `?auth=`）。

```console
# 需要已拥有房间访问权限（Authorization）
$ curl -H "Authorization: Bearer xxxx" \
  -H "Content-Type: application/json" \
  -d '{"type":"content","id":"7"}' \
  http://localhost:9501/share
{"type":"content","id":"7","room":"default","ttl":900,"expiresAt":1710000000,"token":"...","url":"http://localhost:9501/s/...","pageUrl":"http://localhost:9501/s/...","rawUrl":"http://localhost:9501/content/7?t=..."}

$ curl -H "Authorization: Bearer xxxx" \
  -H "Content-Type: application/json" \
  -d '{"type":"file","uuid":"530a16de-07cb-4835-ba26-64f5e8e1f300","ttl":600}' \
  http://localhost:9501/share
{"type":"file","uuid":"530a16de-...","room":"default","ttl":600,"expiresAt":1710000000,"token":"...","url":"http://localhost:9501/s/...","pageUrl":"http://localhost:9501/s/...","rawUrl":"http://localhost:9501/file/530a16de-.../image.png?t=..."}

# 用短期 token 直连（无需房间密码）
$ curl "http://localhost:9501/content/7?t=..."
$ curl -L "http://localhost:9501/file/530a16de-.../image.png?t=..." -o image.png

# 旧 API 仍然有效
$ curl -H "Authorization: Bearer xxxx" \
  "http://localhost:9501/file/530a16de-.../image.png" -o image.png
```

说明：
- **`url` 就是分享页地址**（`<服务地址><prefix>/s/<token>`），交给收件人的就是它。
  服务端对这条路径返回 SPA 外壳、并把 Open Graph 标签直接注入它的 `<head>` ——
  聊天软件拿这个地址展开预览能拿到真实卡片，真人打开**同一个**地址直接进分享页，
  没有第二跳、也没有第二个地址。
- `pageUrl` 为兼容保留，目前与 `url` **同值**（只认 `pageUrl` 的老客户端照常工作）；
  `rawUrl` 才是带同一个 token 的直连接口地址，下载链路用。
- **一律签发 token**，房间没开密码也发 —— 有效期、次数限制、密码都装在 token 里。
  以前开放房间返回的是裸 `/content/<id>`，`ttl` / `maxUses` 会被静默丢弃。
- `ttl` 可选，默认 900 秒（15 分钟），范围 60～86400
- `maxUses` 可选，默认 `0`（不限次数）；正整数表示最多完整访问次数，范围 1～1000
  - 一次完整 GET（无 Range，或 `bytes=0-...`）计 1 次
  - 视频/文件的 Range 续传（`bytes>0`）与 HEAD 不计入次数
  - 次数在服务端按 token 的 `jti` 计数（Go 进程内存；Cloudflare 优先 D1）
- `password` 可选：设了之后收件人必须提供。**走 `X-Share-Password` 请求头，不进 URL**
  （query 会进浏览器历史和访问日志）；token 里只存 `HMAC(服务端密钥, 密码)` 的前 16 位
- 短期 token 仅授予对应 content/file 的读取权限，不能用于上传/删除

分享页在取正文之前会先问一次 `GET /share?t=<token>`，拿类型 / 文件名 / 大小 / 剩余有效期 /
是否需要密码。**这一步不消耗次数** —— 打开页面本身不该烧掉一次。失败原因可区分：
`share_token_invalid`（无效或过期）、`share_password_required`（没带或带错密码）、
`content_not_found` / `file_not_found`、`file_expired`。

```console
# 15 分钟、最多打开 3 次
$ curl -H "Authorization: Bearer xxxx" -H "Content-Type: application/json" \
  -d '{"type":"content","id":"7","ttl":900,"maxUses":3}' \
  http://localhost:9501/share

# 15 分钟、最多 3 次、且需要密码 hunter2
$ curl -H "Authorization: Bearer xxxx" -H "Content-Type: application/json" \
  -d '{"type":"content","id":"7","ttl":900,"maxUses":3,"password":"hunter2"}' \
  http://localhost:9501/share
```
