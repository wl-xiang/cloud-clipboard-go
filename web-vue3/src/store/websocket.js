import { defineStore } from 'pinia';
import axios from 'axios';
import router from '@/router';
import { APP_BASE_URL } from '@/base.js';
import { useAppStore } from './app';

const ROOM_AUTH_CACHE_KEY = 'roomAuthCache';
const DEFAULT_ROOM_KEY = '__default__';
const GLOBAL_ROOM_KEY = '__global__';

// 会话/刷新策略（见 scheduleAuthRefresh）：
// 令牌本身默认 7 天有效，只要还在用就**每天顶一次**，让「一直在用的人」永远不必再输密码。
// 顶不破的是服务端那条绝对生存期（默认 30 天），前端再勤刷新也没用 —— 那是刻意的。
const AUTH_REFRESH_WINDOW_SECONDS = 24 * 60 * 60;
const AUTH_REFRESH_LEAD_SECONDS = 60;

// 凭据怎么从服务端交到浏览器手上。
// **同源**时一律走 `cookie`：服务端把它塞进 HttpOnly Cookie，前端 JS 从头到尾接触不到令牌，
// 页面上的任意脚本（包括第三方库被打进供应链攻击的那种）都偷不走登录态。
// 跨源部署时浏览器默认不携带 Cookie，那时退回「令牌 + localStorage」—— 功能一致，
// 只是少了 HttpOnly 那层防护。
function cookieDeliveryAvailable() {
    try {
        return window.location.origin === new URL(APP_BASE_URL).origin;
    } catch {
        return false;
    }
}
const AUTH_DELIVERY = cookieDeliveryAvailable() ? 'cookie' : 'token';

function normalizeAuthEntry(value) {
    if (typeof value === 'string' && value) {
        return { token: value, expiresAt: 0, delivery: 'token', sessionId: '' };
    }
    if (value && typeof value === 'object') {
        return {
            token: typeof value.token === 'string' ? value.token : '',
            expiresAt: Number(value.expiresAt) || 0,
            delivery: value.delivery === 'cookie' ? 'cookie' : 'token',
            sessionId: typeof value.sessionId === 'string' ? value.sessionId : '',
        };
    }
    return null;
}

function loadRoomAuthCache() {
    // ⚠️ 存在 localStorage、**不是** sessionStorage：后者关掉标签页就没了，
    // 那样七天的会话在第一天晚上就消失了 —— 用户体感还是「天天要输密码」。
    // 这里存的也不是凭据本体：Cookie 模式下只是「登录过、什么时候到期」的标记，
    // 令牌始终留在浏览器的 HttpOnly Cookie 里。
    try {
        const raw = localStorage.getItem(ROOM_AUTH_CACHE_KEY) || sessionStorage.getItem(ROOM_AUTH_CACHE_KEY);
        if (!raw) {
            return {};
        }
        const parsed = JSON.parse(raw);
        if (!parsed || typeof parsed !== 'object') {
            return {};
        }
        const normalized = {};
        Object.entries(parsed).forEach(([key, value]) => {
            const entry = normalizeAuthEntry(value);
            if (entry) {
                normalized[key] = entry;
            }
        });
        return normalized;
    } catch {
        return {};
    }
}

export const useWebSocketStore = defineStore('websocket', {
    state: () => ({
        websocket: null,
        websocketConnecting: false,
        authCode: '',
        inputPassword: '',
        authCodeDialog: false,
        authPendingRoom: '',
        authCodeError: '',
        authDialogLoading: false,
        roomAuthCache: loadRoomAuthCache(),
        roomProtectionCache: {},
        authRefreshTimer: null,
        room: '',
        roomInput: '',
        roomDialog: false,
        retry: 0,
        heartbeatTimer: null,
        pingTimer: null,
        latency: null,
        pendingReceiveQueue: [],
        receiveFlushTimer: null,
    }),

    getters: {
        // 用于请求鉴权：与旧 $root.getRequestAuthToken 等价
        currentRoom() {
            return this.normalizeRoomName(this.room);
        },
    },

    actions: {
        initFromRoute(roomQuery) {
            this.room = this.normalizeRoomName(roomQuery || '');
        },

        /* ---------- 房间/鉴权工具 ---------- */
        normalizeRoomName(room = '') {
            const normalized = (room || '').trim();
            return normalized === 'default' ? '' : normalized;
        },
        getRoomStorageKey(room = this.room) {
            return this.normalizeRoomName(room) || DEFAULT_ROOM_KEY;
        },
        persistRoomAuthCache() {
            try {
                localStorage.setItem(ROOM_AUTH_CACHE_KEY, JSON.stringify(this.roomAuthCache));
                sessionStorage.removeItem(ROOM_AUTH_CACHE_KEY); // 老版本的存档一次性搬家到这里
            } catch {
                // 隐私模式下 localStorage 可能不可写 —— 最坏是关掉浏览器要重新登录，不该崩
            }
        },
        getGlobalAuthToken() {
            const entry = this.roomAuthCache[GLOBAL_ROOM_KEY];
            if (typeof entry === 'string') {
                return entry;
            }
            if (entry && typeof entry === 'object' && typeof entry.token === 'string') {
                return entry.token;
            }
            return '';
        },
        // hasGlobalSession 当前是不是用**平台密码**登录的。
        //
        // ⚠️ Cookie 模式下前端拿不到令牌本体（这正是它的意义），所以「我是不是管理员」
        // 不能再靠「有没有令牌」判断 —— 那样会让 Cookie 模式下的管理员功能全部静默失效
        // （房间管理、配额豁免……界面上看不出为什么不能用）。这里改成「有没有标记」。
        hasGlobalSession() {
            const entry = normalizeAuthEntry(this.roomAuthCache[GLOBAL_ROOM_KEY]);
            if (!entry) {
                return false;
            }
            if (entry.expiresAt > 0 && entry.expiresAt <= Math.floor(Date.now() / 1000)) {
                return false;
            }
            return entry.delivery === 'cookie' || Boolean(entry.token);
        },
        getEffectiveAuthEntry(room = this.room) {
            const now = Math.floor(Date.now() / 1000);
            const read = key => {
                const entry = normalizeAuthEntry(this.roomAuthCache[key]);
                if (!entry) {
                    return null;
                }
                // Cookie 模式下没有令牌是正常的 —— 凭据在 HttpOnly Cookie 里，这条路只作标记
                const usable = entry.delivery === 'cookie' ? true : Boolean(entry.token);
                if (!usable) {
                    return null;
                }
                if (entry.expiresAt > 0 && entry.expiresAt <= now) {
                    return null;
                }
                return { token: entry.token, expiresAt: entry.expiresAt, delivery: entry.delivery, key };
            };
            return read(this.getRoomStorageKey(room)) || read(GLOBAL_ROOM_KEY);
        },
        getAuthTokenForRoom(room = this.room) {
            const effective = this.getEffectiveAuthEntry(room);
            return effective ? effective.token : '';
        },
        // hasRoomSession 这个房间现在有没有有效期内的登录状态（不限交付方式）。
        hasRoomSession(room = this.room) {
            return Boolean(this.getEffectiveAuthEntry(room));
        },
        cacheAuthTokenForRoom(room, token, expiresAt = 0, delivery = AUTH_DELIVERY) {
            const normalizedToken = (token || '').trim();
            const key = this.getRoomStorageKey(room);
            if (!normalizedToken && delivery !== 'cookie') {
                this.clearAuthTokenForRoom(room);
                return;
            }
            const existing = this.roomAuthCache[key];
            const effectiveExpiresAt = Number(expiresAt) > 0
                ? Number(expiresAt)
                : (existing && typeof existing === 'object' && Number(existing.expiresAt) > 0 ? Number(existing.expiresAt) : 0);
            this.roomAuthCache[key] = {
                token: normalizedToken,
                expiresAt: effectiveExpiresAt,
                delivery: delivery === 'cookie' ? 'cookie' : 'token',
                sessionId: '',
            };
            this.persistRoomAuthCache();
            if (this.normalizeRoomName(room) === this.currentRoom) {
                this.authCode = normalizedToken;
            }
            this.scheduleAuthRefresh(room);
        },
        clearAuthTokenForRoom(room = this.room) {
            const key = this.getRoomStorageKey(room);
            if (Object.prototype.hasOwnProperty.call(this.roomAuthCache, key)) {
                delete this.roomAuthCache[key];
                this.persistRoomAuthCache();
            }
            if (this.normalizeRoomName(room) === this.currentRoom) {
                this.authCode = '';
                this.clearAuthRefreshTimer();
            }
        },
        // clearAllAuthCache 丢掉全部本地登录标记（退出登录用）。
        // ⚠️ 服务端那份是真正被吊销的东西 —— 这里的清理只是让界面立刻回到「未登录」状态。
        clearAllAuthCache() {
            this.roomAuthCache = {};
            try {
                localStorage.removeItem(ROOM_AUTH_CACHE_KEY);
                sessionStorage.removeItem(ROOM_AUTH_CACHE_KEY);
            } catch {
                // 同上：不可写不影响功能
            }
            this.authCode = '';
            this.clearAuthRefreshTimer();
        },
        getKnownAuthTokens(room = this.room) {
            const tokens = [];
            const push = token => {
                let value = token;
                if (token && typeof token === 'object' && typeof token.token === 'string') {
                    value = token.token;
                }
                const normalized = String(value || '').trim();
                if (normalized && !tokens.includes(normalized)) {
                    tokens.push(normalized);
                }
            };
            push(this.getAuthTokenForRoom(room));
            push(this.authCode);
            Object.values(this.roomAuthCache).forEach(push);
            return tokens;
        },
        clearAuthRefreshTimer() {
            if (this.authRefreshTimer) {
                clearTimeout(this.authRefreshTimer);
                this.authRefreshTimer = null;
            }
        },
        setRoomProtection(room, isProtected) {
            this.roomProtectionCache[this.normalizeRoomName(room)] = Boolean(isProtected);
        },
        async fetchServerInfo(room = this.room, { token = '' } = {}) {
            const response = await axios.get('server', {
                params: new URLSearchParams([['room', this.normalizeRoomName(room)]]),
                headers: token ? { Authorization: `Bearer ${token}` } : undefined,
                __skipRoomAuthHandling: true,
            });
            if (Object.prototype.hasOwnProperty.call(response.data || {}, 'roomProtected')) {
                this.setRoomProtection(room, response.data.roomProtected);
            }
            // 每一次 /server 都是「平台闸门要不要开着」的最新依据 ——
            // 登录成功后 connect() 会再问一次，那时 authorized 变 true，闸门自己就撤了。
            useAppStore().setAuthState({
                globalAuth: response.data?.globalAuth === true,
                authorized: response.data?.authorized !== false,
            });
            return response.data;
        },
        async verifyRoomAccess(room, token) {
            if (!token) {
                return false;
            }
            const serverInfo = await this.fetchServerInfo(room, { token });
            return serverInfo.auth ? serverInfo.authorized === true : true;
        },
        openAuthDialog(room, initialToken = '') {
            this.authPendingRoom = this.normalizeRoomName(room);
            this.roomDialog = false;
            this.inputPassword = '';
            this.authCodeError = '';
            this.authDialogLoading = false;
            this.authCodeDialog = true;
        },
        async resolveAuthTokenForRoom(room, { interactive = true } = {}) {
            const normalizedRoom = this.normalizeRoomName(room);
            const cachedToken = this.getAuthTokenForRoom(normalizedRoom);
            if (cachedToken) {
                return cachedToken;
            }
            const serverInfo = await this.fetchServerInfo(normalizedRoom);
            if (!serverInfo.auth) {
                return '';
            }
            const candidates = this.getKnownAuthTokens(normalizedRoom);
            for (const token of candidates) {
                if (await this.verifyRoomAccess(normalizedRoom, token)) {
                    return token;
                }
            }
            if (interactive) {
                this.openAuthDialog(normalizedRoom);
            }
            return null;
        },
        async obtainRoomSessionToken(room, password) {
            try {
                const response = await axios.post('auth/token', { password, delivery: AUTH_DELIVERY }, {
                    params: new URLSearchParams([['room', this.normalizeRoomName(room)]]),
                    __skipRoomAuthHandling: true,
                });
                const data = response.data || {};
                return {
                    token: data.token || '',
                    // 老版本服务端不认 delivery 参数：它照样会返回 token ——
                    // 那就当本次是 token 模式走，别把自己卡在「等一块不会来的 Cookie」上。
                    delivery: data.delivery === 'cookie' ? 'cookie' : (data.token ? 'token' : AUTH_DELIVERY),
                    expiresAt: Number(data.expiresAt) || 0,
                    sessionId: data.sessionId || '',
                    scope: data.scope === 'global' ? 'global' : '',
                };
            } catch (error) {
                console.error('Failed to obtain session token:', error);
                return null;
            }
        },
        async refreshRoomSessionToken(room) {
            const normalizedRoom = this.normalizeRoomName(room);
            const effective = this.getEffectiveAuthEntry(normalizedRoom);
            if (!effective) {
                return null;
            }
            try {
                const response = await axios.post('auth/token/refresh', { delivery: AUTH_DELIVERY }, {
                    params: new URLSearchParams([['room', normalizedRoom]]),
                    __skipRoomAuthHandling: true,
                });
                const data = response.data || {};
                return {
                    token: data.token || '',
                    delivery: data.delivery === 'cookie' ? 'cookie' : (data.token ? 'token' : effective.delivery),
                    expiresAt: Number(data.expiresAt) || 0,
                    sessionId: data.sessionId || '',
                    scope: data.scope === 'global' ? 'global' : '',
                };
            } catch (error) {
                console.error('Failed to refresh session token:', error);
                return null;
            }
        },
        scheduleAuthRefresh(room = this.room) {
            this.clearAuthRefreshTimer();
            const normalizedRoom = this.normalizeRoomName(room);
            if (normalizedRoom !== this.currentRoom) {
                return;
            }
            const effective = this.getEffectiveAuthEntry(normalizedRoom);
            if (!effective || !effective.expiresAt) {
                return;
            }
            const remainingSeconds = effective.expiresAt - Math.floor(Date.now() / 1000);
            // 「离到期还早」不等于「不用续」：七天是**滑动**的，趁早顶一次，
            // 用户永远碰不到那条边界；真等快到期再去续，那时可能已经掉过一次线了。
            const delaySeconds = remainingSeconds > AUTH_REFRESH_WINDOW_SECONDS
                ? remainingSeconds - AUTH_REFRESH_WINDOW_SECONDS
                : Math.max(0, remainingSeconds - AUTH_REFRESH_LEAD_SECONDS);
            this.authRefreshTimer = setTimeout(async () => {
                this.authRefreshTimer = null;
                const refreshed = await this.refreshRoomSessionToken(normalizedRoom);
                if (refreshed && (refreshed.token || refreshed.delivery === 'cookie') && refreshed.expiresAt) {
                    const cacheRoom = refreshed.scope === 'global' ? GLOBAL_ROOM_KEY : effective.key;
                    this.cacheAuthTokenForRoom(cacheRoom, refreshed.token, refreshed.expiresAt, refreshed.delivery);
                    return;
                }
                // 续不动 = 会话被吊销了（登出 / 被踢 / 超过绝对生存期）。
                // 别再安排下一次，直接清干净并让用户重新认证 —— 否则会无限重试。
                this.clearAuthTokenForRoom(normalizedRoom);
                if (normalizedRoom === this.currentRoom) {
                    this.openAuthDialog(normalizedRoom);
                }
            }, delaySeconds * 1000);
        },

        getWebSocketEndpoint(room = this.room) {
            const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
            // ⚠️ 必须用 APP_BASE_URL（从 `document.baseURI` 推导），**不能**用
            // `app.config.server.prefix`。
            //
            // 后者只能从 WebSocket 握手时那条 `config` 事件拿到（见本文件 `case 'config'`），
            // 而这里正是**为了连上 WebSocket** 才拼地址 —— 首次连接时它还是空串，
            // 于是拼出 `/push` 而不是 `/clip/push`，服务端直接 404。
            // 这是个鸡生蛋：要连上才知道 prefix，要知道 prefix 才能连上。
            //
            // APP_BASE_URL 没有这个问题：它从文档目录推导，服务端在深路径上会注入
            // `<base href="<prefix>/">`（见 lib/spa_shell.go），页面加载时就确定了。
            // （Issue #23；#7「prefix 不更新到链接」是同一根因的另一面。）
            const wsUrl = new URL('push', APP_BASE_URL);
            wsUrl.protocol = protocol;
            const normalizedRoom = this.normalizeRoomName(room);
            if (normalizedRoom) {
                wsUrl.searchParams.set('room', normalizedRoom);
            }
            return wsUrl.toString();
        },

        /* ---------- 连接 ---------- */
        async connect() {
            const app = useAppStore();
            if (this.websocketConnecting) {
                return;
            }
            this.websocketConnecting = true;
            try {
                const currentRoom = this.normalizeRoomName(this.room);
                let resolvedToken = this.getAuthTokenForRoom(currentRoom);

                // 只要这条路走得通，就顺便把续期排上 ——
                // 包括「浏览器关了又打开」的情形：那时只有本地标记，从来没人排过定时器。
                this.scheduleAuthRefresh(currentRoom);

                // 无论是否已缓存 token，都先探测 /server 以可靠获知房间是否需要认证。
                // 若仅在 app.config?.auth 为真时才探测，首次加载（config 需在认证后才会收到）
                // 受保护房间时会永远探测不到，导致既不连接也不弹认证窗口。
                const serverInfo = await this.fetchServerInfo(currentRoom);
                // Cookie 模式下 `authorized` 已经把服务端的登录态算进去了 ——
                // 只要它是 true，就说明这次连接不需要任何额外凭据（连 WS 子协议都不用带）。
                if (serverInfo.auth && serverInfo.authorized !== true && !resolvedToken) {
                    // 平台级闸门（server.auth）没通过时**不弹房间对话框**：
                    // 那时全屏闸门已经盖住了整个界面，再叠一个房间对话框只会让人不知道该填哪个。
                    // 密码由闸门统一收，通过后这里有 token，直接往下走。
                    if (serverInfo.globalAuth === true && serverInfo.authorized !== true) {
                        this.websocketConnecting = false;
                        return;
                    }
                    resolvedToken = await this.resolveAuthTokenForRoom(currentRoom, { interactive: true });
                    if (resolvedToken === null) {
                        this.websocketConnecting = false;
                        return;
                    }
                }

                const wsUrl = this.getWebSocketEndpoint(currentRoom);
                const protocols = resolvedToken ? [resolvedToken] : [];

                const ws = await new Promise((resolve, reject) => {
                    const socket = new WebSocket(wsUrl, protocols);
                    socket.onopen = () => resolve(socket);
                    socket.onerror = reject;
                });

                this.websocket = ws;
                this.websocketConnecting = false;
                this.retry = 0;
                this.authCode = resolvedToken || this.getAuthTokenForRoom(currentRoom);
                if (this.heartbeatTimer) {
                    clearInterval(this.heartbeatTimer);
                }
                const heartbeat = () => {
                    if (this.websocket && this.websocket.readyState === WebSocket.OPEN) {
                        this.websocket.send('');
                    }
                };
                this.heartbeatTimer = setInterval(heartbeat, 30000);
                const ping = () => {
                    if (this.websocket && this.websocket.readyState === WebSocket.OPEN) {
                        try {
                            this.websocket.send(JSON.stringify({ event: 'ping', data: Date.now() }));
                        } catch {}
                    }
                };
                ping();
                this.pingTimer = setInterval(ping, 3000);
                ws.onclose = async () => {
                    if (this.heartbeatTimer) {
                        clearInterval(this.heartbeatTimer);
                        this.heartbeatTimer = null;
                    }
                    if (this.pingTimer) {
                        clearInterval(this.pingTimer);
                        this.pingTimer = null;
                    }
                    this.latency = null;
                    this.websocket = null;
                    this.websocketConnecting = false;
                    app.device = [];
                    if (this.retry < 3) {
                        this.retry++;
                        setTimeout(() => this.connect(), 3000);
                        return;
                    }
                    // 重试耗尽后，若服务器仍要求认证（可能因 token 失效/过期），
                    // 清除本地 token 并弹出认证窗口，否则静默失败用户无法感知。
                    try {
                        const info = await this.fetchServerInfo(this.room);
                        if (info.auth) {
                            this.clearAuthTokenForRoom(this.room);
                            this.openAuthDialog(this.room);
                        }
                    } catch {
                        this.openAuthDialog(this.room);
                    }
                };
                ws.onmessage = e => {
                    try {
                        const parsed = JSON.parse(e.data);
                        this.handleEvent(parsed.event, parsed.data);
                    } catch {}
                };
            } catch (error) {
                this.websocketConnecting = false;
                this.failure();
            }
        },
        syncRoomView(targetRoom) {
            const app = useAppStore();
            const normalizedRoom = this.normalizeRoomName(targetRoom);
            const cached = app.roomMessagesCache[normalizedRoom];
            if (cached && Array.isArray(cached)) {
                app.received = [...cached];
            } else {
                app.received = [];
            }
        },
        saveRoomCache(room = this.room) {
            const app = useAppStore();
            const normalizedRoom = this.normalizeRoomName(room);
            app.roomMessagesCache[normalizedRoom] = [...app.received];
        },
        flushPendingReceives() {
            if (this.receiveFlushTimer) {
                clearTimeout(this.receiveFlushTimer);
                this.receiveFlushTimer = null;
            }
            if (!this.pendingReceiveQueue.length) {
                return;
            }
            const app = useAppStore();
            const newItems = this.pendingReceiveQueue.splice(0);
            this.mergeMessages(newItems);
        },
        mergeMessages(incomingItems) {
            const app = useAppStore();
            if (!incomingItems || !incomingItems.length) {
                return;
            }
            const currentList = [...app.received];
            const existingIdMap = new Map();
            currentList.forEach((item, index) => {
                existingIdMap.set(item.id, index);
            });

            for (const item of incomingItems) {
                if (existingIdMap.has(item.id)) {
                    const idx = existingIdMap.get(item.id);
                    currentList[idx] = { ...currentList[idx], ...item };
                } else {
                    currentList.push(item);
                }
            }

            // 按时间倒序排列 (最新的排在最前)
            currentList.sort((a, b) => (Number(b.timestamp) || 0) - (Number(a.timestamp) || 0));

            // 如果有配置历史条数限制，进行截断
            const limit = Number(app.config?.server?.history || 0);
            if (limit > 0 && currentList.length > limit) {
                currentList.splice(limit);
            }

            app.received = currentList;
            this.saveRoomCache();
        },
        queueReceive(data) {
            this.pendingReceiveQueue.unshift(data);
            if (!this.receiveFlushTimer) {
                this.receiveFlushTimer = setTimeout(() => {
                    this.flushPendingReceives();
                }, 32);
            }
        },
        handleEvent(event, data) {
            const app = useAppStore();
            switch (event) {
                case 'receive':
                    this.queueReceive(data);
                    break;
                case 'receiveMulti':
                    this.flushPendingReceives();
                    this.mergeMessages(Array.isArray(data) ? data : [data]);
                    break;
                case 'revoke': {
                    this.flushPendingReceives();
                    const index = app.received.findIndex(e => e.id === data.id);
                    if (index !== -1) {
                        app.received.splice(index, 1);
                        this.saveRoomCache();
                    }
                    break;
                }
                case 'config': {
                    this.flushPendingReceives();
                    app.config = data;
                    console.log(
                        `%c Cloud Clipboard ${data.version} by Jonnyan404 %c https://github.com/Jonnyan404/cloud-clipboard-go `,
                        'color:#fff;background-color:#1e88e5',
                        'color:#fff;background-color:#64b5f6'
                    );
                    break;
                }
                case 'connect':
                    app.device.push(data);
                    break;
                case 'disconnect': {
                    const index = app.device.findIndex(e => e.id === data.id);
                    if (index !== -1) {
                        app.device.splice(index, 1);
                    }
                    break;
                }
                case 'update': {
                    this.flushPendingReceives();
                    const index = app.received.findIndex(e => e.id === data.id);
                    if (index !== -1) {
                        app.received.splice(index, 1, { ...app.received[index], ...data });
                        this.saveRoomCache();
                    }
                    break;
                }
                case 'forbidden': {
                    this.flushPendingReceives();
                    this.clearAuthTokenForRoom(this.room);
                    this.openAuthDialog(this.room);
                    break;
                }
                case 'pong': {
                    if (typeof data === 'number' && data > 0) {
                        const rtt = Date.now() - data;
                        if (rtt >= 0) {
                            this.latency = rtt;
                        }
                    }
                    break;
                }
            }
        },
        disconnect() {
            const app = useAppStore();
            this.websocketConnecting = false;
            if (this.websocket) {
                this.websocket.onopen = null;
                this.websocket.onmessage = null;
                this.websocket.onerror = null;
                this.websocket.onclose = null;
                this.websocket.close();
                this.websocket = null;
            }
            this.clearAuthRefreshTimer();
            if (this.heartbeatTimer) {
                clearInterval(this.heartbeatTimer);
                this.heartbeatTimer = null;
            }
            if (this.pingTimer) {
                clearInterval(this.pingTimer);
                this.pingTimer = null;
            }
            if (this.receiveFlushTimer) {
                clearTimeout(this.receiveFlushTimer);
                this.receiveFlushTimer = null;
            }
            this.pendingReceiveQueue = [];
            this.saveRoomCache();
            app.device = [];
        },
        switchRoom(targetRoom) {
            const app = useAppStore();
            const oldRoom = this.normalizeRoomName(this.room);
            const newRoom = this.normalizeRoomName(targetRoom);
            if (oldRoom === newRoom) {
                return;
            }
            // 1. 先把当前视图（旧房间内容）保存回旧房间的 cache，避免混入新房间
            app.roomMessagesCache[oldRoom] = [...app.received];
            // 2. 断开旧连接并清空所有 handler，防止旧连接的残留消息写入新房间
            this.websocketConnecting = false;
            if (this.websocket) {
                this.websocket.onopen = null;
                this.websocket.onmessage = null;
                this.websocket.onerror = null;
                this.websocket.onclose = null;
                this.websocket.close();
                this.websocket = null;
            }
            this.clearAuthRefreshTimer();
            if (this.heartbeatTimer) {
                clearInterval(this.heartbeatTimer);
                this.heartbeatTimer = null;
            }
            if (this.pingTimer) {
                clearInterval(this.pingTimer);
                this.pingTimer = null;
            }
            this.latency = null;
            if (this.receiveFlushTimer) {
                clearTimeout(this.receiveFlushTimer);
                this.receiveFlushTimer = null;
            }
            this.pendingReceiveQueue = [];
            app.device = [];
            // 3. 切换当前房间
            this.room = newRoom;
            // 4. 载入新房间的 cache
            this.syncRoomView(newRoom);
            // 5. 连接新房间
            this.connect();
            // 6. 同步地址栏（使用 replace 避免历史堆栈膨胀）
            const targetQuery = newRoom ? { room: newRoom } : {};
            router.replace({ path: '/', query: targetQuery });
        },
        failure() {
            const app = useAppStore();
            this.websocket = null;
            this.latency = null;
            if (this.pingTimer) {
                clearInterval(this.pingTimer);
                this.pingTimer = null;
            }
            app.device = [];
            if (this.retry++ < 3) {
                this.connect();
            } else {
                // 连接失败提示
            }
        },
        handleHttpUnauthorized(config = {}) {
            const room = this.getRequestRoom(config);
            this.clearAuthTokenForRoom(room);
            this.openAuthDialog(room);
        },
        getRequestRoom(config = {}) {
            if (config.params instanceof URLSearchParams) {
                return this.normalizeRoomName(config.params.get('room') || this.room);
            }
            if (config.params && typeof config.params === 'object' && config.params.room !== undefined) {
                return this.normalizeRoomName(config.params.room);
            }
            return this.normalizeRoomName(this.room);
        },
        getRequestAuthToken(config = {}) {
            return this.getAuthTokenForRoom(this.getRequestRoom(config));
        },
        async navigateToRoom(room) {
            const normalizedRoom = this.normalizeRoomName(room);
            const targetQuery = normalizedRoom ? { room: normalizedRoom } : {};
            const currentQuery = router.currentRoute.value.query;
            if (normalizedRoom === this.normalizeRoomName(currentQuery.room || '')) {
                return true;
            }

            const isProtected = this.roomProtectionCache[normalizedRoom];
            // 明确标记为「开放」的房间不必问；其余（含未知）一律问一次服务端 ——
            // `/server?room=` 的 `authorized` 由服务端算，Cookie 里的登录态也算在内。
            if (isProtected !== false) {
                try {
                    const info = await this.fetchServerInfo(normalizedRoom);
                    if (info.auth && info.authorized !== true) {
                        const token = await this.resolveAuthTokenForRoom(normalizedRoom, { interactive: true });
                        if (token === null) {
                            return false;
                        }
                    }
                } catch {
                    // 探测失败不拦 —— 跳过去之后 connect() 还会再试一次
                }
            }

            await router.push({ path: '/', query: targetQuery });
            return true;
        },
        // 平台闸门提交。和房间认证**分开**：房间那套要处理「切房间 / 待进入房间」，
        // 平台这层只有一件事 —— 拿全局令牌、重连、让 /server 把 authorized 翻成 true。
        async submitPlatformPassword(password) {
            const value = (password || '').trim();
            if (!value || this.authDialogLoading) {
                return false;
            }
            this.authDialogLoading = true;
            this.authCodeError = '';
            try {
                const response = await axios.post('auth/token', { password: value, delivery: AUTH_DELIVERY }, {
                    params: new URLSearchParams([['room', this.normalizeRoomName(this.room)]]),
                    __skipRoomAuthHandling: true,
                });
                const data = response.data || {};
                const delivery = data.delivery === 'cookie' ? 'cookie' : (data.token ? 'token' : AUTH_DELIVERY);
                // Cookie 模式下**故意没有 token**：凭据留在 HttpOnly Cookie 里，
                // 前端拿不到也不需要拿 —— 见 auth_session.go 里 Cookie 那条路的说明。
                if (delivery !== 'cookie' && !data.token) {
                    this.authCodeError = 'authInvalid';
                    return false;
                }
                // scope=global 说明填的是**平台密码**（不是某个房间的密码）——
                // 缓存到 GLOBAL_ROOM_KEY，这样任何房间都能用它（服务端 tokenMatchesRoom 认全局令牌）。
                const cacheKey = data.scope === 'global' ? GLOBAL_ROOM_KEY : this.normalizeRoomName(this.room);
                this.cacheAuthTokenForRoom(cacheKey, data.token || '', Number(data.expiresAt) || 0, delivery);
                this.inputPassword = '';
                this.retry = 0;
                // 重连会重新拉一次 /server，闸门据此自己撤掉。
                await this.connect();
                return true;
            } catch (error) {
                console.error('Platform authentication failed:', error);
                this.authCodeError = 'authInvalid';
                return false;
            } finally {
                this.authDialogLoading = false;
            }
        },
        async submitAuthCodeForPendingRoom() {
            const targetRoom = this.authPendingRoom || this.currentRoom;
            const password = (this.inputPassword || '').trim();
            if (!password || this.authDialogLoading) {
                return;
            }
            this.authDialogLoading = true;
            this.authCodeError = '';
            try {
                const verified = await this.verifyRoomAccess(targetRoom, password);
                if (!verified) {
                    this.authCodeError = 'authInvalid';
                    return;
                }
                const session = await this.obtainRoomSessionToken(targetRoom, password);
                if (!session) {
                    this.authCodeError = 'connectionFailedRetry';
                    return;
                }
                if (session.delivery !== 'cookie' && !session.token) {
                    this.authCodeError = 'connectionFailedRetry';
                    return;
                }
                const cacheKey = session.scope === 'global'
                    ? GLOBAL_ROOM_KEY
                    : this.getRoomStorageKey(targetRoom);
                this.cacheAuthTokenForRoom(cacheKey, session.token, session.expiresAt, session.delivery);
                this.inputPassword = '';
                this.authCodeDialog = false;
                this.authPendingRoom = '';
                if (this.normalizeRoomName(targetRoom) !== this.normalizeRoomName(this.room)) {
                    await this.navigateToRoom(targetRoom);
                    return;
                }
                this.retry = 0;
                this.connect();
            } catch (error) {
                console.error(error);
                this.authCodeError = 'connectionFailedRetry';
            } finally {
                this.authDialogLoading = false;
            }
        },

        /* ---------- 登出与登录设备管理 ---------- */

        // logout 退出登录。
        //
        // ⚠️ 必须打到服务端：只删 localStorage 里的标记是「眼不见为净」，服务端那份会话还活着，
        // 拿到过令牌的人照样能继续用。服务端吊销之后令牌立刻失效、WebSocket 也会被掐断，
        // 本地这份清理只是让界面马上回到未登录的样子。
        //
        // all=true：连同其它所有设备一起踢下线（需要当前是平台级会话）。
        async logout({ all = false } = {}) {
            try {
                await axios.post('auth/logout', { all }, {
                    params: new URLSearchParams([['room', this.normalizeRoomName(this.room)]]),
                    __skipRoomAuthHandling: true,
                });
            } catch (error) {
                // 网络出错也照常清本地：用户点了退出，界面就不该还显示登录态
                console.error('Logout request failed:', error);
            }
            const hadSession = this.hasRoomSession(this.room);
            this.clearAllAuthCache();
            this.disconnect();
            this.retry = 0;
            if (hadSession) {
                // 重连会把 /server 再问一遍：平台闸门或房间密码框自己就回来了
                this.connect();
            }
            return true;
        },

        // fetchSessions 列出服务端当前可见的登录会话（见云端的 GET /auth/sessions）。
        // 平台级会话看得到全部；房间会话只看得到自己那一族 —— 这是服务端的边界，前端不用重复实现。
        async fetchSessions() {
            try {
                const response = await axios.get('auth/sessions', { __skipRoomAuthHandling: true });
                const data = response.data || {};
                return {
                    sessions: Array.isArray(data.sessions) ? data.sessions : [],
                    ttl: Number(data.ttl) || 0,
                    lifetime: Number(data.lifetime) || 0,
                };
            } catch (error) {
                console.error('Failed to load sessions:', error);
                return null;
            }
        },

        // revokeSession 踢掉某一台设备。sid === 'all' 表示「全部设备下线」。
        async revokeSession(sid) {
            const target = String(sid || '').trim();
            if (!target) {
                return false;
            }
            try {
                await axios.delete('auth/sessions', {
                    params: new URLSearchParams([['sid', target]]),
                    __skipRoomAuthHandling: true,
                });
                return true;
            } catch (error) {
                console.error('Failed to revoke session:', error);
                return false;
            }
        },
    },
});
