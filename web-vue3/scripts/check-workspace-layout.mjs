// 标准模式「工作区」偏好（布局 / 展示方式）的契约检查。纯 Node，不需要浏览器。
//
//     cd web-vue3 && node scripts/check-workspace-layout.mjs
//
// 为什么需要它：这两组偏好的定义是**一处定义、三处消费**，
// 而且全都不是编译期能查出来的：
//   1. 选项键在 `src/data/workspace.js` 定义，被 DefaultMode（工作区条）、
//      App.vue（个性化面板）、store/app.js（持久化 + 合法值守卫）三处读；
//   2. 每个选项的 `labelKey` 必须在**四份**语言文件里都存在 ——
//      漏一份不会报错，只会让那个语言下的按钮显示成 key 本身；
//   3. localStorage 的键名（homeLayout / historyView）一旦漂移，
//      老用户的偏好会静默丢失（读不到就回落默认），没有任何报错。
//
// 断言的是**契约**，不是实现：换图标、改文案、调整 CSS 都不该让它变红；
// 只有「键名/语言包/持久化对不上」才该红。

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const read = (p) => fs.readFileSync(path.join(root, p), 'utf8');

const { LAYOUT_OPTIONS, VIEW_OPTIONS, LAYOUT_KEYS, VIEW_KEYS, DEFAULT_LAYOUT, DEFAULT_VIEW, effectiveLayout } =
    await import(new URL('../src/data/workspace.js', import.meta.url).href);

let failed = 0;
function ok(name, condition, detail = '') {
    console.log(`${condition ? 'ok  ' : 'FAIL'}  ${name}${detail && !condition ? `  → ${detail}` : ''}`);
    if (!condition) failed += 1;
}

// ── 1. 选项定义本身 ────────────────────────────────────────────────
ok('布局有 3 种（上下 / 左右 / 聊天）', LAYOUT_KEYS.length === 3, JSON.stringify(LAYOUT_KEYS));
ok('展示方式有 2 种（列表 / 宫格）', VIEW_KEYS.length === 2, JSON.stringify(VIEW_KEYS));
ok('布局键唯一', new Set(LAYOUT_KEYS).size === LAYOUT_KEYS.length);
ok('展示方式键唯一', new Set(VIEW_KEYS).size === VIEW_KEYS.length);
ok('出厂布局是上下', DEFAULT_LAYOUT === 'stack', DEFAULT_LAYOUT);
ok('出厂展示方式是列表', DEFAULT_VIEW === 'list', DEFAULT_VIEW);
ok('出厂值在选项里', LAYOUT_KEYS.includes(DEFAULT_LAYOUT) && VIEW_KEYS.includes(DEFAULT_VIEW));

// 「宫格」必须是**新增的**那种；列表是原有的默认。两个键换个位置就说明有人把默认态改了。
ok('列表 / 宫格的键名是 list / grid', VIEW_KEYS.join(',') === 'list,grid', VIEW_KEYS.join(','));

// ── 2. 窄屏退化规则 ────────────────────────────────────────────────
// 左右布局在窄屏下必须退成上下：CSS 里那份 @media 与 JS 的 effectiveLayout
// 是两条独立的实现，规则必须一致（否则「显示上在左右、JS 以为在上下」）。
ok('左右布局在窄屏退化为上下', effectiveLayout('split', 390) === 'stack');
ok('左右布局在宽屏保持左右', effectiveLayout('split', 1440) === 'split');
ok('聊天式不退化', effectiveLayout('chat', 390) === 'chat');
ok('非法值回落到出厂布局', effectiveLayout('nonsense', 1440) === DEFAULT_LAYOUT);

// ── 3. 持久化键名 + 合法值守卫 ─────────────────────────────────────
const store = read('src/store/app.js');
ok('持久化键 homeLayout', store.includes("localStorage.getItem('homeLayout')") && store.includes("localStorage.setItem('homeLayout'"));
ok('持久化键 historyView', store.includes("localStorage.getItem('historyView')") && store.includes("localStorage.setItem('historyView'"));
ok('setHomeLayout 有合法值守卫', /setHomeLayout\(layout\)\s*\{[^}]*LAYOUT_KEYS\.includes\(layout\)/.test(store));
ok('setHistoryView 有合法值守卫', /setHistoryView\(view\)\s*\{[^}]*VIEW_KEYS\.includes\(view\)/.test(store));

// ── 4. 语言包：四份都要有全部 labelKey ─────────────────────────────
const LOCALES = ['zh', 'zh-TW', 'en', 'ja'];
const labelKeys = [...LAYOUT_OPTIONS, ...VIEW_OPTIONS].map((o) => o.labelKey);
labelKeys.push('workspaceLayout', 'workspaceViewMode', 'workspaceHint');
for (const locale of LOCALES) {
    const dict = JSON.parse(read(`src/locales/${locale}.json`));
    const missing = labelKeys.filter((key) => typeof dict[key] !== 'string' || !dict[key].trim());
    ok(`locale ${locale}: 工作区标签齐全`, missing.length === 0, missing.join(', '));
}

// ── 5. 消费点确实在用（防止「定义加了但没接线」）────────────────────
const mode = read('src/views/modes/DefaultMode.vue');
ok('DefaultMode 读 store 的 homeLayout / historyView', /app\.homeLayout/.test(mode) && /app\.historyView/.test(mode));
ok('DefaultMode 渲染工作区条', mode.includes('workspace-bar'));
ok('三套布局都有对应类', ['workspace--stack', 'workspace--split', 'workspace--chat'].every((c) => mode.includes(c)));
ok('宫格类存在', mode.includes('timeline-panel__stream--grid'));
ok('宫格时把 grid 传给卡片', /:grid="isGrid"/.test(mode));
ok('聊天式下翻转渲染顺序（最新在后）', /effective\.value === 'chat'[\s\S]{0,80}reverse\(\)/.test(mode));

const app = read('src/App.vue');
ok('个性化面板有布局 / 展示方式入口', app.includes('cc-workspace-toggle') && app.includes('LAYOUT_OPTIONS') && app.includes('VIEW_OPTIONS'));
ok('入口只对标准模式显示', /app\.uiMode === 'default'/.test(app));

// 卡片组件要认识 grid 变体，否则宫格里的窄卡片会挤成一列竖排字（实测过）。
for (const file of ['Text.vue', 'File.vue']) {
    const card = read(`src/components/received-item/${file}`);
    ok(`${file}: 声明 grid prop`, /grid:\s*\{[\s\S]{0,80}type:\s*Boolean/.test(card));
    ok(`${file}: 有 --grid 样式`, card.includes('timeline-card--grid'));
}

console.log(failed ? `\n${failed} 条失败` : '\n全部通过');
process.exit(failed ? 1 : 0);
