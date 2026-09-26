// 房间管理的契约检查（纯 Node，不需要浏览器）。
//
//     cd web-vue3 && node scripts/check-room-management.mjs
//
// 为什么需要它：房间管理是**前后端各一半**的功能，而两半之间的接缝全是没有编译期保护的东西：
//   1. 四个事件名（create / delete / cleanup / enter）写在 RoomList 的 emit 里，
//      App.vue 用它接线 —— 任何一边改名字都是「点了没反应」，控制台还一片干净；
//   2. 「谁能删」的判定**必须只在服务端**。前端一旦自己判一遍（比如自己认房间名是不是
//      "default"），两份规则就开始了各自的漂移，而漂掉的一定是没有测试兜着的前端 ——
//      后果是界面显示了删除入口、点了 403，或者更糟：服务端拦住了但界面以为删成功了；
//   3. 文案键要在**四份**语言文件里都存在（漏一份不报错，只在那个语言下显示成 key 本身）。
//
// 断言的是契约，不是实现：改图标、改文案、调整样式都不该让它变红。

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '..');
const read = (p) => fs.readFileSync(path.join(root, p), 'utf8');

let failed = 0;
function ok(name, condition, detail = '') {
    console.log(`${condition ? 'ok  ' : 'FAIL'}  ${name}${detail && !condition ? `  → ${detail}` : ''}`);
    if (!condition) failed += 1;
}

const roomList = read('src/components/RoomList.vue');
const app = read('src/App.vue');
const toolbar = read('src/components/PageToolbar.vue');

// ── 1. 事件契约：RoomList 发什么，App.vue 就要接什么 ────────────────────
const EVENTS = ['create', 'delete', 'cleanup', 'enter'];
ok('RoomList 声明了四个管理事件', EVENTS.every((e) => new RegExp(`emit\\(['"]${e}['"]`).test(roomList)));
for (const e of EVENTS) {
    // 两处 RoomList（dock 侧栏 + 移动端底部弹层）都要接，漏一处就会出现
    // 「桌面能用、手机上点了没反应」这种只在特定布局下复现的 bug。
    const wired = (app.match(new RegExp(`@${e}="`, 'g')) || []).length;
    ok(`App.vue 的 @${e} 接了两处（dock + sheet）`, wired === 2, `实际接了 ${wired} 处`);
}

// ── 2. 权限判定只能有一份，且在服务端 ─────────────────────────────────
// 前端只信服务端给的 room.canManage —— 这是「UI 隐藏 ≠ 权限」那条原则的前半句。
ok('删除按钮按服务端下发的 canManage 显示', /v-if="room\.canManage"/.test(roomList));
// ⚠️ 不能简单地搜 `'default'` 字面量 —— RoomList 里**合法地**出现过它
// （模式皮肤的 `rl--` 前缀回退值）。要钉的是「用房名做判断」，所以只看比较表达式。
ok(
    'RoomList 没有自己拿房间名做判断',
    !/(room|currentRoom)\.name\s*===\s*['"]/.test(roomList),
    '找到形如 room.name === "…" 的判断',
);
ok('App.vue 里没有自己复刻可删判定', !/canManageRoom|isDefaultRoom|room\.name ===\s*['"]default['"]/.test(app));
// 删除必须带上「手输的那个房间密码」（走 X-Room-Auth-Tokens 与已有身份并存），
// 而不是覆盖 Authorization —— 覆盖了管理员就变成「必须手打密码才能删」。
ok('删除请求用 X-Room-Auth-Tokens 送手输密码', /X-Room-Auth-Tokens/.test(app) && /axios\.delete\(\s*`rooms\//.test(app));
// 房间管理密码是第三把钥匙：新建 / 清理用它，删除时与房间密码二选一。
// ⚠️ 前端只负责把它送到请求头里 —— **校验只在服务端**（roomManagePasswordOK）。
ok('新建 / 清理都带上管理密码请求头', (app.match(/X-Room-Manage-Password/g) || []).length >= 2);
ok('App.vue 不自己判管理密码对错', !/roomManagePassword\s*===\s*['"]/.test(app));

// ── 3. 入口去重：房间列开着时，工具栏不该再有一个「门」图标 ────────────
// 两项都要有，且顺序必须是「先守卫、后图标」—— 否则出现的是一个没有 v-if 的门图标。
const guardAt = toolbar.indexOf('v-if="!roomListEnabled"');
const doorAt = toolbar.indexOf('mdi-door-open');
const doorCount = (toolbar.match(/mdi-door-open/g) || []).length;
ok(
    '工具栏的「按名称进入」只在房间列表关闭时出现',
    guardAt >= 0 && doorAt > guardAt && doorCount === 1,
    `guard@${guardAt} door@${doorAt} 出现次数=${doorCount}`,
);
ok('侧栏动作行里有「按名称进入」', /enterRoomByName/.test(roomList));

// ── 4. 语言包：四份都要有全部房间管理文案 ─────────────────────────────
const KEYS = [
    'createRoom', 'create', 'createRoomHint', 'roomName', 'roomPassword', 'roomPasswordConfirm',
    'roomPasswordAdminOptional', 'deleteRoom', 'deleteRoomConfirm', 'deleteRoomHint',
    'cleanupRooms', 'enterRoomByName', 'roomCreateSuccess', 'roomCreateFailed',
    'roomNameRequired', 'roomPasswordRequired', 'roomPasswordMismatch',
    'roomDeleteSuccess', 'roomDeleteFailed', 'roomCleanupDone', 'roomCleanupNothing', 'roomCleanupFailed',
    'roomManagePasswordLabel', 'roomManagePasswordHint', 'roomManagePasswordOptional', 'cleanupRoomsHint',
];
for (const locale of ['zh', 'zh-TW', 'en', 'ja']) {
    const dict = JSON.parse(read(`src/locales/${locale}.json`));
    const missing = KEYS.filter((k) => typeof dict[k] !== 'string' || !dict[k].trim());
    ok(`locale ${locale}: 房间管理文案齐全`, missing.length === 0, missing.join(', '));
}

// ── 5. 服务端接得住 ─────────────────────────────────────────────────
const lib = path.resolve(root, '..', 'cloud-clip', 'lib');
if (fs.existsSync(lib)) {
    const handlers = fs.readFileSync(path.join(lib, 'handler_rooms.go'), 'utf8');
    for (const route of ['handleRooms', 'handleRoomItem', 'handleRoomCleanup']) {
        ok(`服务端有 ${route}`, handlers.includes(`func (s *ClipboardServer) ${route}(`));
    }
    const main = fs.readFileSync(path.join(lib, 'main.go'), 'utf8');
    ok('路由注册了 /rooms/cleanup（比 /rooms/ 更具体，优先匹配）', main.includes('"/rooms/cleanup"'));
    ok('路由注册了 /rooms/ 前缀（删除用）', main.includes('"/rooms/"'));
} else {
    console.log('skip  没找到 cloud-clip/lib（只查前端时正常）');
}

console.log(failed ? `\n${failed} 条失败` : '\n全部通过');
process.exit(failed ? 1 : 0);
