<script setup>
// 登录设备管理：此刻有谁在用我的凭据登着这个实例、随时可以把某一台踢下去。
//
// 这是「令牌被盗之后止血」的唯一入口。七天免密的代价是「一次泄露的窗口变长」，
// 所以必须配一把能立刻收回来的东西 —— 否则用户唯一的办法还是改密码（那会把所有人都踢掉，
// 而且下一次还得再来一遍）。
//
// 三件刻意为之的事，别当成 bug 改掉：
//   1. **列表里没有任何凭据**：只有服务端返回的元信息（时间、UA、来源 IP）。
//      这份列表本身是要渲染给别人看的，放 token 等于把钥匙挂在门上。
//   2. **能踢谁由服务端决定**，前端不做自己的权限判断：
//      平台级会话看得到全部，房间会话只看得到自己这一族（同一个浏览器 / 同一次登录）。
//      界面上「少几条」不是漏 —— 是服务端的可见范围就那么宽。
//   3. **每次打开都重新拉**：这是「现在有谁在线」的答案，缓存着显示等于骗人。
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useWebSocketStore } from '@/store/websocket';
import { formatTimestamp } from '@/util.js';

const mdiAlertCircleOutline = 'mdi-alert-circle-outline';
const mdiClose = 'mdi-close';
const mdiDevices = 'mdi-devices';
const mdiLaptopOff = 'mdi-laptop-off';
const mdiRefresh = 'mdi-refresh';
const mdiShieldKeyOutline = 'mdi-shield-key-outline';

const props = defineProps({
    modelValue: {
        type: Boolean,
        default: false,
    },
});

const emit = defineEmits(['update:modelValue']);

const ws = useWebSocketStore();
const { t } = useI18n();

const visible = computed({
    get: () => props.modelValue,
    set: (value) => emit('update:modelValue', value),
});

const loading = ref(false);
const error = ref('');
const sessions = ref([]);
const ttlSeconds = ref(0);
const lifetimeSeconds = ref(0);
const revoking = ref('');

function describeDevice(session) {
    const parts = [];
    const agent = String(session?.userAgent || '').trim();
    parts.push(agent || t('sessionUnknownDevice'));
    return parts.join('');
}

function describeMeta(session) {
    const parts = [];
    if (session?.createdAt) {
        parts.push(t('sinceAt', { time: formatTimestamp(session.createdAt) }));
    }
    if (session?.lastSeenAt) {
        parts.push(t('lastActiveAt', { time: formatTimestamp(session.lastSeenAt) }));
    }
    const ip = String(session?.lastIp || session?.createdIp || '').trim();
    if (ip) {
        parts.push(ip);
    }
    return parts.join(' · ');
}

function expiresText(session) {
    if (!session?.expiresAt) {
        return '';
    }
    return t('expiresAt', { time: formatTimestamp(session.expiresAt) });
}

function daysLeft(seconds) {
    if (!seconds) {
        return 0;
    }
    return Math.max(1, Math.round(Number(seconds) / 86400));
}

const ttlDays = computed(() => daysLeft(ttlSeconds.value));
const lifetimeDays = computed(() => daysLeft(lifetimeSeconds.value));

async function load() {
    loading.value = true;
    error.value = '';
    try {
        const data = await ws.fetchSessions();
        sessions.value = Array.isArray(data?.sessions) ? data.sessions : [];
        ttlSeconds.value = Number(data?.ttl) || 0;
        lifetimeSeconds.value = Number(data?.lifetime) || 0;
    } catch (err) {
        console.error('读取登录会话失败:', err);
        sessions.value = [];
        error.value = String(err?.message || t('sessionListFailed'));
    } finally {
        loading.value = false;
    }
}

async function revoke(sid) {
    if (!sid || revoking.value) {
        return;
    }
    revoking.value = sid;
    try {
        const ok = await ws.revokeSession(sid);
        if (!ok) {
            error.value = t('sessionRevokeFailed');
            return;
        }
        // ⚠️ 先记下「踢的是不是自己」再把它从列表里去掉 ——
        // 顺序反了的话界面永远看不出问题，但踢掉自己之后不会回到未登录状态。
        const self = sessions.value.find(item => item.id === sid && item.current);
        sessions.value = sessions.value.filter(item => item.id !== sid);
        if (self) {
            visible.value = false;
            await ws.logout({ all: false });
        }
    } finally {
        revoking.value = '';
    }
}

async function revokeAllExceptCurrent() {
    if (revoking.value) {
        return;
    }
    const others = sessions.value.filter(item => !item.current);
    for (const session of others) {
        await revoke(session.id);
    }
}

watch(visible, (open) => {
    if (open) {
        load();
    }
});
</script>

<template>
    <v-dialog v-model="visible" max-width="560">
        <v-card>
            <v-card-title class="d-flex align-center">
                <v-icon start size="20">{{ mdiDevices }}</v-icon>
                <span class="flex-grow-1">{{ t('sessionManagerTitle') }}</span>
                <v-btn icon variant="text" @click="visible = false">
                    <v-icon>{{ mdiClose }}</v-icon>
                </v-btn>
            </v-card-title>
            <v-divider></v-divider>
            <v-card-text style="max-height: 62vh; overflow-y: auto;">
                <div class="text-body-2 text-medium-emphasis mb-3">
                    <template v-if="ttlDays">{{ t('sessionRetentionHint', { days: ttlDays, max: lifetimeDays }) }}</template>
                    <template v-else>{{ t('sessionHint') }}</template>
                </div>

                <div v-if="loading" class="d-flex justify-center py-8">
                    <v-progress-circular indeterminate size="28" width="3" color="primary" />
                </div>

                <div v-else-if="error" class="session-manager__state">
                    <v-icon size="30" color="error">{{ mdiAlertCircleOutline }}</v-icon>
                    <span class="text-body-2">{{ error }}</span>
                </div>

                <div v-else-if="!sessions.length" class="session-manager__state">
                    <v-icon size="30" class="text-medium-emphasis">{{ mdiShieldKeyOutline }}</v-icon>
                    <span class="text-body-2 text-medium-emphasis">{{ t('sessionEmpty') }}</span>
                </div>

                <v-list v-else density="comfortable" class="pa-0">
                    <v-list-item v-for="session in sessions" :key="session.id" class="px-0">
                        <template v-slot:prepend>
                            <v-icon :color="session.current ? 'primary' : 'default'">
                                {{ session.current ? mdiDevices : mdiShieldKeyOutline }}
                            </v-icon>
                        </template>
                        <v-list-item-title class="session-manager__title">
                            {{ describeDevice(session) }}
                            <v-chip v-if="session.current" size="x-small" color="primary" variant="tonal" label class="ml-1">
                                {{ t('sessionCurrent') }}
                            </v-chip>
                            <v-chip v-else-if="session.scope === 'global'" size="x-small" variant="tonal" label class="ml-1">
                                {{ t('sessionScopeGlobal') }}
                            </v-chip>
                        </v-list-item-title>
                        <v-list-item-subtitle class="session-manager__subtitle">
                            {{ describeMeta(session) }}
                            <template v-if="expiresText(session)"> · {{ expiresText(session) }}</template>
                        </v-list-item-subtitle>
                        <template v-slot:append>
                            <v-btn
                                v-if="!session.current"
                                variant="text"
                                size="small"
                                color="error"
                                :prepend-icon="mdiLaptopOff"
                                :loading="revoking === session.id"
                                @click="revoke(session.id)"
                            >
                                {{ t('sessionKick') }}
                            </v-btn>
                        </template>
                    </v-list-item>
                </v-list>

                <div class="text-caption text-medium-emphasis mt-3">{{ t('sessionPrivacyHint') }}</div>
            </v-card-text>
            <v-card-actions>
                <v-btn
                    v-if="sessions.filter(item => !item.current).length > 0"
                    variant="text"
                    color="error"
                    :prepend-icon="mdiLaptopOff"
                    :loading="Boolean(revoking)"
                    @click="revokeAllExceptCurrent"
                >
                    {{ t('sessionKickOthers') }}
                </v-btn>
                <v-spacer></v-spacer>
                <v-btn variant="text" :prepend-icon="mdiRefresh" :loading="loading" @click="load">
                    {{ t('refresh') }}
                </v-btn>
                <v-btn variant="text" @click="visible = false">{{ t('close') }}</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<style scoped>
/* ⚠️ 弹窗一律 teleport 出应用子树 —— 样式必须挂在自己身上（见仓库里的覆盖层约定）。 */
.session-manager__state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 32px 8px;
    text-align: center;
}

.session-manager__title {
    font-size: 0.875rem;
    word-break: break-word;
}

.session-manager__subtitle {
    white-space: normal;
    font-size: 0.75rem;
}
</style>
