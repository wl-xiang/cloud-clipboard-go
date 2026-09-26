<template>
    <v-card
        class="unified-composer"
        :class="{ 'unified-composer--dark': isDark, 'unified-composer--dragover': dragover }"
        variant="outlined"
        @dragenter.prevent="dragover = true"
        @dragover.prevent="dragover = true"
        @dragleave.prevent="handleDragLeave"
        @drop.prevent="handleDrop"
    >
        <div class="unified-composer__body">
            <div
                class="unified-composer__inputs"
                :class="{ 'unified-composer__inputs--files-first': isFilePrimary }"
            >
                <div v-if="app.display.composerText" class="unified-composer__textblock">
                    <!-- `/` 模板菜单：行首打 `/` 弹出。放在文本区**上方**、走正常流 ——
                         这块是底部停靠的，多出来的高度往上长，输入框位置不动。
                         不用绝对定位：浮动元素一旦祖先有 overflow 就会被静默裁掉（踩过）。 -->
                    <composer-slash-menu v-if="slashMenu" :items="SLASH_TEMPLATES" @pick="insertSlashTemplate" />
                    <v-btn
                        icon
                        size="small"
                        density="comfortable"
                        variant="text"
                        color="grey-darken-1"
                        class="unified-composer__fullscreen-btn"
                        @click="toggleTextFullscreen"
                    >
                        <v-icon>{{ textFullscreen ? mdiFullscreenExit : mdiFullscreen }}</v-icon>
                    </v-btn>
                    <v-textarea
                        ref="textarea"
                        :key="app.composerPrimary"
                        v-model="app.send.text"
                        variant="solo"
                        flat
                        density="compact"
                        auto-grow
                        :rows="composerRows"
                        :max-rows="composerMaxRows"
                        :placeholder="textareaPlaceholder"
                        hide-details
                        class="unified-composer__textarea"
                        :class="{ 'unified-composer__textarea--secondary': isFilePrimary }"
                        @keydown.ctrl.enter.prevent="onSendShortcut"
                        @keydown.meta.enter.prevent="onSendShortcut"
                        @keydown="onTextareaKeydown"
                        @input="onTextareaInput"
                        @compositionend="onTextareaInput"
                    ></v-textarea>
                </div>

                <div v-if="app.display.composerText && app.display.composerUpload" class="unified-composer__divider">
                    <div class="unified-composer__divider-line"></div>
                    <span class="unified-composer__limit text-caption text-medium-emphasis">{{ textLimitLabel }}</span>
                    <div class="unified-composer__divider-line unified-composer__divider-line--short"></div>
                    <v-tooltip v-if="app.display.composerSwap" location="top">
                        <template v-slot:activator="{ props }">
                            <v-btn
                                icon
                                size="small"
                                density="comfortable"
                                variant="text"
                                class="unified-composer__swapbtn"
                                v-bind="props"
                                @click="app.toggleComposerPrimary"
                            >
                                <v-icon>{{ mdiSwapVertical }}</v-icon>
                            </v-btn>
                        </template>
                        <span>{{ isFilePrimary ? t('textIsPrimaryTip') : t('fileIsPrimaryTip') }}</span>
                    </v-tooltip>
                    <div class="unified-composer__divider-line unified-composer__divider-line--short"></div>
                    <span class="unified-composer__limit text-caption text-medium-emphasis">{{ fileLimitLabel }}</span>
                    <div class="unified-composer__divider-line"></div>
                </div>

                <div v-if="app.display.composerUpload" class="unified-composer__fileblock">
                    <div
                        class="unified-composer__dropzone"
                        :class="{ 'unified-composer__dropzone--primary': isFilePrimary }"
                        @click="openFilePicker"
                    >
                        <v-icon :size="isFilePrimary ? 40 : 20" class="mr-2">{{ mdiCloudUpload }}</v-icon>
                        <span>{{ t(mobile ? 'addFilesShort' : 'addFiles', { keys: pasteKey }) }}</span>
                    </div>
                    <div v-if="app.send.files.length" class="unified-composer__attachments px-1 pt-2">
                        <v-chip
                            v-for="(file, index) in app.send.files"
                            :key="file.name + file.size + index"
                            closable
                            :variant="'outlined'"
                            size="small"
                            class="mr-2 mb-2"
                            @click:close="removeFile(index)"
                        >
                            {{ file.name }} · {{ prettyFileSize(file.size) }}
                        </v-chip>
                    </div>
                </div>
            </div>

            <div v-if="progress" class="unified-composer__progress">
                <small class="d-block text-right text-medium-emphasis mb-1">
                    {{ prettyFileSize(Math.min(uploadedSize, fileSize)) }} / {{ prettyFileSize(fileSize) }}
                </small>
                <v-progress-linear :value="uploadProgress * 100"></v-progress-linear>
            </div>
        </div>

        <div class="unified-composer__footer pt-1">
                <div class="unified-composer__footer-icons">
                    <v-tooltip v-if="app.display.composerDevice" location="top">
                        <template v-slot:activator="{ props }">
                            <v-btn
                                variant="text"
                                size="small"
                                density="comfortable"
                                color="grey-darken-1"
                                v-bind="props"
                                class="unified-composer__device"
                                @click="goDeviceList"
                            >
                                <span class="unified-composer__device-full">
                                    <span class="unified-composer__devicestat"><v-icon size="small" class="mr-1">{{ mdiLaptop }}</v-icon>{{ deviceStats.desktop }}</span>
                                    <span class="unified-composer__devicestat"><v-icon size="small" class="mr-1">{{ mdiCellphone }}</v-icon>{{ deviceStats.mobile }}</span>
                                    <span class="unified-composer__devicestat"><v-icon size="small" class="mr-1">{{ mdiDevices }}</v-icon>{{ deviceStats.other }}</span>
                                </span>
                            </v-btn>
                        </template>
                        <span>{{ t('connectedTotal', { count: deviceTotal }) }}</span>
                    </v-tooltip>
                    <v-tooltip v-if="app.display.composerPalette" location="top">
                        <template v-slot:activator="{ props }">
                            <v-btn
                                icon
                                density="comfortable"
                                variant="text"
                                size="small"
                                color="grey-darken-1"
                                v-bind="props"
                                @click="colorDialog = true"
                            >
                                <v-icon>{{ mdiPaletteSwatch }}</v-icon>
                            </v-btn>
                        </template>
                        <span>{{ t('traditionalColors') }}</span>
                    </v-tooltip>
                </div>

                <v-btn
                    variant="flat"
                    color="primary"
                    rounded="pill"
                    class="unified-composer__send"
                    :disabled="sendDisabled"
                    @click="sendAll"
                 v-if="canSend">
                    <v-icon start size="small">{{ mdiSend }}</v-icon>
                    {{ t('send') }}
                </v-btn>
            </div>

        <input
            ref="selectFile"
            type="file"
            class="d-none"
            multiple
            @change="handleSelectFiles(Array.from($event.target.files))"
        >
    </v-card>

    <v-dialog v-model="deviceDialog" max-width="480" scrollable>
        <v-card>
            <v-card-title class="d-flex align-center">
                <v-icon start>{{ mdiDevices }}</v-icon>
                {{ t('connectedDevices') }}
                <v-spacer></v-spacer>
                <v-btn icon variant="text" @click="deviceDialog = false">
                    <v-icon>{{ mdiClose }}</v-icon>
                </v-btn>
            </v-card-title>
            <v-divider></v-divider>
            <v-card-text class="pa-2">
                <template v-if="!ws.websocket">
                    <p class="pa-2 text-medium-emphasis">{{ t('notConnectedToServer') }}</p>
                </template>
                <template v-else-if="app.device.length === 0">
                    <p class="pa-2 text-medium-emphasis">{{ t('noDevicesConnected') }}</p>
                </template>
                <template v-else>
                    <p class="pa-2 text-caption text-medium-emphasis">
                        {{ t('devicesConnected', { count: app.device.length, desktop: desktopDeviceCount, mobile: mobileDeviceCount }) }}
                    </p>
                    <v-list rounded two-line density="compact">
                        <v-list-item v-for="item in app.device" :key="item.id">
                            <template v-slot:prepend>
                                <v-icon v-if="item.type === 'desktop' && item.os.split(' ').shift() === 'Windows'">{{mdiMicrosoftWindows}}</v-icon>
                                <v-icon v-else-if="item.type === 'desktop' && item.os.split(' ').shift() === 'GNU/Linux'">{{mdiLinux}}</v-icon>
                                <v-icon v-else-if="item.type === 'desktop' && item.os.split(' ').shift() === 'Mac'">{{mdiApple}}</v-icon>
                                <v-icon v-else-if="item.type === 'desktop'">{{mdiLaptop}}</v-icon>
                                <v-icon v-else-if="(item.type === 'smartphone' || item.type === 'mobile' || item.type === 'tablet') && item.os.split(' ').shift() === 'Android'">{{mdiAndroid}}</v-icon>
                                <v-icon v-else-if="(item.type === 'smartphone' || item.type === 'mobile' || item.type === 'tablet') && item.os.split(' ').shift() === 'iOS'">{{mdiAppleIos}}</v-icon>
                                <v-icon v-else-if="item.type === 'smartphone' || item.type === 'mobile' || item.type === 'tablet'">{{mdiTabletCellphone}}</v-icon>
                                <v-icon v-else>{{mdiDevices}}</v-icon>
                            </template>
                            <v-list-item-title>{{ item.name || deviceTypeLabel(item) }}</v-list-item-title>
                            <v-list-item-subtitle>{{item.os}} ({{item.browser}})</v-list-item-subtitle>
                        </v-list-item>
                    </v-list>
                </template>
            </v-card-text>
        </v-card>
    </v-dialog>

    <v-dialog v-model="textFullscreen" fullscreen>
        <v-card class="d-flex flex-column fill-height pts-fullscreen-card">
            <v-toolbar elevation="1">
                <v-btn icon variant="text" @click="textFullscreen = false">
                    <v-icon>{{ mdiArrowLeft }}</v-icon>
                </v-btn>
                <v-toolbar-title>{{ t('enterTextToSend') }}</v-toolbar-title>
                <v-spacer></v-spacer>
                <v-tooltip location="bottom">
                    <template v-slot:activator="{ props }">
                        <v-btn
                            icon
                            variant="text"
                            v-bind="props"
                            :color="app.fullscreenSendClose ? 'primary' : 'grey-darken-1'"
                            @click="app.toggleFullscreenSendClose"
                        >
                            <v-icon>{{ app.fullscreenSendClose ? mdiChevronDownCircle : mdiWindowRestore }}</v-icon>
                        </v-btn>
                    </template>
                    <span>{{ app.fullscreenSendClose ? t('fullscreenCloseAfterSendOn') : t('fullscreenCloseAfterSendOff') }}</span>
                </v-tooltip>
                <v-btn
                    variant="flat"
                    color="primary"
                    :disabled="sendDisabled"
                    @click="sendAll"
                 v-if="canSend">
                    <v-icon start size="small">{{ mdiSend }}</v-icon>
                    {{ t('send') }}
                </v-btn>
            </v-toolbar>
            <div class="pts-fullscreen-body flex-grow-1">
                <composer-slash-menu v-if="slashMenu" :items="SLASH_TEMPLATES" @pick="insertSlashTemplate" />
                <v-textarea
                    v-model="app.send.text"
                    variant="solo"
                    flat
                    hide-details
                    no-resize
                    class="pts-fullscreen-textarea"
                    :placeholder="textareaPlaceholder"
                    @keydown.ctrl.enter.prevent="onSendShortcut"
                    @keydown.meta.enter.prevent="onSendShortcut"
                    @keydown="onTextareaKeydown"
                    @input="onTextareaInput"
                    @compositionend="onTextareaInput"
                ></v-textarea>
                <small class="d-flex justify-center pa-2 text-medium-emphasis">{{ textLimitLabel }}</small>
            </div>
        </v-card>
    </v-dialog>


    <traditional-color-dialog v-model="colorDialog"></traditional-color-dialog>
</template>

<script setup>import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';
import { useAppStore } from '@/store/app';
import { useWebSocketStore } from '@/store/websocket';
import { useDisplay } from 'vuetify';
import { useTheme } from 'vuetify';
import { useI18n } from 'vue-i18n';
import axios from 'axios';
import { toast } from '@/plugins/toast';
import { errorMessage, prettyFileSize } from '@/util.js';
import TraditionalColorDialog from '@/components/TraditionalColorDialog.vue';
import ComposerSlashMenu from '@/components/ComposerSlashMenu.vue';
import { SLASH_TEMPLATES, resolveSlashText, slashMenuShouldOpen, slashMenuShouldStay, slashPendingAt, stripTrailingSlash } from '@/slash-template.js';

const mdiPalette = 'mdi-palette';
const mdiPaletteSwatch = 'mdi-palette-swatch';
const mdiSend = 'mdi-send';
const mdiLaptop = 'mdi-laptop';
const mdiCellphone = 'mdi-cellphone';
const mdiDevices = 'mdi-devices';
const mdiClose = 'mdi-close';
const mdiAndroid = 'mdi-android';
const mdiApple = 'mdi-apple';
const mdiAppleIos = 'mdi-apple-ios';
const mdiLinux = 'mdi-linux';
const mdiMicrosoftWindows = 'mdi-microsoft-windows';
const mdiTabletCellphone = 'mdi-tablet-cellphone';
const mdiSwapVertical = 'mdi-swap-vertical';
const mdiCloudUpload = 'mdi-cloud-upload-outline';
const mdiFullscreen = 'mdi-fullscreen';
const mdiFullscreenExit = 'mdi-fullscreen-exit';
const mdiArrowLeft = 'mdi-arrow-left';
const mdiChevronDownCircle = 'mdi-chevron-down-circle';
const mdiWindowRestore = 'mdi-window-restore';


const app = useAppStore();
const ws = useWebSocketStore();
const theme = useTheme();
const isDark = computed(() => theme.current.value?.dark ?? false);
const { t } = useI18n();
const { mobile } = useDisplay();
const isFilePrimary = computed(() => app.composerPrimary === 'files');
// 输入框起始行数 / 最多长到几行。
//
// 为什么要有 `max-rows`：文本上限已放宽到 9000，一屏贴进来的多行文本会很长。
// 让输入框跟着内容长（auto-grow）是好的 —— 否则「支持换行」这个能力看不见；
// 但**必须封顶**，否则它会一路长到把同一张卡片底部的发送按钮顶出视口，
// 那正是「发送按钮被隐藏」的成因。
// 封顶后超出部分在框内滚动；同时卡片整体还有视口上限兜底（见 .unified-composer）。
const composerRows = computed(() => isFilePrimary.value ? 1 : 3);
const composerMaxRows = computed(() => isFilePrimary.value ? 3 : 8);
const deviceDialog = ref(false);
const colorDialog = ref(false);
const textFullscreen = ref(false);
function toggleTextFullscreen() {
    textFullscreen.value = !textFullscreen.value;
}
const deviceStats = computed(() => {
    const list = app.device || [];
    const desktop = list.filter(d => d.type === 'desktop').length;
    const mobile = list.filter(d => d.type === 'smartphone' || d.type === 'mobile' || d.type === 'tablet').length;
    const other = list.length - desktop - mobile;
    return { desktop, mobile, other };
});
const deviceTotal = computed(() => deviceStats.value.desktop + deviceStats.value.mobile + deviceStats.value.other);
const desktopDeviceCount = computed(() => app.device.filter(e => e.type === 'desktop').length);
const mobileDeviceCount = computed(() => app.device.filter(e => (e.type === 'smartphone' || e.type === 'tablet')).length);
// 设备没自报名字时的兜底标题（服务端对未声明的名字会 omitempty 掉，所以这条路径真的会走到）。
//
// ⚠️ 这个函数在模板里被调用，但一直**没有定义**。JS 的 || 短路让它只在
// 「有设备、且至少一台没名字」时才被求值 —— 那时整块列表渲染抛 TypeError，
// 弹窗直接挂不上，表现就是「点了没反应」，而控制台之外看不出任何异常。
function deviceTypeLabel(item) {
    if (item.type === 'desktop') {
        return t('desktopDevice');
    }
    if (item.type === 'smartphone' || item.type === 'mobile' || item.type === 'tablet') {
        return t('mobileDevice');
    }
    return t('otherDevice');
}
function goDeviceList() {
    deviceDialog.value = true;
}
defineExpose({ focus, openFilePicker });
const progress = ref(false);
const dragover = ref(false);
const uploadedSizes = ref([]);
const isMac = /mac|iphone|ipad|ipod/i.test(navigator.userAgent || '');
const textarea = ref(null);
const selectFile = ref(null);
const fileSize = computed(() => app.send.files.length ? app.send.files.reduce((acc, cur) => acc += cur.size, 0) : 0);
const uploadedSize = computed(() => uploadedSizes.value.length ? uploadedSizes.value.reduce((acc, cur) => acc += cur, 0) : 0);
const uploadProgress = computed(() => Math.min(fileSize.value !== 0 ? (uploadedSize.value / fileSize.value) : 0, 1));
const sendDisabled = computed(() => !ws.websocket || progress.value || (!app.send.text && !app.send.files.length) || app.send.text.length > app.config.text.limit);
const pasteKey = isMac ? '⌘+V' : 'Ctrl+V';
const sendShortcutLabel = computed(() => t('sendShortcutTip', {
    keys: isMac ? '⌘+Enter' : 'Ctrl+Enter',
}));
const textareaPlaceholder = computed(() => {
    // 「/」模板是这套输入区里最不容易被发现的功能，直接写在占位符里。
    const hint = t('composerSlashHint');
    return mobile.value ? hint : `${hint} ${sendShortcutLabel.value}`;
});
// 文本区和上传区都被关掉时，发送按钮没有任何东西可发 —— 藏起来，
// 而不是留一个点了没反应的按钮。（两边都关 = 这个模式只想接收。）
const canSend = computed(() => Boolean(app.display.composerText || app.display.composerUpload));

const textLimitLabel = computed(() => t('composerTextLimit', {
    current: app.send.text.length,
    limit: app.config.text.limit,
}));
const fileLimitLabel = computed(() => t('fileSizeLimit', {
    limit: prettyFileSize(app.config.file.limit),
}));
function focus(type) {
    if (type === 'file') {
        openFilePicker();
        return;
    }
    if (textarea.value && typeof textarea.value.focus === 'function') {
        textarea.value.focus();
    }
}
function openFilePicker() {
    // 上传开关关掉时这个 input 会被 v-if 摘掉，ref 就是 null —— 不能裸点
    selectFile.value?.click();
}

// 「/」快捷方式：在**行首**打 `/` 弹出 markdown 模板菜单。
//
// 为什么限定行首：正文里 `/` 太常见了（路径、日期、`a/b`），到处弹菜单会烦人；
// 行首打 `/` 是个明确的开头动作。缩进过的行（前面只有空白）也算行首。
//
// 判定与模板都在 `slash-template.js`：这个组件和 StickyComposer 共用一份。
const slashMenu = ref(false);
let slashEl = null;

function onTextareaKeydown(e) {
    if (e.key === 'Escape') {
        if (slashMenu.value) {
            slashMenu.value = false;
            e.stopPropagation();
        }
        return;
    }
    if (e.key !== '/') return;
    // 硬件键盘（桌面）：这里的 `/` 还没落进文本，判定点在光标当前位置。
    // 屏幕键盘不保证能走到这里 —— 手机上靠下面的 onTextareaInput。
    if (!slashPendingAt(e.target, app.send.text)) return;
    slashEl = e.target;
    slashMenu.value = true;
}

// 手机上 `/` **只有** `input` 事件看得见（原因见 slash-template.js）：这里既负责在刚打完
// 行首 `/` 时把菜单弹出来，也负责继续敲别的（正文里的路径、日期）时收起来。
//
// 顺带补上桌面缺的一半：以前只靠 keydown 弹、没人收 —— 菜单弹出后继续打字不会消失。
function onTextareaInput(e) {
    if (!slashMenu.value) {
        if (!slashMenuShouldOpen(e, app.send.text)) return;
        slashEl = e.target;
        slashMenu.value = true;
        return;
    }
    if (!slashMenuShouldStay(e, app.send.text)) {
        slashMenu.value = false;
    }
}

async function insertSlashTemplate(tpl) {
    const el = slashEl;
    const text = app.send.text || '';
    // ⚠️ 光标位置要在 await **之前**读 —— 要插入的文本可能是动作算出来的（异步），
    // 等回来时光标未必还在原处。
    const pos = el && typeof el.selectionStart === 'number' ? el.selectionStart : text.length;
    // 连同刚打的那个 `/` 一起换掉（如果它还在光标前）
    const head = stripTrailingSlash(text.slice(0, pos));
    const tail = text.slice(pos);
    // 模板项直接给文本；动作项（插入时间 / UUID）在**这一刻**才算 —— 时间是「现在」的
    const insert = await resolveSlashText(tpl);
    app.send.text = head + insert + tail;
    slashMenu.value = false;
    nextTick(() => {
        const caret = head.length + insert.length;
        el?.focus?.();
        el?.setSelectionRange?.(caret, caret);
    });
}
function onSendShortcut() {
    if (!sendDisabled.value) {
        sendAll();
    }
}
function removeFile(index) {
    app.send.files.splice(index, 1);
}
function handleSelectFiles(files) {
    if (!files.length) {
        return;
    }
    if (files.some(file => !file.size)) {
        toast(t('cannotSendEmptyFile'));
        return;
    }
    if (files.some(file => file.size > app.config.file.limit)) {
        toast(t('fileSizeExceeded', { limit: prettyFileSize(app.config.file.limit) }));
        return;
    }
    app.send.files.splice(0);
    app.send.files.push(...files);
}
async function sendText() {
    if (!app.send.text) {
        return;
    }
    await axios.post(
        'text',
        app.send.text,
        {
            params: new URLSearchParams([['room', ws.room]]),
            headers: {
                'Content-Type': 'text/plain',
            },
        },
    );
    app.send.text = '';
}
async function sendFiles() {
    if (!app.send.files.length) {
        return;
    }
    const chunkSize = app.config.file.chunk;
    uploadedSizes.value.splice(0);
    uploadedSizes.value.push(...Array(app.send.files.length).fill(0));
    progress.value = true;
    await Promise.all(app.send.files.map(async (file, index) => {
        if (file.size < chunkSize) {
            const formData = new FormData;
            formData.set('file', file);
            await axios.postForm('upload', formData, {
                params: new URLSearchParams([['room', ws.room]]),
                onUploadProgress: event => uploadedSizes.value[index] = event.loaded,
            });
            return;
        }
        const response = await axios.post('upload/chunk', file.name, {
            headers: { 'Content-Type': 'text/plain' },
            params: new URLSearchParams([['room', ws.room]]),
        });
        const uuid = response.data.result.uuid;
        let uploadedSize = 0;
        while (uploadedSize < file.size) {
            const chunk = file.slice(uploadedSize, uploadedSize + chunkSize);
            await axios.post(`upload/chunk/${uuid}`, chunk, {
                headers: { 'Content-Type': 'application/octet-stream' },
                onUploadProgress: event => uploadedSizes.value[index] = uploadedSize + event.loaded,
            });
            uploadedSize += chunkSize;
        }
        await axios.post(`upload/finish/${uuid}`, null, {
            params: new URLSearchParams([['room', ws.room]]),
        });
    }));
    app.send.files.splice(0);
}
async function sendAll() {
    try {
        if (app.send.text) {
            await sendText();
        }
        if (app.send.files.length) {
            await sendFiles();
        }
        toast(t('sendSuccess'));
        if (app.fullscreenSendClose) {
            textFullscreen.value = false;
        }
        focus();
    } catch (error) {
        const errMsg = errorMessage(error);
        if (errMsg) {
            toast(t('sendFailedMsg', { msg: errMsg }));
        } else {
            toast(t('sendFailed'));
        }
    } finally {
        progress.value = false;
    }
}
function handleDragLeave(event) {
    if (event.currentTarget.contains(event.relatedTarget)) {
        return;
    }
    dragover.value = false;
}
function handleDrop(event) {
    dragover.value = false;
    if (!(event && event.dataTransfer)) {
        return;
    }
    const files = Array.from(event.dataTransfer.files || []);
    if (files.length) {
        handleSelectFiles(files);
    }
}
function handlePaste(event) {
    if (!(event && event.clipboardData)) {
        return;
    }
    const items = Array.from(event.clipboardData.items || []);
    const files = items.filter(item => item.kind === 'file').map(item => item.getAsFile()).filter(Boolean);
    if (files.length) {
        handleSelectFiles(files);
    }
}
onMounted(() => {
    document.addEventListener('paste', handlePaste);
    nextTick(() => {
        focus();
    });
});
onBeforeUnmount(() => {
    document.removeEventListener('paste', handlePaste);
});
</script>

<style scoped>
.unified-composer {
    /* 圆角与高度都可被外层覆写：输入区外壳包着它时要用**同心**圆角（外R − 内缩）、
       并在左右布局下**铺满**外壳的高度。
       走 CSS 变量继承而不是外层 :deep() —— Vuetify 组件的根元素**不带**父组件的
       scoped 属性，:deep() 那条选择器根本匹配不上（实测踩过）；而变量继承不走 scope。 */
    border-radius: var(--cc-composer-radius, var(--cc-radius-xl, 28px));
    height: var(--cc-composer-height, auto);
    border: 1px solid var(--cc-glass-border, rgba(148, 163, 184, 0.26)) !important;
    box-shadow: var(--cc-shadow-2, 0 10px 26px rgba(15, 23, 42, 0.09));
    background: var(--cc-glass-bg-strong, rgba(255, 255, 255, 0.78));
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    transition: background-color var(--cc-dur, 0.22s) var(--cc-ease, ease),
                border-color var(--cc-dur, 0.22s) var(--cc-ease, ease),
                box-shadow var(--cc-dur, 0.22s) var(--cc-ease, ease);
    display: flex;
    flex-direction: column;
    /* ★ 发送按钮「始终可见」的第一道（也是主）保证 ──────────────────────
       卡片最高只能到「视口 − 工具栏 − 上下留白」。发送按钮是卡片里
       flex-shrink:0 的最后一行，卡片既然装得进视口，它就必然在视口内。
       原来这里是 calc(100dvh - 6.5rem)：既没让开顶部工具栏，又假设正文区
       一定能被压缩 —— 输入框一长（auto-grow / 多行 / 附件），卡片底就翻出视口。 */
    max-height: var(--cc-composer-max-height, calc(100dvh - var(--cc-toolbar-h, 52px) - 3 * var(--cc-gap, 8px)));
    min-height: 0;
    /* Vuetify 的 .v-card 自带 overflow:hidden，会把下面 footer 的
       position:sticky 与阴影一起裁掉（sticky 需要祖先不裁剪才能生效）。
       正文区自己带 overflow-y:auto，裁剪交给它，这一层放开。 */
    overflow: visible;
}

@media (max-width: 1263px) {
    .unified-composer {
        max-height: var(--cc-composer-max-height, calc(100dvh - var(--cc-toolbar-h, 52px) - 2 * var(--cc-gap, 8px)));
    }
}

.unified-composer--dark {
    border-color: var(--cc-glass-border, rgba(71, 85, 105, 0.72)) !important;
    box-shadow: var(--cc-shadow-2, 0 12px 30px rgba(2, 6, 23, 0.5));
    background: var(--cc-glass-bg-strong, rgba(15, 23, 42, 0.76));
}

.unified-composer--dragover {
    border-color: var(--v-primary-base, #1976d2) !important;
    box-shadow: 0 0 0 2px var(--v-primary-base, #1976d2);
}

.unified-composer--dragover * {
    pointer-events: none;
}

/* ⚠️ 选择器是 `.v-field`（Vuetify 3），不是 `.v-input__slot`（那是 Vuetify 2 的名字）。
   这里原来写的是 v2 的名字，规则从来没生效过 —— 表现为输入框没有圆角底、也没有内边距。 */
.unified-composer__textarea :deep(.v-field) {
    box-shadow: none !important;
    /* 同心圆角：卡片圆角 − 正文区内缩。链条：外壳 28 → 卡片 20 → 输入框 12，
       每一级都内缩 --cc-frame-inset(8px)，三级 R 角互相平行。 */
    border-radius: calc(var(--cc-composer-radius, var(--cc-radius-xl, 28px)) - var(--cc-frame-inset, 8px));
    background: rgba(248, 250, 252, 0.7) !important;
    padding: 0.25rem 0.25rem 0 0.25rem;
}

.unified-composer--dark .unified-composer__textarea :deep(.v-field) {
    background: rgba(30, 41, 59, 0.6) !important;
}

.unified-composer--dark .unified-composer__textarea :deep(textarea),
.unified-composer--dark .unified-composer__limit,
.unified-composer--dark .unified-composer__attachments {
    color: rgba(226, 232, 240, 0.92) !important;
}

/* 高度**不再由 CSS 定死**：交给 Vuetify 的 auto-grow + max-rows。
   以前这里写死 height:80px，和 auto-grow 是两套互相打架的高度来源
   （auto-grow 走 rows 属性，CSS 走 height，谁在后谁赢），
   所以只保留「不许用户手动拖拽」和「超出上限就在框内滚动」这两条。 */
.unified-composer__textarea :deep(textarea) {
    resize: none;
    overflow-y: auto;
    overscroll-behavior: contain;
}

.unified-composer__textarea--secondary :deep(textarea) {
    resize: none;
}

.unified-composer__textblock {
    position: relative;
}

.unified-composer__fullscreen-btn {
    position: absolute;
    top: 4px;
    right: 4px;
    z-index: 1;
    background: rgba(255, 255, 255, 0.9);
    border-radius: 50%;
}

.unified-composer__fullscreen-btn :deep(.v-btn__overlay) {
    background: transparent;
}

.unified-composer--dark .unified-composer__fullscreen-btn {
    background: rgba(30, 41, 59, 0.9);
}

.unified-composer__fullscreen-btn :deep(.v-icon),
.unified-composer__fullscreen-btn :deep(.v-btn__content) {
    opacity: 0.55;
}

.unified-composer__textblock:hover .unified-composer__fullscreen-btn :deep(.v-icon) {
    opacity: 1;
}

.pts-fullscreen-body {
    display: flex;
    flex-direction: column;
    padding: 1rem;
    min-height: 0;
}

.pts-fullscreen-textarea {
    flex: 1 1 auto;
    min-height: 0;
}

.pts-fullscreen-textarea :deep(.v-field--solo),
.pts-fullscreen-textarea :deep(.v-field__input),
.pts-fullscreen-textarea :deep(textarea) {
    height: 100% !important;
}

.pts-fullscreen-textarea :deep(.v-field__input) {
    overflow-y: auto;
}

.unified-composer__inputs {
    display: flex;
    flex-direction: column;
    flex: var(--cc-composer-inputs-flex, 0 1 auto);
    min-height: var(--cc-composer-inputs-min, 0);
}

/* ── 左右布局下的高度分配 ──────────────────────────────────────────
   外壳给了确定高度时，把多余的空间分给**输入框**，让「写字的地方」真的变大，
   而不是空着一片。
   ⚠️ 全部走 **CSS 变量 + flex**，不用 `height: 100%`：
     · 百分比高度只认确定值，中间任何一层断了它就解析成 auto（实测踩过）；
     · 变量继承不走 scope、不看组件层级 —— Vuetify 组件的根元素不带父组件的
       scoped 属性，类穿透（class fallthrough）在多层组件上也不可靠（实测踩过）。
   默认值 = 现状（不伸展），所以不在左右布局时行为完全不变。 */
.unified-composer__textblock {
    position: relative;
    display: flex;
    flex-direction: column;
    flex: var(--cc-composer-textblock-flex, 0 1 auto);
    min-height: var(--cc-composer-textblock-min, 0);
}

.unified-composer__textarea {
    flex: var(--cc-composer-textarea-flex, 0 1 auto);
    min-height: var(--cc-composer-textarea-min, 0);
}

.unified-composer__inputs--files-first .unified-composer__textblock {
    order: 3;
}

.unified-composer__inputs--files-first .unified-composer__divider {
    order: 2;
}

.unified-composer__inputs--files-first .unified-composer__fileblock {
    order: 1;
}

.unified-composer__textarea--secondary {
    max-height: 3.5rem;
}


.unified-composer__progress {
    padding: 0 var(--cc-frame-inset, 8px) var(--cc-frame-inset, 8px);
}

.unified-composer__divider {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.25rem 0;
}

.unified-composer__limit {
    flex-shrink: 0;
    white-space: nowrap;
}

.unified-composer__divider-line {
    flex: 1;
    min-width: 0;
    height: 1px;
    background: rgba(148, 163, 184, 0.35);
}

.unified-composer__divider-line--short {
    flex: 0 0 1.5rem;
}

.unified-composer--dark .unified-composer__divider-line {
    background: rgba(71, 85, 105, 0.6);
}

.unified-composer__swapbtn {
    flex-shrink: 0;
}

.unified-composer__fileblock {
    min-width: 0;
}

.unified-composer__dropzone {
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px dashed rgba(148, 163, 184, 0.55);
    border-radius: calc(var(--cc-composer-radius, var(--cc-radius-xl, 28px)) - var(--cc-frame-inset, 8px));
    color: rgba(100, 116, 139, 0.95);
    cursor: pointer;
    min-height: 2.5rem;
    padding: 0.25rem 0.5rem;
    transition: border-color 0.2s ease, background 0.2s ease;
}

.unified-composer--dark .unified-composer__dropzone {
    border-color: rgba(71, 85, 105, 0.65);
    color: rgba(203, 213, 225, 0.85);
}

.unified-composer__dropzone:hover {
    border-color: var(--v-theme-primary);
    background: rgba(99, 102, 241, 0.06);
}

.unified-composer__dropzone--primary {
    min-height: 7rem;
    flex-direction: column;
    gap: 0.25rem;
}

.unified-composer__attachments {
    min-height: 1.5rem;
}

.unified-composer__body {
    flex: 1 1 auto;
    min-height: 0;
    /* 纵向 flex：__inputs 在左右布局下靠 flex:1 吃掉剩余高度（见上面的高度分配段） */
    display: flex;
    flex-direction: column;
    /* 纵向 flex：__inputs 在左右布局下靠 flex:1 吃掉剩余高度（见上面的高度分配段） */
    display: flex;
    flex-direction: column;
    /* 统一内缩（四边同一个值）。原来是 Vuetify 的 `pa-1 pa-md-3`：窄屏 4px、
       宽屏 12px，而**上下**还额外被分区分隔线切走一截 —— 内外圆角没法同心。 */
    padding: var(--cc-frame-inset, 8px);
    /* 正文超出卡片上限时**在这里内部滚动**，而不是被裁掉。
       footer 是它的兄弟节点（不在这个滚动盒里），所以滚正文不会带走发送按钮。 */
    overflow-y: auto;
    overscroll-behavior: contain;
}

.unified-composer__footer {
    /* ★ 发送按钮「始终可见」的第二道保险 ──────────────────────────────
       底部操作栏自己粘在视口底。卡片装得进视口时它本来就在视口内（不触发 sticky）；
       万一 dvh 不被支持、或某天卡片的上限被改坏，它也会钉在视口底边不跟着内容跑。
       背景必须**不透明**：否则滚动时底下正文会从按钮后面透出来。 */
    position: sticky;
    bottom: 0;
    z-index: 3;
    flex-shrink: 0;
    /* ⚠️ 这里**不能**用 `grid-template-columns: ... auto ...`。
       左右布局下输入区只有 ~420px 宽，而 960px 那个断点是按**视口**算的、
       对窄容器不生效 —— 三列 grid 会把中间那排图标挤到容器外，
       最右边那个（深浅色切换）直接被裁掉，看起来就是「按钮不见了」。
       换成会换行的 flex：放不下就换行，一个都不会丢。 */
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    /* 图标靠左、发送按钮贴右（`margin-inline-start: auto`）。
       原来整体居中 —— 发送键是全屏里最该被「定位到」的按钮，
       居中会让它随图标数量左右漂移，永远停在同一个地方才点得准。 */
    justify-content: flex-start;
    gap: 0.35rem 0.5rem;
    border-top: 1px solid var(--cc-glass-border, rgba(226, 232, 240, 0.9));
    border-radius: 0 0 calc(var(--cc-composer-radius, var(--cc-radius-xl, 28px)) - 1px) calc(var(--cc-composer-radius, var(--cc-radius-xl, 28px)) - 1px);
    background: var(--cc-glass-bg-solid, rgba(255, 255, 255, 0.94));
    backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    -webkit-backdrop-filter: blur(var(--cc-glass-blur, 18px)) saturate(var(--cc-glass-saturate, 165%));
    padding: 0.25rem;
}

.unified-composer__footer-icons {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: center;
    gap: 0.15rem 0.5rem;
    min-width: 0;
}

.unified-composer__device {
    margin-inline-start: 2px;
    height: 28px;
    padding: 0 6px;
}

.unified-composer__device-full {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    font-variant-numeric: tabular-nums;
}

.unified-composer__devicestat {
    display: inline-flex;
    align-items: center;
}

.unified-composer__send {
    /* 贴右：把左边图标排剩下的空间都吃掉 */
    margin-inline-start: auto;
    flex-shrink: 0;
    white-space: nowrap;
    /* 发送是全站点击密度最高的按钮，给足点击区（原来 ~36px 偏小） */
    min-height: var(--cc-touch-lg, 46px);
    min-width: 104px;
    padding-inline: 20px;
    font-weight: 600;
    letter-spacing: 0.02em;
    box-shadow: var(--cc-shadow-1, 0 2px 10px rgba(15, 23, 42, 0.06));
    transition: transform var(--cc-dur, 0.22s) var(--cc-ease, ease),
                box-shadow var(--cc-dur, 0.22s) var(--cc-ease, ease);
}

.unified-composer__send:not(:disabled):hover {
    transform: translateY(-1px);
    box-shadow: var(--cc-shadow-2, 0 10px 26px rgba(15, 23, 42, 0.09));
}

.unified-composer__send:not(:disabled):active {
    transform: translateY(0) scale(0.97);
}

/* 底部那排小图标（设备 / 配色 / 快捷键 / 主题）也放到能点准的尺寸。
   它们和发送按钮同处一条栏，一大一小会显得没对齐。 */
.unified-composer__footer .v-btn--icon.v-btn--size-small,
.unified-composer__footer .v-btn--icon.v-btn--density-comfortable {
    min-width: var(--cc-touch, 40px);
    min-height: var(--cc-touch, 40px);
    border-radius: var(--cc-radius-pill, 999px);
}

.unified-composer__footer .v-btn--icon .v-icon {
    font-size: 22px;
}

@media (max-width: 960px) {
    .unified-composer__send {
        flex: 1 0 100%;
        justify-content: center;
    }
}
</style>