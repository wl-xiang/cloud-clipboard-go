<script setup>import { computed, inject, ref } from 'vue';
import { useAppStore } from '@/store/app';
import { useWebSocketStore } from '@/store/websocket';
import { useTheme } from 'vuetify';
import { useI18n } from 'vue-i18n';
import { MODES } from '@/views/modes/registry.js';

const props = defineProps({
    variant: { type: String, default: 'default' },
});

const app = useAppStore();
const ws = useWebSocketStore();
const theme = useTheme();
const isDark = computed(() => theme.current.value?.dark ?? false);
const { t } = useI18n();

// 默认**收起**：顶栏是低频设置的集合（房间列表开关 / 模式切换 / 设置），
// 而高频动作（清空、深浅色）已经搬到工作区条上 —— 默认收起能给内容让出一条 ~52px 的高度。
// 展开偏好记在 localStorage：展开过一次就一直展开，不会每次刷新都弹出来。
const toolbarCollapsed = ref(localStorage.getItem('pageToolbarCollapsed') !== 'false');

function toggleToolbar() {
    toolbarCollapsed.value = !toolbarCollapsed.value;
    localStorage.setItem('pageToolbarCollapsed', String(toolbarCollapsed.value));
}

const actions = inject('pageToolbarActions', {});

const roomCount = computed(() => Number(actions.roomCount?.value ?? actions.roomCount ?? 0));
const roomListEnabled = computed(() => Boolean(actions.roomListEnabled?.value ?? actions.roomListEnabled));
const roomBrowserVisible = computed(() => Boolean(actions.roomBrowserVisible?.value ?? actions.roomBrowserVisible));

const roomName = computed(() => ws.room || t('publicRoom'));

// 房间 chip 上的锁：🔒 = 进这个房间要密码，🌍 = 公开。
//
// 数据来自 `roomProtectionCache`，而它由 `fetchServerInfo` 用 `/server?room=` 的
// `roomProtected` 填充。**那个字段的含义是「这个房间实际要不要密码」**，不是
// 「roomAuth 里有没有配这一项」—— 显式 `{open: true}` 的房间有配置项但不要密码，
// 「有全局密码、没配过房间条目」的房间没配置项却要密码。服务端那边由
// resolveRoomAuth(...).Required 算（lib/handler.go，有 server_room_protected_test.go 钉着）。
//
// ⚠️ 缓存是**三态**的：true / false / undefined（还没问过服务端）。
// 这里用 Boolean() 把 undefined 归成 false，也就是「未知」先按公开画 ——
// 窗口是一次 /server 往返（connect() 里必发，见 store/websocket.js），所以只会闪一下；
// 别把它当成「已经确认公开」的信号用。
const isProtected = computed(() => Boolean(ws.roomProtectionCache?.[ws.normalizeRoomName(ws.room)]));
const latencyValue = computed(() => {
    if (ws.latency === null) {
        return '';
    }
    return `${Math.round(ws.latency)} ms`;
});
const latencyHexColor = computed(() => {
    if (ws.latency === null) {
        return '';
    }
    let colorName = 'success';
    if (ws.latency >= 60 && ws.latency < 120) {
        colorName = 'warning';
    } else if (ws.latency >= 120) {
        colorName = 'error';
    }
    const themeColors = theme.themes.value[isDark.value ? 'dark' : 'light'].colors;
    return themeColors[colorName] || colorName;
});

function setMode(mode) {
    app.setUiMode(mode);
}

const currentMode = computed(() => MODES.find(mode => mode.key === app.uiMode) || MODES[0]);

// 模式下拉框直接铺开整份 MODES：巨型与终端已下架删除，聊天和工作台回到常驻，
// 「即将下架」分组与配套的投票入口一并移除 —— 没有待下架的模式了，留着空分组只会碍事。
const modes = computed(() => MODES);
</script>

<template>
    <div
        class="page-toolbar"
        :class="[
            `page-toolbar--${variant}`,
            { 'page-toolbar--dark': isDark },
            { 'page-toolbar--collapsed': toolbarCollapsed }
        ]"
    >
        <div class="page-toolbar__inner" v-show="!toolbarCollapsed">
            <div class="page-toolbar__leading">
                <v-tooltip v-if="ws.room" :text="t('backToDefaultRoom')" location="bottom">
                    <template v-slot:activator="{ props }">
                        <v-btn icon density="compact" size="small" variant="text" class="page-toolbar__home-btn" v-bind="props" @click="ws.switchRoom('')">
                            <v-icon size="24">mdi-home-outline</v-icon>
                        </v-btn>
                    </template>
                </v-tooltip>

                <!-- 连接图标只在「没连上」时出现。
                     连上之后它是个**死按钮**：toggleConnection 只在断开时重连，
                     而「连接好不好」已经由房间 chip 里的延迟数字（还带颜色分级）表达了 ——
                     常驻就只是白占一个位置、还让人以为点了有用。
                     断开时给 error 色：这是异常态，不是常态装饰。 -->
                <v-tooltip
                    v-if="!ws.websocket"
                    :text="ws.websocketConnecting ? t('connecting') : t('disconnected')"
                    location="bottom"
                >
                    <template v-slot:activator="{ props }">
                        <v-btn icon density="compact" size="small" variant="text" v-bind="props" @click="actions.toggleConnection && actions.toggleConnection()">
                            <v-icon size="24" :color="ws.websocketConnecting ? undefined : 'error'">
                                {{ ws.websocketConnecting ? 'mdi-lan-pending' : 'mdi-lan-disconnect' }}
                            </v-icon>
                        </v-btn>
                    </template>
                </v-tooltip>

                <v-chip
                    size="small"
                    variant="tonal"
                    :color="variant === 'sticky' ? 'amber-darken-1' : 'primary'"
                    class="page-toolbar__room"
                    :title="t('showQrCode')"
                    @click="actions.openPageQr && actions.openPageQr()"
                >
                    <v-icon start size="x-small">
                        {{ isProtected ? 'mdi-lock' : 'mdi-earth' }}
                    </v-icon>
                    <span v-if="ws.room" class="page-toolbar__roomname">{{ roomName }}</span>
                    <span v-else>{{ roomName }}</span>
                    <span
                        v-if="ws.websocket && ws.latency !== null"
                        class="page-toolbar__latency"
                        :style="{ color: latencyHexColor }"
                    >
                        {{ latencyValue }}
                    </span>
                </v-chip>
            </div>

            <div class="page-toolbar__actions">
                <v-menu location="bottom end" min-width="192" :close-on-content-click="true">
                    <template v-slot:activator="{ props: menuProps }">
                        <button
                            v-bind="menuProps"
                            class="page-toolbar__mode"
                            :title="t('uiMode')"
                        >
                            <!-- 菜单栏里只留文字：旁边那几个都是纯图标按钮，这里放个图标反而
                                 多一层信息（而且 6 个模式图标挤在一起时辨识度本来就低）。
                                 图标留到下拉里，那里有文字并排，认得出。
                                 文字不能再挂 d-none d-sm-inline —— 图标去掉后它是唯一内容，
                                 窄屏藏了就只剩一个箭头。 -->
                            <span class="page-toolbar__mode-label">{{ t(currentMode.labelKey) }}</span>
                            <v-icon size="x-small" class="page-toolbar__mode-caret">mdi-chevron-down</v-icon>
                        </button>
                    </template>
                    <v-list density="compact" nav>
                        <v-list-item
                            v-for="mode in modes"
                            :key="mode.key"
                            :active="app.uiMode === mode.key"
                            @click="setMode(mode.key)"
                        >
                            <template v-slot:prepend>
                                <v-icon size="small">{{ mode.icon }}</v-icon>
                            </template>
                            <v-list-item-title>{{ t(mode.labelKey) }}</v-list-item-title>
                        </v-list-item>
                    </v-list>
                </v-menu>

                <div class="page-toolbar__group">
                <!-- 这个按钮现在是房间侧栏的开关（原来只能「开」，关在侧栏头部那个 ✕ 上）。
                     开着时必须看得出来 —— 否则「已开启」和「点它能关」都读不出来。 -->
                <v-tooltip v-if="roomListEnabled" :text="roomBrowserVisible ? t('hideRoomBrowser') : t('showRoomBrowser')" location="bottom">
                    <template v-slot:activator="{ props }">
                        <v-btn
                            icon
                            density="compact"
                            size="small"
                            variant="text"
                            class="page-toolbar__icon"
                            :class="{ 'page-toolbar__icon--active': roomBrowserVisible }"
                            v-bind="props"
                            @click="actions.openRoomBrowser && actions.openRoomBrowser()"
                        >
                            <v-badge :content="roomCount" :model-value="roomCount > 0" color="accent" overlap>
                                <v-icon size="24">mdi-view-list</v-icon>
                            </v-badge>
                        </v-btn>
                    </template>
                </v-tooltip>

                    <!-- 「按名称进入」只在**房间列表关闭**时留在这里。
                         列表开着的时候，它已经在侧栏的动作行里 —— 同一件事摆两个入口
                         正是「入口混乱」的来源，所以任何时候只留一个。 -->
                    <v-tooltip v-if="!roomListEnabled" :text="t('enterRoomByName')" location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon density="compact" size="small" variant="text" v-bind="props" @click="actions.openRoomDialog && actions.openRoomDialog()">
                                <v-icon size="24">mdi-door-open</v-icon>
                            </v-btn>
                        </template>
                    </v-tooltip>
                </div>

                <div class="page-toolbar__group">
                </div>
            </div>
        </div>

        <button
            type="button"
            class="page-toolbar__collapse-toggle"
            :class="{ 'page-toolbar__collapse-toggle--opened': !toolbarCollapsed }"
            :title="toolbarCollapsed ? t('expandToolbar') : t('collapseToolbar')"
            @click="toggleToolbar"
        >
            <v-icon size="small">{{ toolbarCollapsed ? 'mdi-chevron-double-down' : 'mdi-chevron-double-up' }}</v-icon>
        </button>
    </div>
</template>

<style scoped>
.page-toolbar {
    position: sticky;
    top: 0;
    z-index: 40;
    /* ── 毛玻璃 ────────────────────────────────────────────────
       这条栏一直粘在顶上，底下永远是滚动的内容 —— 正是毛玻璃最合适的场景。
       各模式的底色（下面一组规则）都改成了半透明，blur 才有东西可透。
       ⚠️ 不给它加圆角：它是通栏，圆角会让内容从角上漏出来。 */
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    box-shadow: var(--cc-shadow-1, 0 2px 10px rgba(15, 23, 42, 0.06));
    transition: background-color var(--cc-dur, 0.22s) var(--cc-ease, ease);
}

.page-toolbar--collapsed .page-toolbar__inner {
    display: none !important;
}

/* 收起态：顶条整体高度收到 14px，中间留一块「抓手」，点它展开。
   ⚠️ 必须给一个非零高度 —— 原来收起后这条的高度是 0，把手只剩 4px 的一条缝，
   在深色底上基本看不见，用户找不到怎么把工具栏叫回来。 */
.page-toolbar--collapsed {
    height: 14px;
    background: transparent;
    box-shadow: none;
    backdrop-filter: none;
    -webkit-backdrop-filter: none;
}

.page-toolbar--collapsed .page-toolbar__collapse-toggle {
    top: 0;
    bottom: auto;
    width: 72px;
    height: 14px;
    border-radius: 0 0 var(--cc-radius-sm, 12px) var(--cc-radius-sm, 12px);
    background: var(--cc-glass-bg-solid, rgba(255, 255, 255, 0.94));
    opacity: 0.75;
}

.page-toolbar--collapsed .page-toolbar__collapse-toggle .v-icon {
    display: inline-flex;
    font-size: 15px;
}

.page-toolbar--collapsed .page-toolbar__collapse-toggle:hover {
    opacity: 1;
    width: 96px;
}

.page-toolbar__collapse-toggle {
    position: absolute;
    left: 50%;
    bottom: -2px;
    transform: translateX(-50%);
    z-index: 5;
    width: 26px;
    height: 4px;
    border-radius: 999px;
    border: none;
    background: currentColor;
    opacity: 0.35;
    cursor: pointer;
    padding: 0;
    transition: opacity var(--cc-dur-fast, 0.14s) var(--cc-ease, ease),
                width var(--cc-dur-fast, 0.14s) var(--cc-ease, ease),
                height var(--cc-dur-fast, 0.14s) var(--cc-ease, ease);
}

.page-toolbar__collapse-toggle:hover {
    opacity: 0.8;
    width: 36px;
}

.page-toolbar__collapse-toggle .v-icon {
    display: none;
}

.page-toolbar--dark .page-toolbar__collapse-toggle {
    background: currentColor;
    opacity: 0.3;
}

.page-toolbar__inner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    max-width: 1100px;
    margin: 0 auto;
    padding: 8px 16px;
    flex-wrap: nowrap;
}

.page-toolbar__leading {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: 1 1 auto;
    min-width: 0;
}

.page-toolbar__home-btn {
    flex-shrink: 0;
}

.page-toolbar__room {
    cursor: pointer;
    flex: 0 1 auto;
    min-width: 0;
    max-width: 46%;
}

.page-toolbar__room :deep(.v-chip__content) {
    min-width: 0;
}

.page-toolbar__room :deep(.v-chip__content > span) {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
}

.page-toolbar__roomname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
}

.page-toolbar__latency {
    font-size: 0.7rem;
    font-weight: 700;
    margin-left: 6px;
    flex-shrink: 0;
}

.page-toolbar__room :deep(.v-icon) {
    flex-shrink: 0;
}

/* 右侧按语义分三段：视图（模式）/ 房间 / 系统。
   组内贴紧、组间留空档 —— 用间距而不是竖线分隔符：这条栏本来就很密，
   再加一种视觉元素只会更吵。
   ⚠️ 不要靠「图标大小」分主次：差 2px 眼睛看不出来，只会显得没对齐。
   层级交给分组间距和悬停色表达。 */
.page-toolbar__actions {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-shrink: 0;
    min-width: 0;
}

.page-toolbar__group {
    display: flex;
    align-items: center;
    gap: 2px;
}

/* 开关类图标的「已开启」态（目前是房间侧栏那一个）。用和模式触发器同一支蓝，
   整套工具栏只有这一支强调色。 */
.page-toolbar__icon--active {
    background: rgba(30, 136, 229, 0.16);
}

.page-toolbar__icon--active :deep(.v-icon) {
    color: #1e88e5;
}

.page-toolbar--dark .page-toolbar__icon--active {
    background: rgba(144, 202, 249, 0.2);
}

.page-toolbar--dark .page-toolbar__icon--active :deep(.v-icon) {
    color: #90caf9;
}

/* 模式触发器。改前是「半透明白底 + 淡边框 + 悬停整块变实心蓝」，两个毛病：
   1) 白底淡边框让它读起来像状态标签，跟左边的房间 chip 撞脸，看不出是个控件；
   2) 悬停直接变实心蓝，跟旁边那几个图标按钮的轻悬停不是一套语言。
   现在跟图标按钮统一：无边框、静止一层中性底色、悬停只加深一点。
/* 模式选择器。几条取舍（第一版踩过）：
   1) 白底淡边框让它读起来像状态标签，跟左边的房间 chip 撞脸，看不出是个控件；
   2) 悬停直接变实心蓝，跟旁边那几个图标按钮的轻悬停不是一套语言。
   现在跟图标按钮统一：无边框、静止一层中性底色、悬停只加深一点。
   胶囊形状保留 —— 它是「选择器」，胶囊比圆角方块更能说明这件事。 */
.page-toolbar__mode {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    border: none;
    background: rgba(148, 163, 184, 0.14);
    border-radius: 999px;
    padding: 3px 10px;
    font-size: 0.78rem;
    line-height: 1.3;
    font-family: inherit;
    cursor: pointer;
    color: rgba(71, 85, 105, 0.95);
    transition: background var(--cc-dur-fast, 0.14s) var(--cc-ease, ease),
                color var(--cc-dur-fast, 0.14s) var(--cc-ease, ease),
                transform var(--cc-dur-fast, 0.14s) var(--cc-ease, ease);
}

.page-toolbar__mode:hover {
    background: rgba(148, 163, 184, 0.26);
}

/* 按下去有个回弹：这条栏上所有控件同一种反馈语言（图标按钮由 Vuetify 自己的
   overlay 表达，这里补的是「动」这一层）。 */
.page-toolbar__mode:active {
    transform: scale(0.96);
}

.page-toolbar--dark .page-toolbar__mode {
    background: rgba(148, 163, 184, 0.16);
    color: rgba(203, 213, 225, 0.92);
}

.page-toolbar--dark .page-toolbar__mode:hover {
    background: rgba(148, 163, 184, 0.28);
}

.page-toolbar__mode-caret {
    opacity: 0.7;
}

@media (max-width: 600px) {
    .page-toolbar__room {
        flex: 0 1 auto;
        max-width: 100%;
    }
}

/* 宽屏把工具栏的图标按钮放大到跟卡片那排一致（40px 上下）。
   ⚠️ 只在 ≥960px 生效：这条栏是 nowrap 的，窄屏上加尺寸会直接把房间 chip 挤出屏幕
   （实测 390px 宽下 5 个图标各加 4px 就已经溢出）。 */
@media (min-width: 960px) {
    .page-toolbar__inner .v-btn--icon {
        min-width: var(--cc-touch, 40px);
        min-height: var(--cc-touch, 40px);
        border-radius: var(--cc-radius-pill, 999px);
        transition: transform var(--cc-dur-fast, 0.14s) var(--cc-ease, ease);
    }

    .page-toolbar__inner .v-btn--icon:active {
        transform: scale(0.92);
    }

    .page-toolbar__inner .v-btn--icon :deep(.v-icon) {
        font-size: 26px;
    }
}
</style>