// 标准模式「工作区」的两组视图偏好：**布局**与**展示方式**。
//
// 为什么单独放一个文件、而不是塞进 displayToggles.js：
//   那套开关的存储是 `displayByMode[模式][开关] = true|false`，只存**布尔**；
//   而这两个是**枚举**（布局三选一、展示方式二选一），混进去要么把存储结构搞成
//   混杂类型，要么给每个枚举值造一个伪开关（互斥布尔组，状态能自相矛盾）。
//   所以跟 `composerPrimary` / `shareDefaults` 一样：独立 key、独立 action。
//
// 选项在这里定义一次，消费点（DefaultMode 的工作区条、App.vue 的个性化面板）都读这份 ——
// 别各自再抄一遍 key 列表，否则加第四种布局时必然漏掉一处。

/** 布局：输入区与历史消息的相对位置 */
export const LAYOUT_OPTIONS = [
    {
        key: 'stack',
        labelKey: 'layoutStack',
        // 「上下」画的是**上下两块**（agenda = 一叠横条），跟实际长相一致。
        icon: 'mdi-view-agenda-outline',
    },
    {
        key: 'split',
        labelKey: 'layoutSplit',
        icon: 'mdi-view-column-outline',
    },
    {
        key: 'chat',
        labelKey: 'layoutChat',
        // 输入区在底部 = 聊天软件的长相
        icon: 'mdi-forum-outline',
    },
];

export const LAYOUT_KEYS = LAYOUT_OPTIONS.map((option) => option.key);
export const DEFAULT_LAYOUT = 'stack';

/** 展示方式：历史消息与文件的排列 */
export const VIEW_OPTIONS = [
    { key: 'list', labelKey: 'viewList', icon: 'mdi-view-list-outline' },
    { key: 'grid', labelKey: 'viewGrid', icon: 'mdi-view-grid-outline' },
];

export const VIEW_KEYS = VIEW_OPTIONS.map((option) => option.key);
export const DEFAULT_VIEW = 'list';

/** 布局在窄屏下会退化成上下（CSS 处理，见 DefaultMode），
 *  这个函数给需要「知道当前实际布局」的 JS 逻辑用（例如新消息该往哪边滚）。 */
export function effectiveLayout(layout, width) {
    if (layout === 'split' && width > 0 && width <= 960) {
        return 'stack';
    }
    return LAYOUT_KEYS.includes(layout) ? layout : DEFAULT_LAYOUT;
}
