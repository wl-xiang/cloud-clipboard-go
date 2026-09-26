<script setup>import { computed, nextTick, ref, watch } from 'vue';
import { isImageName, looksLikeTable, looksLikeTaskList } from '@/util.js';
import { useAppStore } from '@/store/app';
import { useWebSocketStore } from '@/store/websocket';
import { useDisplay, useTheme } from 'vuetify';
import { useI18n } from 'vue-i18n';
import { inject } from 'vue';
import UnifiedComposer from '@/components/UnifiedComposer.vue';
import ReceivedText from '@/components/received-item/Text.vue';
import ReceivedFile from '@/components/received-item/File.vue';
import { LAYOUT_OPTIONS, VIEW_OPTIONS, effectiveLayout } from '@/data/workspace.js';
import { MODES } from '@/views/modes/registry.js';

const mdiTimeline = 'mdi-timeline';
const mdiTextBox = 'mdi-text-box-outline';
const mdiImage = 'mdi-image-outline';
const mdiFile = 'mdi-file-outline';
const mdiCheckboxMarkedOutline = 'mdi-checkbox-marked-outline';
const mdiTable = 'mdi-table';
const tlLastRoom = ref('');
let tlJustSwitched = false;


const app = useAppStore();
const ws = useWebSocketStore();
const theme = useTheme();
const isDark = computed(() => theme.current.value?.dark ?? false);
const { t } = useI18n();
const display = useDisplay();
const composer = ref(null);
// 清空剪贴板弹窗的状态在 App.vue 里 —— 走同一条 provide/inject 通道（工具栏也用它）。
const toolbarActions = inject('pageToolbarActions', {});

// ── 这些原来住在顶栏（PageToolbar），顶栏并入工作区条之后跟着搬过来 ──
// 房间 chip：名字 + 锁/地球 + 延迟（颜色分级）。
const roomName = computed(() => ws.room || t('publicRoom'));
// 「这个房间实际要不要密码」来自 /server 的 roomProtected（有测试钉着它的含义）。
// 缓存是三态：true / false / undefined（还没问过）—— 未知先按公开画，只会闪一下。
const roomProtected = computed(() => Boolean(ws.roomProtectionCache?.[ws.normalizeRoomName(ws.room)]));
const latencyText = computed(() => (ws.latency === null ? '' : `${Math.round(ws.latency)} ms`));
const latencyColor = computed(() => {
    if (ws.latency === null) {
        return '';
    }
    let name = 'success';
    if (ws.latency >= 60 && ws.latency < 120) {
        name = 'warning';
    } else if (ws.latency >= 120) {
        name = 'error';
    }
    return theme.themes.value[isDark.value ? 'dark' : 'light'].colors[name] || name;
});

// 三个下拉当前选中项（模式 / 布局 / 展示方式）。
const currentMode = computed(() => MODES.find((mode) => mode.key === app.uiMode) || MODES[0]);
const currentLayout = computed(() => LAYOUT_OPTIONS.find((option) => option.key === app.homeLayout) || LAYOUT_OPTIONS[0]);
const currentView = computed(() => VIEW_OPTIONS.find((option) => option.key === app.historyView) || VIEW_OPTIONS[0]);

// 房间面板开关的状态与数量（App.vue 通过 provide 传下来）。
const roomCount = computed(() => Number(toolbarActions.roomCount?.value ?? toolbarActions.roomCount ?? 0));
const roomListEnabled = computed(() => Boolean(toolbarActions.roomListEnabled?.value ?? toolbarActions.roomListEnabled));
const roomBrowserVisible = computed(() => Boolean(toolbarActions.roomBrowserVisible?.value ?? toolbarActions.roomBrowserVisible));

// 布局 / 展示方式来自 store（持久化在 localStorage，见 store/app.js 的说明）。
// 当前**实际**布局：左右布局在窄屏下会退化成上下（CSS 也有一份同样的回退），
// JS 侧要「知道现在到底是不是聊天式」——新消息该往哪边滚就靠它，不能只看用户选的值。
const layout = computed(() => app.homeLayout);
const effective = computed(() => effectiveLayout(app.homeLayout, display.width.value));
const isGrid = computed(() => app.historyView === 'grid');

// 时间流的分类过滤。选择存 localStorage —— 切模式/刷新后不该莫名回到「全部」。
// 默认 'all'：跟「显示开关默认全开」同一条原则，默认态不能改变既有行为。
const TIMELINE_FILTER_KEY = 'timelineFilter';
const TIMELINE_FILTER_KEYS = ['all', 'text', 'image', 'file', 'task', 'table'];
const storedFilter = localStorage.getItem(TIMELINE_FILTER_KEY);
const timelineFilter = ref(TIMELINE_FILTER_KEYS.includes(storedFilter) ? storedFilter : 'all');
function setTimelineFilter(key) {
    timelineFilter.value = key;
    localStorage.setItem(TIMELINE_FILTER_KEY, key);
}

// 搜索条什么时候出现：开关打开，或者「纯预览模式」（六个模式都把发送区关掉）——
// 后者是 Jonny 要求的「自动出现」：那种状态下没有发送动作，搜索才是主操作。
const showTimelineSearch = computed(() => app.display.timelineSearch || app.composerDisabledEverywhere);

// 「任务列表」「表格」是**文本条目里的 markdown 结构**，不是新的条目类型 ——
// 所以它们排在三个类型之后：前面三格回答「是什么」，后两格回答「里面有什么结构」。
const FILTER_OPTIONS = [
    { key: 'all', labelKey: 'filterAll', icon: mdiTimeline },
    { key: 'text', labelKey: 'filterText', icon: mdiTextBox },
    { key: 'image', labelKey: 'filterImage', icon: mdiImage },
    { key: 'file', labelKey: 'filterFile', icon: mdiFile },
    { key: 'task', labelKey: 'filterTaskList', icon: mdiCheckboxMarkedOutline },
    { key: 'table', labelKey: 'filterTable', icon: mdiTable },
];

// 「图片」= 文件条目里文件名是图片扩展名的那些；文本条目永远只归「文本」。
// 判型用 util 的 isImageName（全站唯一实现），别在这里再抄一份正则。
//
// 「任务列表」「表格」同理用 util 的 looksLikeTaskList / looksLikeTable。
// ⚠️ 直接拿 item.content 判、**不解 HTML 实体**：这两个判据只看 `|` `-` `[` `]` `x` 空格，
// 实体编码动的是 `&` `<` `>` 之类，标记本身不会被编码。
const filteredReceived = computed(() => {
    // 开关关掉时整个过滤不生效（不只是藏起分类条）——
    // 否则用户关掉开关后，列表还停在上次选的分类上，看着像内容丢了。
    // 列表来源是 visibleReceived（已套搜索），不是 received ——
    // 否则纯预览模式下搜索框只在别的模式生效。
    const list = app.visibleReceived;
    if (!app.display.timelineFilter) return list;
    if (timelineFilter.value === 'all') return list;
    if (timelineFilter.value === 'text') return list.filter((item) => item.type === 'text');
    if (timelineFilter.value === 'task') {
        return list.filter((item) => item.type === 'text' && looksLikeTaskList(item.content));
    }
    if (timelineFilter.value === 'table') {
        return list.filter((item) => item.type === 'text' && looksLikeTable(item.content));
    }
    const wantImage = timelineFilter.value === 'image';
    return list.filter((item) => item.type === 'file' && isImageName(item.name) === wantImage);
});
const historyUsageLabel = computed(() => {
    const current = app.received.length;
    const limit = Number(app.config?.server?.history || 0);
    return `${current}/${limit}`;
});

// 渲染顺序**跟着布局走**：
//   · 上下 / 左右：`received` 本来就是「最新在前」，直接顺着渲染（既有行为）；
//   · 聊天式：聊天软件里最新的一条在**最下面**，所以要反过来。
//
// ⚠️ 这不是纯 CSS 能解决的（`flex-direction: column-reverse` 会把滚动条和
// 自动滚动方向一起弄反），所以放在这里按布局翻转。
// 翻转只作用于**渲染**，不动 store —— 别去改 app.received 的顺序，
// 其它四个模式都按「最新在前」在用它。
const orderedReceived = computed(() => (
    effective.value === 'chat' ? filteredReceived.value.slice().reverse() : filteredReceived.value
));

function focusComposer(type) {
    nextTick(() => {
        if (composer.value && typeof composer.value.focus === 'function') {
            composer.value.focus(type);
        }
    });
}

// 新消息到达时的自动滚动方向，**跟着布局走**：
//   · 上下 / 左右：输入区在上面，最新一条在最上面 → 靠近顶部时滚回顶部（既有行为）；
//   · 聊天式：输入区在下面，最新一条在最下面 → 靠近底部时滚到底部。
// 判据都用「离那一端是否在 120/160px 内」，而不是无条件滚 ——
// 用户正在翻旧消息时被强行拉走，比不自动滚更烦。
const NEAR_TOP_PX = 120;
const NEAR_BOTTOM_PX = 160;

// 进入聊天式布局后的**第一次**填充要无条件滚到底：那时页面还在顶部，
// 「离底部够不够近」必然是否，会被下面的守卫挡掉 —— 用户开屏看到的是最旧的一条。
let tlChatNeedsFirstScroll = true;

function tlNearTop() {
    return window.scrollY < NEAR_TOP_PX;
}

function tlNearBottom() {
    const el = document.documentElement;
    return el.scrollHeight - window.scrollY - window.innerHeight < NEAR_BOTTOM_PX;
}

function tlScrollTop(smooth = true) {
    window.scrollTo({ top: 0, behavior: smooth ? 'smooth' : 'auto' });
}

function tlScrollBottom(smooth = true) {
    window.scrollTo({ top: document.documentElement.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
}

watch(() => app.received, () => {
    const chat = effective.value === 'chat';
    if (tlJustSwitched) {
        tlJustSwitched = false;
        // 换房间后直接落到「最新那一端」：非聊天式是顶部，聊天式是底部。
        tlChatNeedsFirstScroll = true;
        nextTick(() => (chat ? tlScrollBottom(false) : tlScrollTop(false)));
        return;
    }
    if (chat) {
        if (!tlChatNeedsFirstScroll && !tlNearBottom()) return;
        tlChatNeedsFirstScroll = false;
        nextTick(() => tlScrollBottom(true));
        return;
    }
    if (!tlNearTop()) return;
    nextTick(() => tlScrollTop(true));
});

// 切布局时也要把位置摆正：从「上下」切到「聊天式」，如果停在顶部，
// 用户看到的是最旧的一条而不是最新的一条 —— 看起来像内容反了。
watch(() => app.homeLayout, (next) => {
    tlChatNeedsFirstScroll = true;
    nextTick(() => (next === 'chat' ? tlScrollBottom(false) : tlScrollTop(false)));
});

watch(() => ws.room, (room) => {
    if (tlLastRoom.value !== '' && tlLastRoom.value !== room) {
        tlJustSwitched = true;
    }
    tlLastRoom.value = room;
});

</script>

<template>
    <div
        class="home-minimal"
        :class="[
            { 'home-minimal--dark': isDark },
            `home-minimal--${app.homeLayout}`,
            { 'home-minimal--grid': isGrid },
        ]"
    >
        <v-container fluid class="home-minimal__body" :class="{ 'home-minimal__body--split': app.homeLayout === 'split' }">
            <div class="home-minimal__shell mx-auto">
            <!-- 工作区条：布局 + 展示方式。放在最上面、不参与滚动 ——
                 它是「这一屏怎么摆」的开关，跟着内容滚走就得回头找。 -->
<div class="workspace-bar" :class="{ 'workspace-bar--dark': isDark }">
                <!-- ── 全站唯一的命令栏 ─────────────────────────────────
                     顶栏（PageToolbar）在标准模式下不再渲染，它的按钮全部并到这里。
                     排列按「左边是身份与位置，右边是这一屏怎么显示 + 高频动作」：
                       [↩ 回默认房间] [⚠ 未连接] [🔒/🌍 房间 · 延时]  [模式▾] [布局▾] [展示▾] [房间面板] | [🌙] [🧹] [⚙]
                     模式 / 布局 / 展示方式用**下拉**而不是按钮组：
                     按钮组在窄容器（左右布局 / 手机）里放不下，下拉只占一个槽位。 -->
                <div class="workspace-bar__group">
                    <v-tooltip v-if="ws.room" :text="t('backToDefaultRoom')" location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon density="compact" size="small" variant="text" class="workspace-bar__icon" v-bind="props" @click="toolbarActions.goHome && toolbarActions.goHome()">
                                <v-icon size="22">mdi-home-outline</v-icon>
                            </v-btn>
                        </template>
                    </v-tooltip>

                    <!-- 连接图标只在「没连上」时出现：连上之后连接质量已由房间 chip 里的
                         延迟数字（带颜色分级）表达，常驻只是白占位置。 -->
                    <v-tooltip v-if="!ws.websocket" :text="ws.websocketConnecting ? t('connecting') : t('disconnected')" location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon density="compact" size="small" variant="text" v-bind="props" @click="toolbarActions.toggleConnection && toolbarActions.toggleConnection()">
                                <v-icon size="22" :color="ws.websocketConnecting ? undefined : 'error'">
                                    {{ ws.websocketConnecting ? 'mdi-lan-pending' : 'mdi-lan-disconnect' }}
                                </v-icon>
                            </v-btn>
                        </template>
                    </v-tooltip>

                    <v-chip
                        size="small"
                        variant="tonal"
                        color="primary"
                        class="workspace-bar__room"
                        :title="t('showQrCode')"
                        @click="toolbarActions.openPageQr && toolbarActions.openPageQr()"
                    >
                        <v-icon start size="x-small">{{ roomProtected ? 'mdi-lock' : 'mdi-earth' }}</v-icon>
                        <span>{{ roomName }}</span>
                        <span v-if="ws.websocket && ws.latency !== null" class="workspace-bar__latency" :style="{ color: latencyColor }">
                            {{ latencyText }}
                        </span>
                    </v-chip>
                </div>

                <div class="workspace-bar__group workspace-bar__group--end">
                    <v-menu location="bottom end" min-width="176" :close-on-content-click="true">
                        <template v-slot:activator="{ props: menuProps }">
                            <button v-bind="menuProps" class="workspace-bar__select" :title="t('uiMode')">
                                <v-icon size="16" class="workspace-bar__select-icon">{{ currentMode.icon }}</v-icon>
                                <span class="workspace-bar__select-label">{{ t(currentMode.labelKey) }}</span>
                                <v-icon size="x-small">mdi-chevron-down</v-icon>
                            </button>
                        </template>
                        <v-list density="compact" nav>
                            <v-list-item v-for="mode in MODES" :key="mode.key" :active="app.uiMode === mode.key" @click="app.setUiMode(mode.key)">
                                <template v-slot:prepend><v-icon size="small">{{ mode.icon }}</v-icon></template>
                                <v-list-item-title>{{ t(mode.labelKey) }}</v-list-item-title>
                            </v-list-item>
                        </v-list>
                    </v-menu>

                    <v-menu location="bottom end" min-width="176" :close-on-content-click="true">
                        <template v-slot:activator="{ props: menuProps }">
                            <button v-bind="menuProps" class="workspace-bar__select" :title="t('workspaceLayout')">
                                <v-icon size="16" class="workspace-bar__select-icon">{{ currentLayout.icon }}</v-icon>
                                <span class="workspace-bar__select-label">{{ t(currentLayout.labelKey) }}</span>
                                <v-icon size="x-small">mdi-chevron-down</v-icon>
                            </button>
                        </template>
                        <v-list density="compact" nav>
                            <v-list-item v-for="option in LAYOUT_OPTIONS" :key="option.key" :active="app.homeLayout === option.key" @click="app.setHomeLayout(option.key)">
                                <template v-slot:prepend><v-icon size="small">{{ option.icon }}</v-icon></template>
                                <v-list-item-title>{{ t(option.labelKey) }}</v-list-item-title>
                            </v-list-item>
                        </v-list>
                    </v-menu>

                    <v-menu location="bottom end" min-width="160" :close-on-content-click="true">
                        <template v-slot:activator="{ props: menuProps }">
                            <button v-bind="menuProps" class="workspace-bar__select" :title="t('workspaceViewMode')">
                                <v-icon size="16" class="workspace-bar__select-icon">{{ currentView.icon }}</v-icon>
                                <span class="workspace-bar__select-label">{{ t(currentView.labelKey) }}</span>
                                <v-icon size="x-small">mdi-chevron-down</v-icon>
                            </button>
                        </template>
                        <v-list density="compact" nav>
                            <v-list-item v-for="option in VIEW_OPTIONS" :key="option.key" :active="app.historyView === option.key" @click="app.setHistoryView(option.key)">
                                <template v-slot:prepend><v-icon size="small">{{ option.icon }}</v-icon></template>
                                <v-list-item-title>{{ t(option.labelKey) }}</v-list-item-title>
                            </v-list-item>
                        </v-list>
                    </v-menu>

                    <v-tooltip :text="roomBrowserVisible ? t('hideRoomBrowser') : t('showRoomBrowser')" location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn
                                icon
                                size="small"
                                variant="text"
                                class="cc-press workspace-bar__icon"
                                :class="{ 'workspace-bar__icon--active': roomBrowserVisible }"
                                v-bind="props"
                                :aria-label="roomBrowserVisible ? t('hideRoomBrowser') : t('showRoomBrowser')"
                                @click="toolbarActions.openRoomBrowser && toolbarActions.openRoomBrowser()"
                            >
                                <v-badge :content="roomCount" :model-value="roomCount > 0" color="accent" overlap>
                                    <v-icon size="20">mdi-view-list</v-icon>
                                </v-badge>
                            </v-btn>
                        </template>
                    </v-tooltip>

                    <v-divider vertical inset class="workspace-bar__sep"></v-divider>

                    <v-tooltip v-if="app.display.composerTheme" location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon size="small" variant="text" class="cc-press workspace-bar__icon" v-bind="props" :aria-label="t('toggleDarkMode')" @click="app.toggleDark()">
                                <v-icon size="20">{{ isDark ? 'mdi-white-balance-sunny' : 'mdi-weather-night' }}</v-icon>
                            </v-btn>
                        </template>
                        <span>{{ t('toggleDarkMode') }}</span>
                    </v-tooltip>

                    <v-tooltip location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon size="small" variant="text" class="cc-press workspace-bar__icon workspace-bar__clear" v-bind="props" :aria-label="t('clearClipboard')" @click="toolbarActions.openClearAll && toolbarActions.openClearAll()">
                                <v-icon size="20">mdi-broom</v-icon>
                            </v-btn>
                        </template>
                        <span>{{ t('clearClipboard') }}</span>
                    </v-tooltip>

                    <v-tooltip location="bottom">
                        <template v-slot:activator="{ props }">
                            <v-btn icon size="small" variant="text" class="cc-press workspace-bar__icon" v-bind="props" :aria-label="t('settings')" @click="toolbarActions.openSettings && toolbarActions.openSettings()">
                                <v-icon size="20">mdi-cog-outline</v-icon>
                            </v-btn>
                        </template>
                        <span>{{ t('settings') }}</span>
                    </v-tooltip>
                </div>
            </div>

                        <!-- 工作区：DOM 里顺序恒为「输入区 → 历史消息」，
                 布局只靠 CSS 的 order / grid 摆位 ——
                 三套布局共用同一份 DOM，切布局不会重建组件、也不会丢输入内容。 -->
            <div class="workspace" :class="`workspace--${app.homeLayout}`">
                <div class="workspace__composer">
                    <v-card v-if="!app.composerFullyHidden" class="composer-dock composer-dock--top mb-2 cc-anim-in" :class="{ 'surface-card--dark': isDark }" variant="outlined">
                        <unified-composer ref="composer"></unified-composer>
                    </v-card>
                </div>


            <!-- 搜索条独立成一块，不塞在时间流卡片里：
                 它是「浏览这一屏」的工具，和时间流本身是两件事；
                 挤在卡片里也会跟着卡片的内边距一起缩进，看着像时间流的一部分。 -->
                <div class="workspace__history">
                    <div v-if="app.received.length && showTimelineSearch" class="timeline-search">
                        <v-text-field
                            :model-value="app.searchQuery"
                            density="compact"
                            variant="solo"
                                    rounded="pill"
                            flat
                            hide-details
                            clearable
                            prepend-inner-icon="mdi-magnify"
                            :placeholder="t('searchPlaceholder')"
                            @update:model-value="app.setSearchQuery"
                        ></v-text-field>
                    </div>

                    <v-card class="timeline-panel" :class="{ 'surface-card--dark': isDark }" variant="outlined">
                        <div class="timeline-panel__body">
                            <!-- 分类条：只在真有内容时出现（空列表上摆一条没用的过滤条更碍事） -->
                            <div v-if="app.received.length && app.display.timelineFilter" class="timeline-panel__filters">
                                <v-chip
                                    v-for="opt in FILTER_OPTIONS"
                                    :key="opt.key"
                                    size="small"
                                    label
                                    class="timeline-panel__filter cc-press"
                                    :variant="timelineFilter === opt.key ? 'flat' : 'outlined'"
                                    :color="timelineFilter === opt.key ? 'primary' : undefined"
                                    :aria-label="t(opt.labelKey)"
                                    @click="setTimelineFilter(opt.key)"
                                >
                                    <!-- 窄屏只留图标：六个带字的分类在手机上会折成两行。
                                         文字藏掉后图标要自己居中，所以间距交给 CSS 管（不用 `start`）。 -->
                                    <v-icon size="16" class="timeline-panel__filter-icon">{{ opt.icon }}</v-icon>
                                    <span class="timeline-panel__filter-label">{{ t(opt.labelKey) }}</span>
                                </v-chip>
                            </div>

                            <!-- 宫格 / 聊天式下把「已存 N/M」从首条的浮层挪到条上：
                                 浮层是绝对定位在第一条上的，宫格里会和第一格内容叠在一起，
                                 聊天式里首条是最旧那条、浮层会跟着掉到最下面。 -->
                            <div v-if="app.received.length" class="timeline-panel__grid-head">
                                <v-chip
                                    size="x-small"
                                    variant="outlined"
                                    color="primary"
                                    class="timeline-panel__count-chip"
                                >{{ historyUsageLabel }}</v-chip>
                            </div>

                            <div
                                v-if="app.received.length"
                                class="timeline-panel__stream"
                                :class="{ 'timeline-panel__stream--grid': isGrid }"
                            >
                                <div
                                    v-for="(item, index) in orderedReceived"
                                    :key="item.id"
                                    class="timeline-panel__item cc-anim-in"
                                    :style="{ '--cc-stagger': `${Math.min(index, 12) * 26}ms` }"
                                >
                                    <component
                                        :is="item.type === 'text' ? ReceivedText : ReceivedFile"
                                        :meta="item"
                                        :grid="isGrid"
                                    />
                                </div>
                                <div
                                    v-if="!filteredReceived.length"
                                    class="text-center text-caption text-medium-emphasis py-6"
                                >{{ t('filterEmpty') }}</div>
                            </div>

                            <v-sheet
                                v-if="!app.received.length"
                                class="empty-timeline py-10 px-6 text-center"
                                :class="{ 'empty-timeline--dark': isDark }"
                                rounded="lg"
                                color="transparent"
                            >
                                <v-icon size="42" color="primary">{{ mdiTimeline }}</v-icon>
                                <div class="text-h6 font-weight-medium mt-4 mb-2">{{ t('emptyTimelineTitle') }}</div>
                                <div class="text-body-2 text-medium-emphasis mb-4">{{ t('timelineEmptySubtitle') }}</div>
                                <v-btn size="small" variant="flat" color="primary" @click="focusComposer('text')">
                                    {{ t('quickSend') }}
                                </v-btn>
                            </v-sheet>

                            <div v-else-if="filteredReceived.length" class="text-center text-caption text-medium-emphasis pt-2">{{ t('alreadyAtBottom') }}</div>
                        </div>
                    </v-card>
                </div>
            </div>
        </div>

        </v-container>
    </div>
</template>

<style scoped>
.home-minimal {
    background: transparent;
    min-height: 100vh;
    min-height: 100dvh;
}

/* 工作区四周留白：**上下左右同一个值**。
   原来是 `px-3 px-md-5 pb-3 pb-md-5` + `padding-top: 8px` —— 左右 16/24px、
   上下只有 8px，卡片贴顶比贴边紧，看起来就是「左右的 margin 比上下大」。
   现在统一走 --cc-gutter，只在这个变量里改一处。 */
.home-minimal__body {
    padding: var(--cc-gutter, 16px);
}

.home-minimal--dark {
    color: rgba(226, 232, 240, 0.96);
}

.home-minimal__shell {
    max-width: 980px;
}

/* 左右布局要更宽的画布：980px 劈成两栏，每栏只有 470px，历史卡片会挤成竖条。 */
.home-minimal--split .home-minimal__shell {
    max-width: 1280px;
}

/* ── 工作区条 ───────────────────────────────────────────────── */
.workspace-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 6px 12px;
    /* 与下面两块面板的间距：**同一个令牌**（别再 8px / 12px / 16px 混着用） */
    margin-bottom: var(--cc-panel-gap, 12px);
    padding: 5px 10px;
    border-radius: var(--cc-radius-pill, 999px);
    border: 1px solid var(--cc-glass-border, rgba(148, 163, 184, 0.26));
    background: var(--cc-glass-bg, rgba(255, 255, 255, 0.62));
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    box-shadow: var(--cc-shadow-1, 0 2px 10px rgba(15, 23, 42, 0.06));
}

.workspace-bar__group {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
}

.workspace-bar__group--end {
    margin-left: auto;
}

/* 图标按钮统一 30px：默认的 40px 圆比胶囊(约 30px)大一圈，摆在一起像没对齐。 */
.workspace-bar__icon {
    width: 30px !important;
    height: 30px !important;
    min-width: 30px !important;
    min-height: 30px !important;
}

.workspace-bar__icon--active {
    color: rgb(var(--v-theme-primary));
    background: rgba(var(--v-theme-primary), 0.12);
}

/* 清空是破坏性动作：平时不显眼，悬停才变红（同消息卡片那把扫帚的既有语言）。 */
.workspace-bar__clear:hover {
    color: rgb(var(--v-theme-error));
    background: rgba(var(--v-theme-error), 0.12);
}

/* 房间 chip：可点（开二维码），延迟数字用等宽字体避免跳动。 */
.workspace-bar__room {
    cursor: pointer;
    max-width: 260px;
}

.workspace-bar__latency {
    font-family: var(--cc-font-mono, ui-monospace, monospace);
    font-size: 0.72rem;
    margin-inline-start: 6px;
}

/* 三个下拉（模式 / 布局 / 展示方式）：同一个槽位大小，视觉上是一组。 */
.workspace-bar__select {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 30px;
    padding: 0 9px;
    border: 1px solid transparent;
    border-radius: var(--cc-radius-pill, 999px);
    background: transparent;
    color: inherit;
    font-size: 0.78rem;
    font-weight: 600;
    line-height: 1;
    cursor: pointer;
    white-space: nowrap;
    transition: background-color var(--cc-dur-fast, 0.14s) var(--cc-ease, ease),
                border-color var(--cc-dur-fast, 0.14s) var(--cc-ease, ease);
}

.workspace-bar__select:hover {
    background: rgba(var(--v-theme-primary), 0.10);
    border-color: rgba(var(--v-theme-primary), 0.28);
}

.workspace-bar__select-icon {
    opacity: 0.85;
}

.workspace-bar__select-label {
    max-width: 9em;
    overflow: hidden;
    text-overflow: ellipsis;
}

.workspace-bar__sep {
    height: 20px;
    opacity: 0.5;
}

/* 窄容器（左右布局 / 手机）下收起文字，只留下拉箭头 —— 标题/aria-label 仍在。 */
@media (max-width: 719px) {
    .workspace-bar__select-label {
        display: none;
    }

    .workspace-bar {
        justify-content: center;
        gap: 6px;
        padding: 4px 8px;
    }

    .workspace-bar__group--end {
        margin-left: 0;
    }

    .workspace-bar__room {
        max-width: 150px;
    }
}

/* ── 工作区三套布局 ─────────────────────────────────────────────
   DOM 顺序恒为 [输入区][历史消息]，布局只改摆位方式。
   这样切布局是纯 CSS 的事，不会重建 UnifiedComposer（重建会丢光标和草稿）。 */
.workspace {
    display: flex;
    flex-direction: column;
    gap: 0;
    min-width: 0;
}

.workspace__composer,
.workspace__history {
    min-width: 0;
}

/* 上下布局（默认）：输入区在上、历史在下 —— 也就是原来的长相，一行不改。
   两块面板的间距走 --cc-panel-gap（与工作区条到输入区的间距一致）。 */
.workspace--stack {
    gap: var(--cc-panel-gap, 12px);
}

.workspace--stack .workspace__composer {
    order: 1;
}

.workspace--stack .workspace__history {
    order: 2;
}

/* 左右布局：输入区在左、历史在右。 */
.workspace--split {
    display: grid;
    grid-template-columns: minmax(320px, 420px) minmax(0, 1fr);
    /* ⚠️ 用 stretch（grid 默认）而不是 start：左右两栏要**等高**，
       否则左栏输入区只有内容那么高，左下角一大片空着（用户报的问题）。 */
    align-items: stretch;
    gap: var(--cc-panel-gap, 12px);
}

.workspace--split .workspace__composer {
    grid-column: 1;
    grid-row: 1;
}

.workspace--split .workspace__history {
    grid-column: 2;
    grid-row: 1;
}

/* 左右布局下输入区仍然粘顶（左栏跟着右栏一起滚），偏移量与上下布局一致，
   由下面的 .composer-dock--top 统一定义 —— 这里不再重复写一遍。 */

/* 聊天式：历史在上、输入区在下。
   输入区**粘在视口底部**（sticky，不是 fixed）——
   sticky 仍然在文档流里占位，所以最后一条历史不会被输入区永久盖住；
   固定定位就得靠 padding 猜高度，猜错就永远有一条看不全。 */
.workspace--chat .workspace__history {
    order: 1;
}

.workspace--chat .workspace__composer {
    order: 2;
    position: sticky;
    bottom: var(--cc-gap, 8px);
    z-index: 3;
    padding-top: var(--cc-gap, 8px);
    margin-top: var(--cc-gap, 8px);
}

/* 输入区在聊天式里是**浮在历史消息之上**的一层，必须一眼看出它在上层。
   普通卡片阴影（--cc-shadow-2）在这个位置上不够 —— 深色模式下尤其糊，
   所以单独用「浮起」那一档，再补一圈浅色描边把轮廓勾出来。 */
.workspace--chat .workspace__composer :deep(.unified-composer) {
    box-shadow: var(--cc-shadow-float, 0 18px 48px rgba(15, 23, 42, 0.22));
    border-color: var(--cc-glass-highlight, rgba(255, 255, 255, 0.7)) !important;
}

/* 外层已经负责「粘底」了，内层卡片必须退回静态 ——
   两层 sticky 叠在一起时，内层的 `top` 会在往上滚时把卡片顶到工具栏下面去。 */
.workspace--chat .composer-dock--top {
    position: static;
    top: auto;
    margin-bottom: 0;
}

/* 窄屏：左右布局没有「左右」可言，退回上下（跟 JS 的 effectiveLayout 同一条规则）。 */
@media (max-width: 960px) {
    .workspace--split {
        display: flex;
        flex-direction: column;
        gap: 0;
    }

    .workspace--split .workspace__composer {
        order: 1;
    }

    .workspace--split .workspace__history {
        order: 2;
    }
}

/* ── 历史面板与卡片 ─────────────────────────────────────────── */
.surface-card--dark,
.timeline-panel,
.composer-dock {
    border-radius: var(--cc-radius-xl, 28px);
    border-color: var(--cc-glass-border, rgba(148, 163, 184, 0.26)) !important;
    box-shadow: var(--cc-shadow-2, 0 10px 26px rgba(15, 23, 42, 0.09));
    background: var(--cc-glass-bg-strong, rgba(255, 255, 255, 0.78));
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    transition: background-color var(--cc-dur, 0.22s) var(--cc-ease, ease),
                border-color var(--cc-dur, 0.22s) var(--cc-ease, ease),
                box-shadow var(--cc-dur, 0.22s) var(--cc-ease, ease);
}

.surface-card--dark {
    border-color: var(--cc-glass-border, rgba(71, 85, 105, 0.72)) !important;
    background: var(--cc-glass-bg-strong, rgba(15, 23, 42, 0.76));
}

.timeline-panel {
    overflow: hidden;
}

/* 面板内容区的内缩**四边同一个值**。
   原来是 `px-3 px-md-4 py-2`：左右 16px、上下 8px —— 首条卡片贴顶比贴边紧，
   而且内外圆角不可能同心（同心要求 内R = 外R − 内缩，内缩不一致就没有解）。 */
.timeline-panel__body {
    min-height: 24rem;
    padding: var(--cc-frame-inset-lg, 12px);
    /* 卡片圆角跟着内缩一起算，保证与外框同心 */
    --cc-card-radius: calc(var(--cc-radius-xl, 28px) - var(--cc-frame-inset-lg, 12px));
}

/* 分类条（全部 / 文本 / 图片 / 文件）。默认关，见 data/displayToggles.js。 */
/* 搜索条：独立在时间流卡片之外的一块。默认关，见 displayToggles 的 timelineSearch。
   限宽居中 —— 搜索框不需要占满整行，也跟时间流的宽度拉开区分。 */
.timeline-search {
    max-width: 420px;
    margin: 0 auto var(--cc-panel-gap, 12px);
}
/* 搜索框底色跟着模式走。各模式没有统一的颜色 token（终端有 --tw-*，便签/聊天是写死的），
   所以用 currentColor 混一层浅底：深色模式文字浅 → 得到浅底；浅色模式文字深 → 得到深一点的底。
   一处规则适配六套皮肤，不用每套各写一份。 */
.timeline-search :deep(.v-field) {
    background: color-mix(in srgb, currentColor 8%, transparent);
    /* ⚠️ `color: inherit` 必须加在 .v-field 上，不能只加在 .v-field__input 上：
       color-mix 里的 currentColor 取的是**元素自己**的颜色。只改 input 的话，
       .v-field 仍是 Vuetify 给的颜色，于是便签/终端这类自带皮肤的模式的底色
       全都算成黑色 —— 看着像「没适配」。 */
    color: inherit;
}

.timeline-search :deep(.v-field__input),
.timeline-search :deep(.v-field__prepend-inner .v-icon),
.timeline-search :deep(.v-field__clearable .v-icon) {
    color: inherit;
}


.timeline-panel__filters {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 6px;
    padding: 2px 0 10px;
}

.timeline-panel__filter {
    cursor: pointer;
}

.timeline-panel__filter-icon {
    margin-inline-end: 6px;
}

/* 窄屏只留图标：六个带字的分类在手机上会折成两行，白白占掉一条横条的高度。
   图标本身认得出来（全部 / 文本 / 图片 / 文件 / 任务列表 / 表格），
   文案靠 chip 上的 aria-label 保住可访问性。 */
@media (max-width: 768px) {
    .timeline-panel__filter-label {
        display: none;
    }

    .timeline-panel__filter-icon {
        margin-inline-end: 0;
    }
}

.timeline-panel__stream {
    position: relative;
}

.timeline-panel__item {
    position: relative;
    /* 入场动画的错峰延迟：卡片依次浮现而不是整体闪一下。
       --cc-stagger 由模板按序号算（超过 12 条就不再累加，否则第 200 条要等 5 秒）。 */
    animation-delay: var(--cc-stagger, 0ms);
}

.timeline-panel__count-chip {
    height: 22px;
    padding: 0 6px;
    border-radius: 999px;
}

.home-minimal--dark /* 「已存 N/M」计数条：改成**恒定显示在列表上方**，不再做成首条上的绝对定位浮层 ——
   浮层要给首条额外补 padding-top 才能让出位置，那个 padding 正是「上下内缩 ≠ 左右内缩」的来源。 */
.timeline-panel__grid-head {
    display: flex;
    justify-content: center;
    padding: 0 0 var(--cc-frame-inset-lg, 12px);
}

/* ── 宫格展示 ─────────────────────────────────────────────────
   auto-fill + minmax：宽度够就多排几列，窄屏自动退成一列，
   不需要为每个断点各写一份列数。 */
.timeline-panel__stream--grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(clamp(220px, 26vw, 300px), 1fr));
    gap: 12px;
    align-items: stretch;
    padding-top: 4px;
}

.timeline-panel__stream--grid .timeline-panel__item {
    display: flex;
    min-width: 0;
    padding-top: 0;
}

/* 卡片自己带 mb-3（一行一张时的行距）。宫格里行距由 gap 管，
   留着 mb-3 会让每格底部多一截空白、且高度对不齐。 */
.timeline-panel__stream :deep(.timeline-card) {
    /* 与历史面板同心（面板圆角 − 内容区内缩），见 .timeline-panel__body */
    border-radius: var(--cc-card-radius, var(--cc-radius-lg, 22px));
}

.timeline-panel__stream--grid :deep(.timeline-card) {
    margin-bottom: 0 !important;
    width: 100%;
    height: 100%;
    display: flex;
    flex-direction: column;
}

.timeline-panel__stream--grid :deep(.timeline-card > .v-card-text) {
    flex: 1 1 auto;
    min-width: 0;
}

/* 宫格里每格都窄，16px 24px 的内边距会把内容挤没 */
.timeline-panel__stream--grid :deep(.timeline-card .v-card-text) {
    padding: 12px 14px 14px;
}

@media (max-width: 600px) {
    .timeline-panel__stream--grid {
        grid-template-columns: 1fr;
    }
}

.empty-timeline {
    border: 1px dashed rgba(148, 163, 184, 0.35);
    background: rgba(248, 250, 252, 0.5) !important;
}

.empty-timeline--dark {
    border-color: rgba(71, 85, 105, 0.72);
    background: rgba(15, 23, 42, 0.35) !important;
}

/* 左右布局：整个工作区**约束在视口内**，两栏等高铺满。
   高度不再对着 toolbar-h 做 calc —— 标准模式的顶栏已经删了，
   而且对着固定值算，窗口一缩就会溢出屏幕（用户报的「超出屏幕外侧边缘」）。
   做法：body 自己限高（视口 − 上下留白），工作区用 flex 吃掉剩下的高度；
   工作区条是它的兄弟、按内容占位，于是工作区拿到的高度永远刚刚好、随窗口自适应。 */
.home-minimal__body--split {
    height: calc(100dvh - var(--cc-gutter, 16px) * 2);
    display: flex;
    flex-direction: column;
    overflow: hidden;
}

/* ⚠️ 工作区不是 body 的直接子元素 —— 中间还隔着一层 .home-minimal__shell。
   flex 链要一层层接下去，断在 shell 这里的话工作区拿不到剩余高度（实测只有 483px）。 */
.home-minimal__body--split .home-minimal__shell {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    width: 100%;
}

/* ⚠️ 工作区不是 body 的直接子元素 —— 中间还隔着一层 .home-minimal__shell。
   flex 链要一层层接下去，断在 shell 这里的话工作区拿不到剩余高度（实测只有 483px）。 */
.home-minimal__body--split .home-minimal__shell {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
    width: 100%;
}

.home-minimal__body--split .workspace {
    flex: 1 1 auto;
    min-height: 0;
    height: auto;
}

/* 历史一栏在自身内部滚动：内容再多也不把整版撑出屏幕。 */
.home-minimal__body--split .workspace__history {
    overflow-y: auto;
    min-height: 0;
}

/* 左栏（输入区）与右栏等高：外壳吃满格子，内层通过变量吃满外壳。
   ⚠️ 给内层传高度必须走**变量** —— Vuetify 组件的根元素不带父组件的 scoped 属性，
   `.workspace--split .unified-composer { … }` 这条选择器匹配不上（实测踩过）。 */
.workspace--split .workspace__composer {
    height: 100%;
}

.workspace--split .composer-dock {
    height: 100%;
    margin-bottom: 0;
    --cc-composer-height: 100%;
    --cc-composer-max-height: 100%;
    /* 把卡内的剩余高度分给输入框（见 UnifiedComposer 的高度分配段）：
       inputs 吃掉 body 的剩余 → textblock 吃掉 inputs 的剩余 → 输入框吃掉 textblock 的剩余 */
    --cc-composer-inputs-flex: 1 1 auto;
    --cc-composer-inputs-min: 0;
    --cc-composer-textblock-flex: 1 1 auto;
    --cc-composer-textblock-min: 0;
    --cc-composer-textarea-flex: 1 1 auto;
    --cc-composer-textarea-min: 3rem;
}

/* 输入区是**双框**（玻璃外壳 + 内层卡片）。要让两个 R 角平行，必须同时满足：
     1. 外壳内缩**四边相等**（--cc-frame-inset）；
     2. 内层圆角 = 外壳圆角 − 内缩。
   这里用 CSS 变量把 (2) 传给内层卡片 —— 内层样式读 `--cc-composer-radius`，
   变量继承，不需要在外层写 :deep() 抢权重（同权重靠源码顺序决胜负，太脆）。
   ⚠️ 内缩和变量只加在**这一条**上，上面的共享玻璃规则里不能加：
      那条同时管着 .timeline-panel，给它加 padding 会连带把历史面板也缩一圈。 */
.composer-dock {
    padding: var(--cc-frame-inset, 8px);
    --cc-composer-radius: var(--cc-radius-frame-inner, 20px);
}

.composer-dock--top {
    position: sticky;
    /* ⚠️ 让开顶部工具栏。工具栏是 sticky top:0（z-index 40），输入区只是 z-index 2，
       写成 top:0.5rem 的话滚动时输入区会**钻到工具栏底下**，顶部那行（全屏按钮）被压住。
       用 --cc-toolbar-h（theme.css）而不是写死像素：两处必须同步。 */
    top: calc(var(--cc-toolbar-h, 52px) + var(--cc-gap, 8px));
    z-index: 2;
    box-shadow: var(--cc-shadow-2, 0 10px 26px rgba(15, 23, 42, 0.09));
}

@media (max-width: 960px) {
    .home-minimal {
        min-height: calc(100vh - 56px);
        min-height: calc(100dvh - 56px);
    }

    .timeline-panel__stream {
        padding-left: 0;
    }

    }
</style>
