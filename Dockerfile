#
# 从**源码**构建 Cloud Clipboard Go（前端 + 后端一体）。
#
# ⚠️ 刻意**不写** `# syntax=docker/dockerfile:1`：那条指令会让 buildkit 去 Docker Hub
# 拉 `docker/dockerfile` 这个前端镜像，网络抖一下（实测遇到 Bad Gateway）整次构建就直接失败。
# 本文件没有用到任何 BuildKit 专有语法（没有 heredoc、没有 RUN --mount），
# 内置前端完全够用 —— 少一个外部依赖，构建就少一个失败面。
#
# 与 cloud-clip/Dockerfile 的区别（那个仍然保留，用途不同）：
#   · cloud-clip/Dockerfile：从 GitHub Release 下载**已构建好的**二进制，只为发布镜像提速，
#     **不含本仓库未发布的改动**；
#   · 本文件：三段式真源码构建 —— 用它才能把本次的前端/后端改动打进镜像。
#
#   ① web      node  → 构建 Vue 前端，产物直接落进 Go 的内嵌目录
#   ② gobuild  golang → 把上一步的前端产物一起 `-tags embed` 编进二进制
#   ③ runtime  alpine → 只留二进制 + entrypoint + mkcert，体积与发布镜像一致
#
# 构建（在仓库根目录执行）：
#   docker build -t cloud-clipboard-go:local .
# 运行：
#   docker compose up -d   # docker-compose.yml 里把 image 指向本地 tag 即可

# ─────────────────────────────────────────────────────────────
# ① 前端构建
# ─────────────────────────────────────────────────────────────
FROM node:22-alpine AS web

WORKDIR /src/web-vue3

# 先只拷依赖清单：源码改动不会让这一层缓存失效，重复构建快很多。
# ⚠️ 用 `npm ci` 而不是 `npm install`：构建必须可复现，必须严格按 lock 文件装。
COPY web-vue3/package.json web-vue3/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web-vue3/ ./

# 注：仓库根的 `shortcuts/` **不需要**拷进来 —— 前端那套「快捷指令下载」入口已经移除，
# 构建里不再有 `sync-shortcuts.mjs` 这一步（见 package.json 的 build 脚本）。
# 那个目录仍留在仓库里，供 Cloudflare Worker 版本与快捷指令契约测试使用。

# DEPLOY_STATIC=1 让 after-build.js 把 dist（含 .gz/.br）拷进 ../cloud-clip/lib/static，
# 也就是 Go 侧 go:embed 的那个唯一静态目录。
# ⚠️ 目录由脚本自己 rm+重建，不要在这里预建 —— 预建了反而会把上一次的残留留下。
RUN DEPLOY_STATIC=1 npm run build

# ─────────────────────────────────────────────────────────────
# ② 后端构建（前端产物内嵌进二进制）
# ─────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS gobuild

WORKDIR /src/cloud-clip

COPY cloud-clip/go.mod cloud-clip/go.sum ./
RUN go mod download

COPY cloud-clip/ ./
# 前端产物覆盖到 go:embed 的目标目录。注意 .dockerignore 里排除了
# cloud-clip/lib/static，所以镜像内**不会**残留上一次构建的前端 ——
# 万一这一步没跑到，构建会直接因 `go:embed static: no matching files` 失败，
# 而不是把旧前端悄悄打进镜像。
COPY --from=web /src/cloud-clip/lib/static ./lib/static

# ⚠️ 版本变量的**包路径是 lib 而不是 main**：`server_version` 声明在
# cloud-clip/lib/main.go（package lib），仓库 Makefile 里写的 `-X main.server_version`
# 其实一直没生效（链接器对不存在的符号是静默忽略的）。这里用完整包路径，
# 让 `===== Cloud Clipboard Server <ver> =====` 真的打得出这次构建的标识。
ARG CC_VERSION=docker-src
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -tags embed \
        -ldflags="-s -w -X github.com/jonnyan404/cloud-clipboard-go/cloud-clip/lib.server_version=${CC_VERSION}" \
        -o /out/cloud-clipboard-go \
        .

# ─────────────────────────────────────────────────────────────
# ③ 运行时
# ─────────────────────────────────────────────────────────────
FROM alpine:latest

# 运行时依赖与发布镜像保持一致：
#   ca-certificates 出网校验证书；netcat 给 HEALTHCHECK 用；
#   git 供 entrypoint 里 mkcert 相关流程；tzdata 让定时任务按本地时区走。
# mkcert 只在 edge/testing 有，单独一行加 —— 它是 HTTPS 自签证书能力的来源。
RUN apk add --no-cache ca-certificates netcat-openbsd git tzdata \
    && apk add --repository=https://dl-cdn.alpinelinux.org/alpine/edge/testing mkcert

WORKDIR /app/server-node

COPY cloud-clip/entrypoint.sh /app/entrypoint.sh
COPY --from=gobuild /out/cloud-clipboard-go /app/server-node/cloud-clipboard-go

# entrypoint 从 Windows 检出时可能带 CRLF，会让 `/bin/sh` 报
# `not found`（实际是解释器名字后面多了个 \r）——统一在这里剥掉。
RUN sed -i 's/\r$//' /app/entrypoint.sh \
 && chmod +x /app/entrypoint.sh /app/server-node/cloud-clipboard-go

EXPOSE 9501

HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=10s \
    CMD nc -z 127.0.0.1 "${LISTEN_PORT:-9501}" || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
