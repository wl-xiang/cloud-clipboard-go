<script setup>
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import { useAppStore } from '@/store/app';
import { useWebSocketStore } from '@/store/websocket';
import { useTheme } from 'vuetify';
import { useDisplay } from 'vuetify';
import { useI18n } from 'vue-i18n';
import axios from 'axios';
import { toast, toastState } from '@/plugins/toast';
import TraditionalColorDialog from '@/components/TraditionalColorDialog.vue';
import ShareHistoryDialog from '@/components/ShareHistoryDialog.vue';
import SessionManagerDialog from '@/components/SessionManagerDialog.vue';
import RoomList from '@/components/RoomList.vue';
import QrcodeVue from 'qrcode.vue';
import { errorMessage } from '@/util.js';
import { buildId } from '@/sw-update.js';
import { MODES } from '@/views/modes/registry.js';
import { DISPLAY_GROUPS, togglesForMode } from '@/data/displayToggles.js';
import { LAYOUT_OPTIONS, VIEW_OPTIONS } from '@/data/workspace.js';

const mdiBrightness4 = 'mdi-brightness-4';
const mdiLoginVariant = 'mdi-login-variant';
const mdiChevronRight = 'mdi-chevron-right';
const mdiDockLeft = 'mdi-dock-left';
const mdiDockRight = 'mdi-dock-right';
const mdiClose = 'mdi-close';
const mdiContentPaste = 'mdi-content-paste';
const mdiDiceMultiple = 'mdi-dice-multiple';
const mdiGithub = 'mdi-github';
const mdiOpenInNew = 'mdi-open-in-new';
const mdiPalette = 'mdi-palette';
const mdiPaletteSwatch = 'mdi-palette-swatch';
const mdiShareVariant = 'mdi-share-variant';
const mdiTranslate = 'mdi-translate';
const mdiDevices = 'mdi-devices';
const mdiLogout = 'mdi-logout';
const mdiLogoutVariant = 'mdi-logout-variant';

const app = useAppStore();
const ws = useWebSocketStore();
const theme = useTheme();
const isDark = computed(() => theme.current.value?.dark ?? false);
const display = useDisplay();
const { t, locale } = useI18n();
const router = useRouter();
const route = useRoute();
// 分享页走裸壳：不渲染工具栏 / 房间侧栏 / 设置面板（见模板顶部的 v-if）
const isShareRoute = computed(() => Boolean(route.meta?.sharePage));

// 模式跟着地址走 —— **一个 tab 一个模式**。初值在 store 里从 `?mode=` 取（见 store/app.js），
// 这里负责在切换时把它写回地址，这样刷新、复制链接、开新 tab 都能复现同一个模式。
// ⚠️ 用 replace 不用 push：每切一次模式就往历史里塞一条的话，后退键会变成
// 「回到上一个模式」而不是「回到上一页」。
//
// ⚠️ 两条守卫缺一不可（都踩过）：
//   1. **分享页不写** —— 往 `/s/<token>` 上挂 `?mode=` 会把收件人的地址顶掉；
//   2. **路由没解析完不写** —— vue-router 的初次解析是异步的，而 App 的 onMounted 跑在它之前，
//      那时 `currentRoute` 还停在 `/`，`replace({ query })` 就按 `/` 解析，
//      直接把 `/s/<token>` 覆盖成 `/?mode=…`。实测症状：**分享链接一打开就变成首页**，
//      地址栏里那条 `/s/<token>` 没了（OG 标题还是对的，因为那是服务端注入的 —— 极具迷惑性）。
function syncModeToAddress() {
    if (isShareRoute.value) {
        return;
    }
    // `matched` 为空 = 路由还没解析出任何记录（START_LOCATION），此时写地址必然写错。
    if (!router.currentRoute.value.matched.length) {
        return;
    }
    if (router.currentRoute.value.query.mode === app.uiMode) {
        return;
    }
    router.replace({ query: { ...router.currentRoute.value.query, mode: app.uiMode } });
}

watch(() => app.uiMode, syncModeToAddress);

// 地址里的模式可能是手打错的（`?mode=xxx`）。`resolveModeComponent` 会安全回落到标准模式、
// 不会白屏，但地址栏会一直挂着一个不存在的键骗人 —— 开局纠正一次。
onMounted(() => {
    if (!MODES.some((entry) => entry.key === app.uiMode)) {
        app.setUiMode('default');
    }
});

// 初次落地址放在**路由就绪之后**：这时才读得到 `meta.sharePage`，`currentRoute` 也才是真的。
router.isReady().then(syncModeToAddress);

const colorDialog = ref(false);
const pickColorDialog = ref(false);
const settingsDialog = ref(false);
// 分享记录：这个房间最近分享过什么、被打开了几次（服务端只有签发时才留记录）
const shareHistoryDialog = ref(false);
// 登录设备管理：此刻有谁登着、能不能把某一台踢下去（七天免密的反制手段）
const sessionManagerDialog = ref(false);
const sessionBusy = ref('');
const pageQrDialogVisible = ref(false);

// 退出登录。
//
// ⚠️ 一定要打到服务端 —— 只清本地是「眼不见为净」：服务端那份会话还活着，
// 拿到过令牌的人照样能用七天。服务端吊销之后令牌立刻失效，连已建立的 WebSocket 也会被掐断。
// all=true 是把**其它设备**一起踢下线（需要当前是平台级会话，否则服务端会拒绝）。
async function handleLogout({ all = false } = {}) {
    if (sessionBusy.value) {
        return;
    }
    sessionBusy.value = all ? 'all' : 'self';
    try {
        await ws.logout({ all });
        settingsDialog.value = false;
        if (all) {
            toast(t('logoutAllDone'), { color: 'success' });
        }
    } catch (err) {
        console.error('登出失败:', err);
        toast(t('logoutFailed'), { color: 'error' });
    } finally {
        sessionBusy.value = '';
    }
}
// 设置面板的页签：通用 / 个性化。个性化里是「每个界面模式一组显示开关」，
// 开关会长到几十项，所以必须单独占一页，不能平铺在通用页里。
const settingsTab = ref('general');

// 个性化面板只显示「当前模式真的能用」的开关（见 displayToggles.js 的 modes 声明）
const togglesInGroup = (groupKey) => togglesForMode(app.uiMode, groupKey);

// 分组总开关：全开才算「开」；点它要么全开、要么全关。
// 半开状态显示为关（而不是三态）—— 用户点一下就能得到「全开」，符合预期。
const groupAllOn = (groupKey) => {
    const list = togglesInGroup(groupKey);
    return list.length > 0 && list.every((toggle) => app.display[toggle.key]);
};
function setGroupAll(groupKey, value) {
    togglesInGroup(groupKey).forEach((toggle) => app.setDisplayToggle(toggle.key, value));
}
const pageQrMode = ref('page');
const currentPrimary = computed(() => isDark.value ? theme.themes.value.dark.colors.primary : theme.themes.value.light.colors.primary);
const clearAllDialog = ref(false);
const clipboardClearedMessageVisible = ref(false);
const roomSheet = ref(false);
const roomSearch = ref('');
const roomDockVisible = ref(false);
const roomDockSide = ref('right');
const availableRooms = ref([]);
const roomsLoading = ref(false);

provide('stickyAppActions', {
    openSettings: () => { settingsDialog.value = true; },
    openClearAll: () => { clearAllDialog.value = true; },
    openRoomDialog: () => { ws.roomInput = ws.room; ws.roomDialog = true; },
    toggleConnection: () => {
        if (!ws.websocket && !ws.websocketConnecting) {
            ws.retry = 0;
            ws.connect();
        }
    },
});

provide('pageToolbarActions', {
    openSettings: () => { settingsDialog.value = true; },
    openClearAll: () => { clearAllDialog.value = true; },
    openRoomDialog: () => { ws.roomInput = ws.room; ws.roomDialog = true; },
    toggleConnection: () => {
        if (!ws.websocket && !ws.websocketConnecting) {
            ws.retry = 0;
            ws.connect();
        }
    },
    openRoomBrowser: () => { openRoomBrowser(); },
    // 工具栏要拿它给那个按钮做「已开启」的状态样式和动态提示语
    roomBrowserVisible: computed(() => isDesktopRoomDockVisible.value || roomSheet.value),
    openPageQr: () => { pageQrDialogVisible.value = true; },
    goHome: () => { goHome(); },
    roomCount: computed(() => availableRooms.value.length),
    roomListEnabled: computed(() => Boolean(app.config?.server?.roomList)),
});

const languageOptions = [
    { code: 'zh', name: '简体中文' },
    { code: 'zh-TW', name: '繁體中文' },
    { code: 'en', name: 'English' },
    { code: 'ja', name: '日本語' },
];
const currentLocaleCode = computed(() => {
    const match = languageOptions.find(option => option.code === locale.value);
    return match ? match.code : 'zh';
});
const isDesktopRoomDockEnabled = computed(() => {
    return display.width.value > 1263 && app.config && app.config.server && app.config.server.roomList;
});
const latencyValue = computed(() => {
    if (ws.latency === null) {
        return '';
    }
    return `${Math.round(ws.latency)} ms`;
});
const latencyColor = computed(() => {
    if (ws.latency === null) {
        return 'grey';
    }
    if (ws.latency < 60) {
        return 'success';
    }
    if (ws.latency < 120) {
        return 'warning';
    }
    return 'error';
});
const latencyHexColor = computed(() => {
    if (ws.latency === null) {
        return '';
    }
    const colorName = latencyColor.value;
    const themeColors = theme.themes.value[isDark.value ? 'dark' : 'light'].colors;
    return themeColors[colorName] || colorName;
});
const isDesktopRoomDockVisible = computed(() => isDesktopRoomDockEnabled.value && roomDockVisible.value);
const filteredRooms = computed(() => {
    let rooms = availableRooms.value.slice();
    if (roomSearch.value) {
        rooms = rooms.filter(room =>
            (room.name || t('publicRoom')).toLowerCase().includes(roomSearch.value.toLowerCase())
        );
    }
    return rooms.sort((a, b) => {
        if (a.isFavorite !== b.isFavorite) {
            return b.isFavorite - a.isFavorite;
        }
        return 0;
    });
});
const currentRoomEntry = computed(() => {
    const currentRoomName = ws.room || '';
    const matching = filteredRooms.value.find(room => room.name === currentRoomName);
    if (matching) {
        return matching;
    }
    if (roomSearch.value) {
        return null;
    }
    return createOptimisticRoom(currentRoomName);
});
const favoriteRooms = computed(() => {
    const currentRoomName = ws.room || '';
    return filteredRooms.value.filter(room => room.isFavorite && room.name !== currentRoomName);
});
const activeRooms = computed(() => {
    const currentRoomName = ws.room || '';
    return filteredRooms.value.filter(room => !room.isFavorite && room.isActive && room.name !== currentRoomName);
});
const otherRooms = computed(() => {
    const currentRoomName = ws.room || '';
    return filteredRooms.value.filter(room => !room.isFavorite && !room.isActive && room.name !== currentRoomName);
});
const favoriteRoomCount = computed(() => availableRooms.value.filter(room => room.isFavorite).length);
const activeRoomCount = computed(() => availableRooms.value.filter(room => room.isActive).length);
const roomGroups = computed(() => [
    {
        key: 'favorites',
        title: t('favoriteRoomsLabel'),
        rooms: favoriteRooms.value,
    },
    {
        key: 'active',
        title: t('activeRoomsLabel'),
        rooms: activeRooms.value,
    },
    {
        key: 'other',
        title: t('otherRoomsLabel'),
        rooms: otherRooms.value,
    },
].filter(group => group.rooms.length > 0));

// 平台闸门：服务端要密码 且 这次请求没通过 → 锁住整个界面。
// ⚠️ 判定用的是 /server 下发的 globalAuth，**不是** `auth` ——
//    `auth` 里还含「某个房间有密码」，用它当闸门会把整个站点锁住，
//    连进一个开放房间都做不到（房间密码是另一套流程，见房间对话框）。
const platformLocked = computed(() => app.globalAuth === true && app.authorized !== true);

// 网络暂时打不通（/server 都没拉到时），不能把人锁在外面 —— 那时 authorized 还是初值 true，
// 闸门自然不显示；这里只负责把「填错密码」翻译成给人看的话。
const platformError = computed(() => (ws.authCodeError ? t(ws.authCodeError) : ''));

const platformPassword = ref('');
const platformSubmitting = ref(false);
async function submitPlatformPassword() {
    if (platformSubmitting.value || !platformPassword.value.trim()) {
        return;
    }
    platformSubmitting.value = true;
    try {
        const ok = await ws.submitPlatformPassword(platformPassword.value);
        if (ok) {
            platformPassword.value = '';
        }
    } finally {
        platformSubmitting.value = false;
    }
}

const darkModeOptions = [
    { value: 'time', title: t('switchByTime'), desc: t('switchByTimeDesc') },
    { value: 'prefer', title: t('switchBySystem'), desc: t('switchBySystemDesc') },
    { value: 'enable', title: t('keepEnabled'), desc: '' },
    { value: 'disable', title: t('keepDisabled'), desc: '' },
];

function submitRoomChange() {
    const roomName = ws.roomInput || '';
    ensureRoomPresent(roomName);
    ws.roomDialog = false;
    ws.navigateToRoom(roomName);
}
function createOptimisticRoom(roomName = ws.room || '') {
    if (roomName === undefined || roomName === null) {
        return null;
    }
    const normalizedRoomName = roomName || '';
    return {
        name: normalizedRoomName,
        isFavorite: getFavoriteRooms().includes(normalizedRoomName),
        // 占位条目：房间还没出现在 /rooms 的返回里时先按缓存画。
        // 语义与工具栏 chip 上那个锁一致 ——「要不要密码」，不是「roomAuth 里有没有这一项」
        // （见 PageToolbar.vue 的 isProtected）。缓存里没有这个房间时算作「不要密码」，
        // 之后由 syncAvailableRooms 用 /rooms 的 isProtected 覆盖成真实值。
        isProtected: Boolean(ws.roomProtectionCache?.[normalizedRoomName]),
        isActive: true,
        messageCount: 0,
        deviceCount: 0,
        lastActive: Math.floor(Date.now() / 1000),
    };
}
function ensureRoomPresent(roomName = ws.room || '') {
    const normalizedRoomName = roomName || '';
    if (availableRooms.value.some(room => room.name === normalizedRoomName)) {
        return;
    }
    availableRooms.value.unshift(createOptimisticRoom(normalizedRoomName));
}
function syncAvailableRooms(rooms) {
    const nextRooms = Array.isArray(rooms) ? rooms : [];
    const existingByName = new Map(availableRooms.value.map(room => [room.name, room]));
    const orderedRooms = nextRooms.map(roomData => {
        const existing = existingByName.get(roomData.name);
        if (existing) {
            Object.assign(existing, roomData);
            return existing;
        }
        return roomData;
    });
    const currentRoomName = ws.room || '';
    if (!orderedRooms.some(room => room.name === currentRoomName)) {
        orderedRooms.unshift(existingByName.get(currentRoomName) || createOptimisticRoom(currentRoomName));
    }
    availableRooms.value.splice(0, availableRooms.value.length, ...orderedRooms);
}
// 工具栏那个房间图标是**开关**，不是「打开」。
//
// 原来「打开」在工具栏、「关闭」在侧栏头部（一个 ✕）：同一个东西的两个动作分居两地，
// 而且 ✕ 在面板头部天然被读成「关掉这个弹窗」—— 侧栏是个持久偏好，不是弹窗，语义对不上。
// 现在两个方向都归这一个按钮，跟 VS Code / 访达的侧栏开关一致。
function openRoomBrowser() {
    if (isDesktopRoomDockEnabled.value) {
        roomDockVisible.value = !roomDockVisible.value;
        persistRoomBrowserPreferences();
        // 关掉时不用拉数据；打开时才需要
        if (roomDockVisible.value) {
            ensureRoomPresent();
            fetchRoomList();
        }
        return;
    }
    roomSheet.value = !roomSheet.value;
    if (roomSheet.value) {
        ensureRoomPresent();
        fetchRoomList();
    }
}
function toggleRoomDockSide() {
    roomDockSide.value = roomDockSide.value === 'right' ? 'left' : 'right';
    persistRoomBrowserPreferences();
}
function persistRoomBrowserPreferences() {
    localStorage.setItem('roomDockVisible', String(roomDockVisible.value));
    localStorage.setItem('roomDockSide', roomDockSide.value);
}
function restoreRoomBrowserPreferences() {
    const storedVisible = localStorage.getItem('roomDockVisible');
    const storedSide = localStorage.getItem('roomDockSide');
    roomDockVisible.value = storedVisible === null ? false : storedVisible === 'true';
    roomDockSide.value = storedSide === 'left' ? 'left' : 'right';
}
async function clearAll() {
    try {
        await axios.delete('revoke/all', {
            params: { room: ws.room },
        });
    } catch (error) {
        console.log(error);
        clipboardClearedMessageVisible.value = false;
        const errMsg = errorMessage(error);
        if (errMsg) {
            toast(t('clearClipboardFailedMsg', { msg: errMsg }));
        } else {
            toast(t('clearClipboardFailed'));
        }
    }
}
function changeLocale(localeValue) {
    if (locale.value !== localeValue) {
        locale.value = localeValue;
        localStorage.setItem('locale', localeValue);
    }
}
function goHome() {
    if (route.path !== '/' || Object.keys(route.query).length > 0) {
        router.push('/');
    }
}
const currentPageUrl = computed(() => {
    // history 路由：地址本身就是路径，不再往里塞 `#`（老写法拼出来的是 `/#/`，现在会 404）。
    // room 跟着走：扫码的人要落到同一个房间；分享页自己认路径里的 token，不受影响。
    const currentRoom = ws.room || '';
    const query = {};
    if (currentRoom) {
        query.room = currentRoom;
    }
    const resolved = router.resolve({ path: route.path, query });
    return new URL(resolved.href, window.location.origin).toString();
});
const latestContentUrl = computed(() => {
    const currentRoom = ws.room || '';
    const roomQuery = currentRoom ? `?room=${encodeURIComponent(currentRoom)}` : '';
    return buildAbsoluteRouteUrl(`content/latest${roomQuery}`);
});
const pageQrUrl = computed(() => pageQrMode.value === 'latest' ? latestContentUrl.value : currentPageUrl.value);
function buildAbsoluteRouteUrl(path) {
    const normalizedPath = String(path || '').replace(/^\/+/, '');
    const baseURL = axios.defaults.baseURL || '';
    if (baseURL) {
        return new URL(normalizedPath, `${baseURL.replace(/\/+$/, '')}/`).toString();
    }
    const prefix = app.config?.server?.prefix || '';
    return new URL(`${prefix}/${normalizedPath}`, `${window.location.origin}/`).toString();
}
function copyQrUrl() {
    const url = pageQrUrl.value;
    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(url)
            .then(() => toast(t('copySuccess')))
            .catch(() => toast(t('copyFailedGeneral')));
    } else {
        try {
            const textArea = document.createElement("textarea");
            textArea.value = url;
            textArea.style.position = "absolute";
            textArea.style.left = "-9999px";
            document.body.appendChild(textArea);
            textArea.select();
            document.execCommand('copy');
            document.body.removeChild(textArea);
            toast(t('copySuccess'));
        } catch {
            toast(t('copyFailedGeneral'));
        }
    }
}
async function fetchRooms() {
    const candidateTokens = typeof ws.getKnownAuthTokens === 'function' ? ws.getKnownAuthTokens() : [];
    const dedupedTokens = Array.from(new Set(candidateTokens.map(token => (token || '').trim()).filter(Boolean)));
    const response = await axios.get('rooms', {
        headers: dedupedTokens.length ? { 'X-Room-Auth-Tokens': JSON.stringify(dedupedTokens) } : undefined,
        __skipRoomAuthHandling: true,
    });
    return Array.isArray(response.data?.rooms) ? response.data.rooms : [];
}
async function fetchRoomList() {
    if (!app.config || !app.config.server || !app.config.server.roomList) {
        return;
    }
    if (roomsLoading.value) {
        return;
    }
    roomsLoading.value = true;
    try {
        const rooms = await fetchRooms();
        const favoriteRooms = getFavoriteRooms();
        syncAvailableRooms(rooms.map(room => ({ ...room, isFavorite: favoriteRooms.includes(room.name) })));
        ensureRoomPresent();
    } catch (error) {
        console.error('Failed to fetch room list:', error);
        toast(t('failedToLoadRooms'));
    } finally {
        roomsLoading.value = false;
    }
}
async function switchRoom(roomName) {
    roomSheet.value = false;
    await ws.navigateToRoom(roomName);
}

// ── 房间管理（新建 / 删除 / 清理）──────────────────────────────────────
//
// 权限模型**只在服务端**（见 lib/handler_rooms.go 的文件头）：
//   公共房间不可删、只能删自建房间、删要持有该房间密码或平台管理员凭据。
// 前端这里只负责收集输入与展示结果 —— 不要在这里复刻那套判断，
// 两处规则迟早会漂，而漂掉的那一侧一定是没有测试兜着的前端。
const roomCreateDialog = ref(false);
const roomCreateForm = ref({ name: '', password: '', confirm: '' });
const roomCreateError = ref('');
const roomCreateLoading = ref(false);

const roomDeleteTarget = ref(null);
const roomDeletePassword = ref('');
const roomDeleteError = ref('');
const roomDeleteLoading = ref(false);
const roomCleanupLoading = ref(false);
const roomCleanupDialog = ref(false);
const roomCleanupError = ref('');

// 房间管理密码：新建 / 删除 / 清理都要出示（服务端逐次校验，前端不缓存明文）。
const roomManagePassword = ref('');

// 「我算不算管理员」在前端只能粗略推断：手上有平台令牌就是。
// 服务端才是权威（每个管理接口都会重判一次），这里只用来决定
// 「清理无用房间」这个按钮要不要显示。
// isPlatformAdmin 这次登录的是不是「平台级」凭据（绿色通道：所有房间 + 房间管理）。
//
// ⚠️ 判定要用 hasGlobalSession()，**不是** getGlobalAuthToken()：
// 浏览器现在默认走 HttpOnly Cookie，前端手上根本没有令牌。用「有没有令牌」来判的话，
// Cookie 模式下的管理员能力会全部**静默失效** —— 界面上看不出任何异常，只是点了没反应。
const isPlatformAdmin = computed(() => !app.globalAuth || Boolean(ws.hasGlobalSession && ws.hasGlobalSession()));

// 「按名称进入」：侧栏动作行与工具栏那个门图标都走这里。
// 侧栏还开着时要顺手收起它 —— 否则弹窗会被侧栏压住（移动端是 bottom sheet，直接叠在一起）。
function openRoomEnter() {
    roomSheet.value = false;
    ws.roomInput = ws.room;
    ws.roomDialog = true;
}

function openRoomCreate() {
    roomCreateForm.value = { name: '', password: '', confirm: '' };
    roomManagePassword.value = '';
    roomCreateError.value = '';
    roomCreateDialog.value = true;
}

async function submitRoomCreate() {
    if (roomCreateLoading.value) {
        return;
    }
    const name = String(roomCreateForm.value.name || '').trim();
    const password = String(roomCreateForm.value.password || '');
    if (!name) {
        roomCreateError.value = t('roomNameRequired');
        return;
    }
    if (!password) {
        roomCreateError.value = t('roomPasswordRequired');
        return;
    }
    if (password !== String(roomCreateForm.value.confirm || '')) {
        roomCreateError.value = t('roomPasswordMismatch');
        return;
    }
    roomCreateLoading.value = true;
    roomCreateError.value = '';
    try {
        await axios.post('rooms', { name, password }, {
            headers: { 'X-Room-Manage-Password': String(roomManagePassword.value || '') },
        });
        roomCreateDialog.value = false;
        toast(t('roomCreateSuccess', { name }));
        await fetchRoomList();
    } catch (error) {
        roomCreateError.value = errorMessage(error) || t('roomCreateFailed');
    } finally {
        roomCreateLoading.value = false;
    }
}

function openRoomDelete(room) {
    roomDeleteTarget.value = room;
    roomDeletePassword.value = '';
    roomManagePassword.value = '';
    roomDeleteError.value = '';
}

async function submitRoomDelete() {
    const room = roomDeleteTarget.value;
    if (!room || roomDeleteLoading.value) {
        return;
    }
    roomDeleteLoading.value = true;
    roomDeleteError.value = '';
    try {
        // 凭据走 X-Room-Auth-Tokens（不是 Authorization）：axios 拦截器已经往
        // Authorization 里塞了当前的身份，那个不能被覆盖 —— 覆盖了管理员就变成
        // 「必须手打密码才能删」。两个候选都带上，服务端逐个试（extractAuthTokens）。
        const typed = String(roomDeletePassword.value || '').trim();
        const manage = String(roomManagePassword.value || '').trim();
        await axios.delete(`rooms/${encodeURIComponent(room.name)}`, {
            params: manage ? { managePassword: manage } : undefined,
            headers: typed ? { 'X-Room-Auth-Tokens': JSON.stringify([typed]) } : undefined,
        });
        roomDeleteTarget.value = null;
        roomDeletePassword.value = '';
        toast(t('roomDeleteSuccess', { name: displayRoomName(room.name) }));
        await fetchRoomList();
    } catch (error) {
        roomDeleteError.value = errorMessage(error) || t('roomDeleteFailed');
    } finally {
        roomDeleteLoading.value = false;
    }
}

function openRoomCleanup() {
    roomManagePassword.value = '';
    roomCleanupError.value = '';
    roomCleanupDialog.value = true;
}

async function submitRoomCleanup() {
    if (roomCleanupLoading.value) {
        return;
    }
    roomCleanupLoading.value = true;
    roomCleanupError.value = '';
    try {
        const response = await axios.post('rooms/cleanup', null, {
            headers: { 'X-Room-Manage-Password': String(roomManagePassword.value || '') },
        });
        roomCleanupDialog.value = false;
        const removed = Array.isArray(response.data?.removed) ? response.data.removed : [];
        toast(removed.length ? t('roomCleanupDone', { count: removed.length }) : t('roomCleanupNothing'));
        await fetchRoomList();
    } catch (error) {
        roomCleanupError.value = errorMessage(error) || t('roomCleanupFailed');
    } finally {
        roomCleanupLoading.value = false;
    }
}

function displayRoomName(name) {
    return name ? name : t('publicRoom');
}
function getFavoriteRooms() {
    try {
        return JSON.parse(localStorage.getItem('favoriteRooms') || '[]');
    } catch {
        return [];
    }
}
function toggleFavoriteRoom(roomName) {
    const favorites = getFavoriteRooms();
    const index = favorites.indexOf(roomName);
    if (index > -1) {
        favorites.splice(index, 1);
        toast(t('removedFromFavorites', { room: roomName || t('publicRoom') }));
    } else {
        favorites.push(roomName);
        toast(t('addedToFavorites', { room: roomName || t('publicRoom') }));
    }
    localStorage.setItem('favoriteRooms', JSON.stringify(favorites));
    const room = availableRooms.value.find(r => r.name === roomName);
    if (room) {
        room.isFavorite = !room.isFavorite;
    }
}
function getRoomDisplayName(room) {
    return room && room.name ? room.name : t('publicRoom');
}
function randomRoomName() {
    const names = ['reimu', 'marisa', 'rumia', 'cirno', 'meiling', 'patchouli', 'sakuya', 'remilia', 'flandre', 'letty', 'chen', 'lyrica', 'lunasa', 'merlin', 'youmu', 'yuyuko', 'ran', 'yukari', 'suika', 'mystia', 'keine', 'tewi', 'reisen', 'eirin', 'kaguya', 'mokou'];
    return names[Math.floor(Math.random() * names.length)] + '-' + Math.random().toString(16).substring(2, 6);
}

onMounted(() => {
    restoreRoomBrowserPreferences();

    const darkPrimary = localStorage.getItem('darkPrimary');
    const lightPrimary = localStorage.getItem('lightPrimary');
    if (darkPrimary) {
        theme.themes.value.dark.colors.primary = darkPrimary;
    }
    if (lightPrimary) {
        theme.themes.value.light.colors.primary = lightPrimary;
    }
});

watch(() => theme.themes.value.dark.colors.primary, (newVal) => {
    localStorage.setItem('darkPrimary', newVal);
});
watch(() => theme.themes.value.light.colors.primary, (newVal) => {
    localStorage.setItem('lightPrimary', newVal);
});
const useDark = computed(() => app.useDark);
// 统一的入口：**只走 applyDarkMode**，别再各写一遍 theme.change ——
// 落掉 documentElement 那一半，深色模式下页面底色就不会跟着变。
watch(useDark, () => {
    applyDarkMode();
});
watch(() => app.dark, (newVal) => {
    localStorage.setItem('darkmode', newVal);
    applyDarkMode();
    setupDarkModeTimers();
});
let darkModeTimer = null;
let darkMediaQuery = null;
function applyDarkMode() {
    const dark = Boolean(app.useDark);
    theme.change(dark ? 'dark' : 'light');
    // ⚠️ 主题类必须**同时**挂在 <html> 上。
    //   Vuetify 的 `.v-theme--dark` 只加在 #app 里层的 .v-application 上，
    //   而页面底色（html / body）在它**外面** —— 深色令牌只声明在 .v-theme--dark 里的话，
    //   html / body 永远解析到浅色值，表现就是「面板是深色、页面底色还是浅色」。
    //   theme.css 里深色令牌写成 `html.cc-dark, .v-theme--dark`，两边都认。
    document.documentElement.classList.toggle('cc-dark', dark);
}
function setupDarkModeTimers() {
    if (darkModeTimer) {
        clearInterval(darkModeTimer);
        darkModeTimer = null;
    }
    if (darkMediaQuery) {
        darkMediaQuery.removeEventListener('change', applyDarkMode);
        darkMediaQuery = null;
    }
    if (app.dark === 'time') {
        darkModeTimer = setInterval(applyDarkMode, 1000);
    } else if (app.dark === 'prefer') {
        darkMediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
        darkMediaQuery.addEventListener('change', applyDarkMode);
    }
}
onMounted(() => {
    applyDarkMode();
    setupDarkModeTimers();
});
onBeforeUnmount(() => {
    if (darkModeTimer) {
        clearInterval(darkModeTimer);
    }
    if (darkMediaQuery) {
        darkMediaQuery.removeEventListener('change', applyDarkMode);
    }
});
watch(() => Boolean(ws.websocket), (connected) => {
    if (connected && app.config && app.config.server && app.config.server.roomList) {
        fetchRoomList();
    }
});
watch(isDesktopRoomDockVisible, (newVal) => {
    if (newVal) {
        ensureRoomPresent();
        fetchRoomList();
    }
}, { immediate: true });
watch(() => route.fullPath, () => {
    clipboardClearedMessageVisible.value = false;
    const routeRoom = ws.normalizeRoomName(route.query.room || '');
    if (ws.normalizeRoomName(ws.room) !== routeRoom) {
        ws.switchRoom(routeRoom);
    }
    if (app.config && app.config.server && app.config.server.roomList) {
        ensureRoomPresent(routeRoom);
    }
});
</script>

<template>
    <v-app class="app-shell" :class="{ 'app-shell--dark': isDark }">
        <!-- 分享页是给收件人看的独立页面：不能带主应用的外壳（工具栏、房间侧栏、设置面板）。
             包一层 v-if 而不是给每一块加条件 —— 外壳的部件太多，漏一个就漏出去了。 -->
        <template v-if="isShareRoute">
            <router-view />
        </template>

        <!-- 平台闸门：没通过密码之前**不渲染主界面**。
             只用一层遮罩盖住是不够的 —— DOM 里仍然能读到内容、也仍会去连 WebSocket。 -->
        <div v-if="platformLocked" class="auth-gate">
            <div class="auth-gate__card">
                <div class="auth-gate__icon">
                    <v-icon size="34">mdi-lock-outline</v-icon>
                </div>
                <h1 class="auth-gate__title">{{ t('platformLockedTitle') }}</h1>
                <p class="auth-gate__desc">{{ t('platformLockedDesc') }}</p>
                <p class="auth-gate__hint">{{ t('platformLockedSessionHint') }}</p>
                <v-text-field
                    v-model="platformPassword"
                    :label="t('password')"
                    type="password"
                    variant="outlined"
                    autocomplete="current-password"
                    class="auth-gate__field"
                    :error-messages="platformError ? [platformError] : []"
                    :loading="platformSubmitting"
                    :disabled="platformSubmitting"
                    autofocus
                    @update:model-value="ws.authCodeError = ''"
                    @keyup.enter="submitPlatformPassword"
                ></v-text-field>
                <v-btn
                    block
                    rounded="pill"
                    size="large"
                    color="primary"
                    variant="flat"
                    class="auth-gate__submit"
                    :loading="platformSubmitting"
                    :disabled="!platformPassword.trim()"
                    @click="submitPlatformPassword"
                >
                    <v-icon start>{{ mdiLoginVariant }}</v-icon>
                    {{ t('platformUnlock') }}
                </v-btn>
            </div>
        </div>

        <template v-else>
        <v-alert
            v-model="clipboardClearedMessageVisible"
            type="error"
            dismissible
            dense
            class="ma-0 text-center"
            style="position: sticky; top: 0; z-index: 5;"
        >
            {{ t('clipboardClearedRefresh') }}
        </v-alert>

        <v-main class="app-shell__main">
            <div
                class="app-shell__workspace"
                :class="{
                    'app-shell__workspace--dock-right': isDesktopRoomDockEnabled && roomDockSide === 'right',
                    'app-shell__workspace--dock-left': isDesktopRoomDockEnabled && roomDockSide === 'left'
                }"
            >
                <div class="app-shell__content">
                    <router-view v-slot="{ Component }">
                        <keep-alive v-if="route.meta.keepAlive"><component :is="Component" /></keep-alive>
                        <component v-else :is="Component" />
                    </router-view>
                </div>

                <aside
                    v-if="isDesktopRoomDockVisible"
                    class="room-browser room-browser--dock"
                    :class="[
                        { 'room-browser--dark': isDark },
                        roomDockSide === 'left' ? 'room-browser--dock-left' : 'room-browser--dock-right'
                    ]"
                >
                    <RoomList
                        v-model:search="roomSearch"
                        :groups="roomGroups"
                        :current-room="currentRoomEntry"
                        :current-room-name="getRoomDisplayName({ name: ws.room })"
                        :current-room-label="t('currentRoomLabel')"
                        :title="t('roomList')"
                        :count="availableRooms.length"
                        :favorite-count="favoriteRoomCount"
                        :active-count="activeRoomCount"
                        :loading="roomsLoading"
                        :has-rooms="filteredRooms.length > 0"
                        :can-cleanup="isPlatformAdmin"
                        @select="switchRoom"
                        @favorite="toggleFavoriteRoom"
                        @create="openRoomCreate"
                        @delete="openRoomDelete"
                        @cleanup="openRoomCleanup"
                        @enter="openRoomEnter"
                        variant="dock"
                        :dock-side="roomDockSide"
                    >
                        <template #actions>
                            <!-- 这里只剩「停靠方向」一个按钮。
                                 原来的 ✕（隐藏侧栏）删了：开关收到工具栏那个房间图标上，
                                 同一个东西的两个方向不该分居两地；而且 ✕ 在面板头部天然被读成
                                 「关掉这个弹窗」，跟「侧栏是个持久偏好」对不上。
                                 图标画的是「会停到哪一侧」，比 chevron 明确 —— 原来那个 ‹ 太像收起。 -->
                            <v-tooltip left>
                                <template v-slot:activator="{ props }">
                                    <v-btn icon density="comfortable" variant="text" class="room-browser__action" v-bind="props" @click="toggleRoomDockSide()">
                                        <v-icon size="20">{{ roomDockSide === 'right' ? mdiDockLeft : mdiDockRight }}</v-icon>
                                    </v-btn>
                                </template>
                                <span>{{ roomDockSide === 'right' ? t('dockLeft') : t('dockRight') }}</span>
                            </v-tooltip>
                        </template>
                    </RoomList>
                </aside>
            </div>
        </v-main>

        <v-dialog v-model="settingsDialog" max-width="480">
            <v-card class="cc-settings">
                <v-card-title class="cc-settings__header">
                    <div class="flex-grow-1 cc-settings__header-wrap">
                        <div class="cc-settings__title">{{ t('settings') }}</div>
                        <div class="cc-settings__version">
                            <span class="mr-1">{{ t('cloudClipboard') }}</span>
                            <span v-if="app.config && app.config.version">{{ app.config.version }}</span>
                            <!-- 前端构建指纹：和后端 version 是两回事。排查「线上是不是旧版」时看这个 -->
                            <span v-if="buildId" class="ml-1 text-medium-emphasis">· web {{ buildId }}</span>
                        </div>
                    </div>
                    <v-btn icon variant="text" @click="settingsDialog = false">
                        <v-icon>{{ mdiClose }}</v-icon>
                    </v-btn>
                </v-card-title>
                <v-divider></v-divider>
                <v-tabs v-model="settingsTab" density="comfortable" color="primary" class="cc-settings__tabs">
                    <v-tab value="general">{{ t('settingsGeneral') }}</v-tab>
                    <v-tab value="personalization">{{ t('personalization') }}</v-tab>
                    <v-tab value="security">{{ t('settingsSecurity') }}</v-tab>
                </v-tabs>
                <v-tabs-window v-model="settingsTab">
                    <v-tabs-window-item value="general">
                        <v-card-text class="cc-settings__body" style="max-height: 62vh; overflow-y: auto;">
                            <div class="cc-settings__group">
                            <v-list-subheader class="cc-settings__subheader">{{ t('appearance') }}</v-list-subheader>
                            <v-list class="cc-settings__list" density="comfortable">
                                <v-list-item class="cc-settings__item">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiBrightness4 }}</v-icon>
                                    </template>
                                    <v-list-item-title>{{ t('darkMode') }}</v-list-item-title>
                                    <template v-slot:append>
                                        <v-select
                                            :model-value="app.dark"
                                            :items="darkModeOptions"
                                            item-title="title"
                                            item-value="value"
                                            hide-details
                                            density="compact"
                                            class="cc-settings-select"
                                            @update:model-value="v => app.dark = v"
                                        ></v-select>
                                    </template>
                                </v-list-item>
                                <v-list-item class="cc-settings__item">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiPalette }}</v-icon>
                                    </template>
                                    <v-list-item-title>{{ t('changeThemeColor') }}</v-list-item-title>
                                    <template v-slot:append>
                                        <div class="cc-settings__theme-actions">
                                            <v-btn
                                                variant="tonal"
                                                size="small"
                                                class="cc-settings__theme-btn"
                                                @click="pickColorDialog = true"
                                            >
                                                <span class="cc-settings__swatch" :style="{ background: currentPrimary }"></span>
                                                {{ t('colorPicker') }}
                                            </v-btn>
                                            <v-btn
                                                variant="tonal"
                                                size="small"
                                                class="cc-settings__theme-btn"
                                                @click="colorDialog = true"
                                            >
                                                <v-icon start size="16">{{ mdiPaletteSwatch }}</v-icon>
                                                {{ t('traditionalColors') }}
                                            </v-btn>
                                        </div>
                                    </template>
                                </v-list-item>
                            </v-list>
                        </div>
                            <div class="cc-settings__group">
                            <v-list-subheader class="cc-settings__subheader">{{ t('language') }}</v-list-subheader>
                            <v-list class="cc-settings__list" density="comfortable">
                                <v-list-item class="cc-settings__item">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiTranslate }}</v-icon>
                                    </template>
                                    <v-list-item-title>{{ t('language') }}</v-list-item-title>
                                    <template v-slot:append>
                                        <v-select
                                            :model-value="currentLocaleCode"
                                            :items="languageOptions"
                                            item-title="name"
                                            item-value="code"
                                            hide-details
                                            density="compact"
                                            class="cc-settings-select"
                                            @update:model-value="changeLocale"
                                        ></v-select>
                                    </template>
                                </v-list-item>
                            </v-list>
                        </div>
                            <div class="cc-settings__group">
                            <v-list-subheader class="cc-settings__subheader">{{ t('shareSettings') }}</v-list-subheader>
                            <v-list class="cc-settings__list" density="comfortable">
                                <v-list-item class="cc-settings__item" @click="shareHistoryDialog = true">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiShareVariant }}</v-icon>
                                    </template>
                                    <v-list-item-title>{{ t('shareHistory') }}</v-list-item-title>
                                    <v-list-item-subtitle>{{ t('shareHistoryEntryHint') }}</v-list-item-subtitle>
                                    <template v-slot:append>
                                        <v-icon size="18">{{ mdiChevronRight }}</v-icon>
                                    </template>
                                </v-list-item>
                            </v-list>
                        </div>
                            <div class="cc-settings__group">
                            <v-list-subheader class="cc-settings__subheader">{{ t('about') }}</v-list-subheader>
                            <v-list class="cc-settings__list" density="comfortable">
                                <v-list-item class="cc-settings__item">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiGithub }}</v-icon>
                                    </template>
                                    <v-list-item-title>
                                        <a href="https://github.com/Jonnyan404/cloud-clipboard-go" target="_blank" rel="noopener" class="cc-settings__link">
                                            {{ t('github') }}
                                            <v-icon size="16" class="cc-settings__external">{{ mdiOpenInNew }}</v-icon>
                                        </a>
                                    </v-list-item-title>
                                </v-list-item>
                            </v-list>
                        </div>
                        </v-card-text>
                    </v-tabs-window-item>
                    <v-tabs-window-item value="personalization">
                        <v-card-text class="cc-settings__body" style="max-height: 62vh; overflow-y: auto;">
                            <div class="text-caption text-medium-emphasis mb-3">{{ t('personalizationHint') }}</div>

                                                    <!-- 布局 / 展示方式：**只对标准模式有意义**（另外五个模式各有一套自己的排布）。
                                                         所以只在 uiMode === 'default' 时出现 —— 按 displayToggles 的同一原则：
                                                         显示一个拨了不生效的开关，比不显示它更糟。 -->
                                                    <template v-if="app.uiMode === 'default'">
                                                        <v-list-subheader class="cc-settings__subheader">{{ t('workspaceLayout') }}</v-list-subheader>
                                                        <v-btn-toggle
                                                            :model-value="app.homeLayout"
                                                            @update:model-value="(value) => value && app.setHomeLayout(value)"
                                                            mandatory
                                                            density="comfortable"
                                                            variant="outlined"
                                                            divided
                                                            class="flex-wrap mb-3 cc-workspace-toggle"
                                                        >
                                                            <v-btn
                                                                v-for="option in LAYOUT_OPTIONS"
                                                                :key="option.key"
                                                                :value="option.key"
                                                                size="small"
                                                                class="text-none"
                                                            >
                                                                <v-icon start size="18">{{ option.icon }}</v-icon>{{ t(option.labelKey) }}
                                                            </v-btn>
                                                        </v-btn-toggle>

                                                        <v-list-subheader class="cc-settings__subheader">{{ t('workspaceViewMode') }}</v-list-subheader>
                                                        <v-btn-toggle
                                                            :model-value="app.historyView"
                                                            @update:model-value="(value) => value && app.setHistoryView(value)"
                                                            mandatory
                                                            density="comfortable"
                                                            variant="outlined"
                                                            divided
                                                            class="flex-wrap mb-3 cc-workspace-toggle"
                                                        >
                                                            <v-btn
                                                                v-for="option in VIEW_OPTIONS"
                                                                :key="option.key"
                                                                :value="option.key"
                                                                size="small"
                                                                class="text-none"
                                                            >
                                                                <v-icon start size="18">{{ option.icon }}</v-icon>{{ t(option.labelKey) }}
                                                            </v-btn>
                                                        </v-btn-toggle>
                                                        <div class="text-caption text-medium-emphasis mb-3">{{ t('workspaceHint') }}</div>
                                                    </template>
                                                    <!-- 界面模式。巨型与终端已删除，「即将下架」的弱化显示也随之取消 ——
                                                         剩下的模式一律常驻，不再有主次之分。 -->
                                                    <v-btn-toggle
                                                        :model-value="app.uiMode"
                                                        @update:model-value="app.setUiMode"
                                                        mandatory
                                                        density="comfortable"
                                                        class="flex-wrap mb-2"
                                                        style="height: auto;"
                                                    >
                                                        <v-btn
                                                            v-for="mode in MODES"
                                                            :key="mode.key"
                                                            :value="mode.key"
                                                            size="small"
                                                            class="text-none"
                                                        >
                                                            <v-icon start size="18">{{ mode.icon }}</v-icon>{{ t(mode.labelKey) }}
                                                        </v-btn>
                                                    </v-btn-toggle>

                                                    <template v-for="group in DISPLAY_GROUPS" :key="group.key">
                                                        <template v-if="togglesInGroup(group.key).length">
                                                            <div class="cc-settings__group-head">
                                                                <v-list-subheader class="cc-settings__subheader">{{ t(group.labelKey) }}</v-list-subheader>
                                                                <v-switch
                                                                    :model-value="groupAllOn(group.key)"
                                                                    @update:model-value="value => setGroupAll(group.key, value)"
                                                                    color="primary"
                                                                    hide-details
                                                                    inset
                                                                    density="compact"
                                                                    :title="t('toggleGroupAll')"
                                                                ></v-switch>
                                                            </div>
                                                            <v-list class="cc-settings__list cc-settings__toggles" density="comfortable">
                                                                <template v-for="toggle in togglesInGroup(group.key)" :key="toggle.key">
                                                                <v-list-item class="cc-settings__item">
                                                                    <template v-slot:prepend>
                                                                        <v-icon color="primary">{{ toggle.icon }}</v-icon>
                                                                    </template>
                                                                    <v-list-item-title>{{ t(toggle.labelKey) }}</v-list-item-title>
                                                                    <template v-slot:append>
                                                                        <v-switch
                                                                            :model-value="app.display[toggle.key]"
                                                                            @update:model-value="value => app.setDisplayToggle(toggle.key, value)"
                                                                            color="primary"
                                                                            hide-details
                                                                            inset
                                                                        ></v-switch>
                                                                    </template>
                                                                </v-list-item>

                                                                <!-- 紧挨着「分享时弹出设置框」这个开关：关掉它之后，这三项就是那次弹框的
                                                                     全部内容。放到面板末尾等于让用户去别处找。
                                                                     ⚠️ 这三个输入框必须 `variant="outlined"`：`solo` + `flat` 的底色是
                                                                     `rgb(var(--v-theme-surface))`，跟设置卡片本身一模一样，又没有阴影 ——
                                                                     白底白框，看着就像「没有输入框」。 -->
                                                                <template v-if="toggle.key === 'shareDialog' && !app.display.shareDialog">
                                                                    <v-list-item class="cc-settings__item">
                                                                        <v-list-item-title>{{ t('shareDefaultTtl') }}</v-list-item-title>
                                                                        <template v-slot:append>
                                                                            <v-text-field
                                                                                :model-value="app.shareDefaults.ttlMinutes"
                                                                                type="number"
                                                                                density="compact"
                                                                                variant="outlined"
                                                                                hide-details
                                                                                class="cc-settings__num"
                                                                                @update:model-value="v => app.setShareDefaults({ ttlMinutes: Number(v) })"
                                                                            ></v-text-field>
                                                                        </template>
                                                                    </v-list-item>
                                                                    <v-list-item class="cc-settings__item">
                                                                        <v-list-item-title>{{ t('shareDefaultMaxUses') }}</v-list-item-title>
                                                                        <template v-slot:append>
                                                                            <v-text-field
                                                                                :model-value="app.shareDefaults.maxUses"
                                                                                type="number"
                                                                                density="compact"
                                                                                variant="outlined"
                                                                                hide-details
                                                                                class="cc-settings__num"
                                                                                @update:model-value="v => app.setShareDefaults({ maxUses: Number(v) })"
                                                                            ></v-text-field>
                                                                        </template>
                                                                    </v-list-item>
                                                                    <v-list-item class="cc-settings__item">
                                                                        <v-list-item-title>{{ t('shareDefaultPassword') }}</v-list-item-title>
                                                                        <template v-slot:append>
                                                                            <v-text-field
                                                                                :model-value="app.shareDefaults.password"
                                                                                type="password"
                                                                                autocomplete="new-password"
                                                                                density="compact"
                                                                                variant="outlined"
                                                                                hide-details
                                                                                class="cc-settings__text"
                                                                                @update:model-value="v => app.setShareDefaults({ password: String(v || '') })"
                                                                            ></v-text-field>
                                                                        </template>
                                                                    </v-list-item>
                                                                </template>
                                                            </template>
                                                            </v-list>
                                                        </template>
                                                    </template>

                        </v-card-text>
                    </v-tabs-window-item>
                    <v-tabs-window-item value="security">
                        <v-card-text class="cc-settings__body" style="max-height: 62vh; overflow-y: auto;">
                            <div class="text-caption text-medium-emphasis mb-3">{{ t('settingsSecurityHint') }}</div>

                            <v-list class="cc-settings__list" density="comfortable">
                                <v-list-item class="cc-settings__item" @click="sessionManagerDialog = true">
                                    <template v-slot:prepend>
                                        <v-icon color="primary">{{ mdiDevices }}</v-icon>
                                    </template>
                                    <v-list-item-title>{{ t('sessionManagerEntry') }}</v-list-item-title>
                                    <v-list-item-subtitle>{{ t('sessionManagerEntryHint') }}</v-list-item-subtitle>
                                    <template v-slot:append>
                                        <v-icon size="18">{{ mdiChevronRight }}</v-icon>
                                    </template>
                                </v-list-item>
                            </v-list>

                            <div class="cc-settings__security-actions mt-3">
                                <v-btn
                                    variant="tonal"
                                    color="error"
                                    :prepend-icon="mdiLogout"
                                    :loading="sessionBusy === 'self'"
                                    @click="handleLogout({ all: false })"
                                >
                                    {{ t('logoutDevice') }}
                                </v-btn>
                                <v-btn
                                    v-if="isPlatformAdmin"
                                    variant="text"
                                    color="error"
                                    :prepend-icon="mdiLogoutVariant"
                                    :loading="sessionBusy === 'all'"
                                    @click="handleLogout({ all: true })"
                                >
                                    {{ t('logoutAllDevices') }}
                                </v-btn>
                            </div>
                            <div class="text-caption text-medium-emphasis mt-2">{{ t('logoutHint') }}</div>
                        </v-card-text>
                    </v-tabs-window-item>
                </v-tabs-window>
            </v-card>
        </v-dialog>


        <!-- 分享记录（从设置 → 分享记录 打开）。与设置弹窗并列，都是顶层 teleport 弹窗。 -->
        <share-history-dialog v-model="shareHistoryDialog"></share-history-dialog>

        <!-- 登录设备管理（设置 → 安全）。令牌能用七天，就得配一把随时能收回来的东西。 -->
        <session-manager-dialog v-model="sessionManagerDialog"></session-manager-dialog>

        <traditional-color-dialog v-model="colorDialog"></traditional-color-dialog>

        <v-dialog v-model="pickColorDialog" max-width="340">
            <v-card>
                <v-card-title>{{ t('selectThemeColor') }}</v-card-title>
                <v-card-text class="cc-picker-dialog__body">
                    <v-color-picker v-if="isDark" v-model="theme.themes.value.dark.colors.primary" show-swatches hide-inputs></v-color-picker>
                    <v-color-picker v-else v-model="theme.themes.value.light.colors.primary" show-swatches hide-inputs></v-color-picker>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn color="primary" variant="text" @click="pickColorDialog = false">{{ t('ok') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="ws.authCodeDialog" persistent max-width="360">
            <v-card>
                <v-card-title class="text-h5">{{ t('authRequired') }}</v-card-title>
                <v-card-text>
                    <p>{{ t('authPrompt') }}</p>
                    <p class="text-caption text-medium-emphasis mb-3">
                        {{ t('room') }}: {{ getRoomDisplayName({ name: ws.authPendingRoom || ws.room }) }}
                    </p>
                    <v-text-field
                        v-model="ws.inputPassword"
                        :label="t('password')"
                        variant="outlined"
                        bg-color="transparent"
                        class="cc-dialog-field"
                        :loading="ws.authDialogLoading"
                        :disabled="ws.authDialogLoading"
                        :error-messages="ws.authCodeError ? [t(ws.authCodeError)] : []"
                        hide-details="auto"
                        @update:model-value="ws.authCodeError = ''"
                        @keyup.enter="ws.submitAuthCodeForPendingRoom()"
                        autofocus
                    ></v-text-field>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn
                        color="primary-darken-1"
                        variant="text"
                        :loading="ws.authDialogLoading"
                        @click="ws.submitAuthCodeForPendingRoom()"
                    >{{ t('submit') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 新建房间。密码是必填的 —— 需求就是「房间准入都要有密码」，
             所以这里不给「先建后补」的口子。 -->
        <v-dialog v-model="roomCreateDialog" max-width="380">
            <v-card>
                <v-card-title class="text-h6 d-flex align-center">
                    <v-icon class="mr-2">mdi-plus</v-icon>
                    {{ t('createRoom') }}
                    <v-spacer></v-spacer>
                    <v-btn icon density="comfortable" variant="text" size="small" @click="roomCreateDialog = false">
                        <v-icon>{{ mdiClose }}</v-icon>
                    </v-btn>
                </v-card-title>
                <v-divider></v-divider>
                <v-card-text>
                    <p class="text-body-2 text-medium-emphasis mb-3">{{ t('createRoomHint') }}</p>
                    <v-text-field
                        v-model="roomCreateForm.name"
                        :label="t('roomName')"
                        variant="outlined"
                        density="comfortable"
                        :counter="32"
                        maxlength="32"
                        autofocus
                        @update:model-value="roomCreateError = ''"
                    ></v-text-field>
                    <v-text-field
                        v-model="roomCreateForm.password"
                        :label="t('roomPassword')"
                        type="password"
                        autocomplete="new-password"
                        variant="outlined"
                        density="comfortable"
                        @update:model-value="roomCreateError = ''"
                    ></v-text-field>
                    <v-text-field
                        v-model="roomCreateForm.confirm"
                        :label="t('roomPasswordConfirm')"
                        type="password"
                        autocomplete="new-password"
                        variant="outlined"
                        density="comfortable"
                        @update:model-value="roomCreateError = ''"
                    ></v-text-field>
                    <v-text-field
                        v-model="roomManagePassword"
                        :label="t('roomManagePasswordLabel')"
                        type="password"
                        autocomplete="off"
                        variant="outlined"
                        density="comfortable"
                        hide-details="auto"
                        :hint="t('roomManagePasswordHint')"
                        persistent-hint
                        :error-messages="roomCreateError ? [roomCreateError] : []"
                        @keyup.enter="submitRoomCreate"
                        @update:model-value="roomCreateError = ''"
                    ></v-text-field>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn variant="text" @click="roomCreateDialog = false">{{ t('cancel') }}</v-btn>
                    <v-btn
                        rounded="pill"
                        color="primary"
                        variant="flat"
                        :loading="roomCreateLoading"
                        @click="submitRoomCreate"
                    >{{ t('create') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <!-- 删除房间。密码与「管理员身份」二者其一即可 ——
             管理员留空直接删，普通用户填该房间的密码。 -->
        <!-- 清理无用房间：需要房间管理密码。 -->
        <v-dialog v-model="roomCleanupDialog" max-width="380">
            <v-card>
                <v-card-title class="text-h6 d-flex align-center">
                    <v-icon class="mr-2">mdi-broom</v-icon>
                    {{ t('cleanupRooms') }}
                    <v-spacer></v-spacer>
                    <v-btn icon density="comfortable" variant="text" size="small" @click="roomCleanupDialog = false">
                        <v-icon>{{ mdiClose }}</v-icon>
                    </v-btn>
                </v-card-title>
                <v-divider></v-divider>
                <v-card-text>
                    <p class="text-body-2 text-medium-emphasis mb-3">{{ t('cleanupRoomsHint') }}</p>
                    <v-text-field
                        v-model="roomManagePassword"
                        :label="t('roomManagePasswordLabel')"
                        type="password"
                        autocomplete="off"
                        variant="outlined"
                        density="comfortable"
                        hide-details="auto"
                        :error-messages="roomCleanupError ? [roomCleanupError] : []"
                        @keyup.enter="submitRoomCleanup"
                        @update:model-value="roomCleanupError = ''"
                    ></v-text-field>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn variant="text" @click="roomCleanupDialog = false">{{ t('cancel') }}</v-btn>
                    <v-btn
                        rounded="pill"
                        color="primary"
                        variant="flat"
                        :loading="roomCleanupLoading"
                        @click="submitRoomCleanup"
                    >{{ t('cleanupRooms') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog :model-value="Boolean(roomDeleteTarget)" max-width="380" @update:model-value="v => { if (!v) roomDeleteTarget = null; }">
            <v-card>
                <v-card-title class="text-h6 d-flex align-center">
                    <v-icon class="mr-2" color="error">mdi-delete-outline</v-icon>
                    {{ t('deleteRoom') }}
                    <v-spacer></v-spacer>
                    <v-btn icon density="comfortable" variant="text" size="small" @click="roomDeleteTarget = null">
                        <v-icon>{{ mdiClose }}</v-icon>
                    </v-btn>
                </v-card-title>
                <v-divider></v-divider>
                <v-card-text>
                    <p class="text-body-2 mb-2">
                        {{ t('deleteRoomConfirm', { name: displayRoomName(roomDeleteTarget && roomDeleteTarget.name) }) }}
                    </p>
                    <p class="text-caption text-medium-emphasis mb-3">{{ t('deleteRoomHint') }}</p>
                    <v-text-field
                        v-model="roomDeletePassword"
                        :label="t('roomPasswordAdminOptional')"
                        type="password"
                        autocomplete="off"
                        variant="outlined"
                        density="comfortable"
                        hide-details="auto"
                        @update:model-value="roomDeleteError = ''"
                    ></v-text-field>
                    <v-text-field
                        v-model="roomManagePassword"
                        :label="t('roomManagePasswordLabel')"
                        type="password"
                        autocomplete="off"
                        variant="outlined"
                        density="comfortable"
                        class="mt-2"
                        hide-details="auto"
                        :hint="t('roomManagePasswordOptional')"
                        persistent-hint
                        :error-messages="roomDeleteError ? [roomDeleteError] : []"
                        @keyup.enter="submitRoomDelete"
                        @update:model-value="roomDeleteError = ''"
                    ></v-text-field>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn variant="text" @click="roomDeleteTarget = null">{{ t('cancel') }}</v-btn>
                    <v-btn
                        rounded="pill"
                        color="error"
                        variant="flat"
                        :loading="roomDeleteLoading"
                        @click="submitRoomDelete"
                    >{{ t('delete') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="ws.roomDialog" persistent max-width="360">
            <v-card>
                <v-card-title class="text-h5">{{ t('clipboardRoom') }}</v-card-title>
                <v-card-text>
                    <p>{{ t('roomPrompt1') }}</p>
                    <p>{{ t('roomPrompt2') }}</p>
                    <v-text-field
                        v-model="ws.roomInput"
                        :label="t('roomName')"
                        variant="outlined"
                        bg-color="transparent"
                        class="cc-dialog-field"
                        :append-icon="mdiDiceMultiple"
                        @click:append="ws.roomInput = randomRoomName()"
                        @keyup.enter="submitRoomChange()"
                        autofocus
                    ></v-text-field>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn
                        color="primary-darken-1"
                        variant="text"
                        @click="ws.roomDialog = false"
                    >{{ t('cancel') }}</v-btn>
                    <v-btn
                        color="primary-darken-1"
                        variant="text"
                        @click="submitRoomChange()"
                    >{{ t('enterRoom') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="clearAllDialog" max-width="360">
            <v-card>
                <v-card-title class="text-h5">{{ t('clearClipboardConfirmTitle') }}</v-card-title>
                <v-card-text>
                    <p>{{ t('clearClipboardConfirmText') }}</p>
                </v-card-text>
                <v-card-actions>
                    <v-spacer></v-spacer>
                    <v-btn
                        color="primary-darken-1"
                        variant="text"
                        @click="clearAllDialog = false"
                    >{{ t('cancel') }}</v-btn>
                    <v-btn
                        color="primary-darken-1"
                        variant="text"
                        @click="clearAllDialog = false; clearAll(); clipboardClearedMessageVisible = true;"
                    >{{ t('ok') }}</v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>

        <v-dialog v-model="pageQrDialogVisible" max-width="250">
            <v-card>
                <v-card-title class="text-h5 d-flex align-center">
                    {{ t('scanToAccess') }}
                    <v-spacer></v-spacer>
                    <v-btn icon variant="text" @click="pageQrDialogVisible = false">
                        <v-icon>{{ mdiClose }}</v-icon>
                    </v-btn>
                </v-card-title>
                <v-card-text class="text-center pa-4 pt-0">
                    <v-btn-toggle v-model="pageQrMode" mandatory density="compact" class="mb-3">
                        <v-btn size="small" value="page">{{ t('currentShare') }}</v-btn>
                        <v-btn size="small" value="latest">{{ t('latestShare') }}</v-btn>
                    </v-btn-toggle>
                    <div>
                        <qrcode-vue :value="pageQrUrl" :size="200" level="H" />
                    </div>
                    <div
                        class="text-caption mt-2 d-flex align-center justify-center"
                        style="word-break: break-all; cursor: pointer;"
                        :title="t('copyLink')"
                        @click="copyQrUrl"
                    >
                        <span class="flex-grow-1">{{ pageQrUrl }}</span>
                        <v-icon size="small" class="ml-1">{{ mdiContentPaste }}</v-icon>
                    </div>
                </v-card-text>
            </v-card>
        </v-dialog>

        <v-bottom-sheet v-model="roomSheet" scrollable max-width="820">
            <v-card class="room-browser" :class="{ 'room-browser--dark': isDark }">
                <RoomList
                    v-model:search="roomSearch"
                    :groups="roomGroups"
                    :current-room="currentRoomEntry"
                    :current-room-name="getRoomDisplayName({ name: ws.room })"
                    :current-room-label="t('currentRoomLabel')"
                    :title="t('roomList')"
                    :count="availableRooms.length"
                    :favorite-count="favoriteRoomCount"
                    :active-count="activeRoomCount"
                    :loading="roomsLoading"
                    :has-rooms="filteredRooms.length > 0"
                    :can-cleanup="isPlatformAdmin"
                    @select="switchRoom"
                    @favorite="toggleFavoriteRoom"
                    @create="openRoomCreate"
                    @delete="openRoomDelete"
                    @cleanup="openRoomCleanup"
                    @enter="openRoomEnter"
                    variant="sheet"
                >
                    <template #actions>
                        <v-btn icon density="comfortable" variant="text" @click="roomSheet = false">
                            <v-icon>{{ mdiClose }}</v-icon>
                        </v-btn>
                    </template>
                </RoomList>
            </v-card>
        </v-bottom-sheet>

        <v-snackbar
            v-model="toastState.visible"
            :color="toastState.color"
            :timeout="toastState.timeout"
            location="top"
        >
            {{ toastState.text }}
        </v-snackbar>

        </template>
    </v-app>
</template>

<style scoped>
/* ⚠️ 这里**必须**透明。
   Vuetify 的 .v-application 自带一层不透明底色（rgb(var(--v-theme-background))），
   它会盖住 theme.css 里 body::before 那层极光 —— 于是所有毛玻璃容器「透过去还是同一块纯色」，
   backdrop-filter 等于白写（看得出模糊、看不出玻璃）。
   底色交给 theme.css 的 --cc-bg / --cc-aurora 统一管。 */
.app-shell {
    background: transparent;
}

.app-shell--dark {
    background: transparent;
}

.app-shell__main {
    background: transparent;
}

.app-shell__workspace {
    display: flex;
    align-items: flex-start;
    gap: 20px;
    min-height: 100vh;
    min-height: 100dvh;
    padding: 0;
}

.app-shell__workspace--dock-left {
    /* 侧栏在右：DOM 顺序是 [内容, 侧栏]，row 把侧栏排在后面（右边）。
       之前这里写反了 —— `--dock-right` 用 row-reverse 把侧栏甩到了左边，
       于是「切到右侧」实际切到左侧，跟按钮上的箭头正好相反。 */
    flex-direction: row-reverse;
}

.app-shell__workspace--dock-right {
    flex-direction: row;
}

/* 停靠时不留缝：20px 的间隙一出现，侧栏立刻又变回「浮在页面上的卡片」。 */
.app-shell__workspace--dock-left,
.app-shell__workspace--dock-right {
    gap: 0;
}

.app-shell__content {
    flex: 1;
    min-width: 0;
}

.cc-settings-select {
    max-width: 180px;
    min-width: 140px;
}

/* ── 平台密码闸门 ────────────────────────────────────────────────
   铺满整个视口，跟主界面同一套玻璃语言（深浅两套都靠 theme.css 的令牌自动适配）。 */
.auth-gate {
    position: fixed;
    inset: 0;
    z-index: 3000;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--cc-gutter, 16px);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
}

.auth-gate__card {
    width: 100%;
    max-width: 380px;
    padding: 32px 28px 28px;
    border-radius: var(--cc-radius-xl, 28px);
    border: 1px solid var(--cc-glass-border, rgba(148, 163, 184, 0.26));
    background: var(--cc-glass-bg-solid, rgba(255, 255, 255, 0.94));
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    box-shadow: var(--cc-shadow-float, 0 18px 48px rgba(15, 23, 42, 0.22));
    text-align: center;
    animation: cc-pop-in var(--cc-dur-slow, 0.36s) var(--cc-ease, ease) both;
}

.auth-gate__icon {
    width: 64px;
    height: 64px;
    margin: 0 auto 14px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--cc-radius-pill, 999px);
    color: rgb(var(--v-theme-primary));
    background: rgba(var(--v-theme-primary), 0.12);
}

.auth-gate__title {
    font-size: 1.25rem;
    font-weight: 650;
    margin-bottom: 6px;
    color: var(--cc-text, currentColor);
}

.auth-gate__desc {
    font-size: 0.875rem;
    line-height: 1.6;
    margin-bottom: 20px;
    color: var(--cc-text-muted, currentColor);
}

/* 「验证一次管七天」要当面说出来 —— 不知道这件事的人会以为自己在给一台公共电脑上锁 */
.auth-gate__hint {
    font-size: 0.75rem;
    line-height: 1.5;
    margin: -12px 0 18px;
    color: var(--cc-text-muted, currentColor);
    opacity: 0.75;
}

.cc-settings__security-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
}

.auth-gate__field {
    text-align: left;
}

.auth-gate__submit {
    margin-top: 4px;
    min-height: var(--cc-touch-lg, 46px);
    font-weight: 600;
}

.cc-settings__header {
    display: flex;
    align-items: center;
    padding: 10px 16px;
}

/* 设置面板里的布局 / 展示方式切换：胶囊化，跟工作区条上那组保持同一种语言。 */
.cc-workspace-toggle {
    background: transparent !important;
    border-radius: var(--cc-radius-pill, 999px);
}

.cc-workspace-toggle :deep(.v-btn) {
    border-radius: var(--cc-radius-pill, 999px) !important;
}

.cc-settings__header-wrap {
    line-height: 1.3;
}

.cc-settings__title {
    font-size: 18px;
    font-weight: 600;
    line-height: 1.4;
}

.cc-settings__version {
    font-size: 12px;
    color: rgb(var(--v-theme-on-background));
    opacity: 0.7;
}

.cc-settings__item {
    padding: 2px 4px !important;
    min-height: 40px;
}

.cc-settings__group {
    margin-bottom: 10px;
}

/* 分类标题右侧那个「整组开关」，以及面板顶部的开关搜索。 */
/* 设置里的数字输入（分享默认值）。窄一点，别把标题挤没了。 */
.cc-settings__num {
    max-width: 96px;
}

/* 密码比数字长得多（数字 2~3 位就够），168px 下输个稍微像样的密码就被挤没了。
   ⚠️ 必须写 `width` 而不是 `max-width`：v-list-item 的 append 列是 `auto`，
   它的宽度由输入框的**内容固有宽度**（内部 `<input>` 默认 size=20）决定 ——
   max-width 比它大就永远不生效，写多少都还是 ~170px。 */
.cc-settings__text {
    width: 220px;
}

.cc-settings__group-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
}

.cc-settings__group-head .v-switch {
    flex: none;
    margin: 0;
}

.cc-settings__subheader {
    padding: 0 4px 2px;
    font-size: 12px;
    font-weight: 500;
    color: rgb(var(--v-theme-on-background));
    opacity: 0.75;
}

.cc-settings__list {
    background: transparent;
    padding: 0;
}

.cc-settings__link-title {
    color: inherit;
}

.cc-picker-dialog__body {
    padding-top: 0;
}

.cc-picker-dialog__body .v-color-picker {
    width: 100%;
    max-width: 340px;
}

.cc-settings__theme-actions {
    display: flex;
    align-items: center;
    gap: 8px;
}

.cc-settings__theme-btn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
}

.cc-settings__swatch {
    display: inline-block;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    border: 1px solid rgba(var(--v-theme-on-background), 0.2);
}

.cc-settings__link {
    display: inline-flex;
    align-items: center;
    color: inherit;
    text-decoration: none;
}

.cc-settings__link:hover {
    color: rgb(var(--v-theme-primary));
}

.cc-settings__external {
    margin-left: 4px;
    opacity: 0.55;
}

.v-alert {
    top: 0;
    z-index: 5;
}

.room-browser {
    border-top-left-radius: 24px;
    border-top-right-radius: 24px;
    background: rgba(255, 255, 255, 0.96);
}

.room-browser--dark {
    background: rgba(15, 23, 42, 0.96);
}

/* 头部和列表内部样式都在 RoomList.vue 里 —— 它们必须跟着当前模式走，
   而模式皮肤是那组 --rl-* 变量。留在这里的只有「容器」这一层。 */

/* 桌面侧栏：贴边、通高、无圆角无投影 —— 它得是「那一栏」本身，不是浮在上面的卡片。
   底色与内侧发丝线交给 RoomList 的 --rl-* token（只有它认识六种模式皮肤）；
   这里只负责占位与停靠。 */
.room-browser--dock {
    position: sticky;
    top: 0;
    flex: 0 0 380px;
    width: 380px;
    height: 100vh;
    height: 100dvh;
    border-radius: 0;
    box-shadow: none;
    background: transparent;
}

@media (max-width: 1263px) {
    .app-shell__workspace {
        display: block;
        padding: 0;
    }
}

/* 侧栏头部那两个图标按钮（停靠方向 / 关闭）。Vuetify 默认那层悬停遮罩在便签、终端
   这类自带底色的皮肤上几乎看不出来，所以显式给一层中性底色。 */
.room-browser__action {
    color: inherit;
}

.room-browser__action:hover {
    background: rgba(148, 163, 184, 0.2);
}

/* 认证密码与进入房间弹窗的输入框：去掉灰色填充底色 */
:deep(.cc-dialog-field .v-field__field) {
    background-color: transparent !important;
}
:deep(.cc-dialog-field.v-field--error) {
    background-color: transparent !important;
}

</style>

<style>
/* 这几条必须写在**非 scoped** 块里：html / body 不在组件模板里，
   scoped 规则会被加上 [data-v-xxx] 属性选择器，永远匹配不到它们。

   背景：移动端整页能被往上拖几十 px，输入框（在文档流里）跟着脱离底部。
   两个来源都要堵：
     1) 页面里任何 `100vh` 都等于「地址栏收起时」的高度，比可视区高一截 ——
        真机上 `100vh != 100dvh`，于是文档比可视区高，根滚动容器就能被拖。
        各处的 `100vh` 后面都已补上 `100dvh`（headless 里两者相等，量不出来）。
     2) 根滚动容器带 overflow-y: scroll，触摸设备上会产生 overscroll 回弹。
        这里关掉回弹兜底；页面本来就没有溢出，不影响任何正常滚动。 */
html,
body {
    overscroll-behavior: none;
}
</style>
