<script setup>import { computed, watchEffect } from 'vue';
import { useAppStore } from '@/store/app';
import { useTheme } from 'vuetify';
import { MODES, resolveModeComponent } from '@/views/modes/registry.js';

const app = useAppStore();
const theme = useTheme();
const isDark = computed(() => theme.current.value?.dark ?? false);

const viewComponent = computed(() => resolveModeComponent(app.uiMode));

// 已下架的模式（老书签里的 `?mode=mega`、或 localStorage 里存的旧值）会落到兜底组件上。
// 光靠 `resolveModeComponent` 兜底是不够的：`app.uiMode` 还是那个无效值，
// 于是工具栏显示的模式名、个性化面板的高亮，都和真正渲染出来的东西对不上。
// 这里把 store 里的值一并纠正过来。
// 放在 Home 而不是 store 的 state 初始化里：`app.js ← registry ← 各模式 ← app.js` 是个环，
// 在 state() 里读 MODES 有拿到未初始化值的风险（见 store/app.js 里那句同样的提醒）。
watchEffect(() => {
    if (!MODES.some((mode) => mode.key === app.uiMode)) {
        app.setUiMode('default');
    }
});
</script>

<template>
    <div class="mode-root" :class="{ 'mode-root--dark': isDark }">
        <component :is="viewComponent"></component>
    </div>
</template>

<style scoped>
.mode-root {
    min-height: 100vh;
    min-height: 100dvh;
}
</style>