/**
 * 图表分类调色板（categorical palette）。
 *
 * 设计取舍：应用「外观 / 主题色」整体走 Linear 石墨单色，但**多类别的数据可视化**
 * （分布环形图、多序列趋势折线）若也用灰阶，类别之间根本无法区分——环形图会糊成
 * 一整圈白、多条折线叠在一起分不清谁是谁。因此**数据系列**统一改用这组「可区分但
 * 克制」的分类色；UI chrome（按钮 / 侧栏 / 边框等）仍保持单色不变。
 *
 * 与管理控制台「最近使用 Top12」折线图共用同一组色，保证全站图表观感一致。
 */
export const CHART_CATEGORICAL_COLORS = [
  '#3b82f6', // blue
  '#d97706', // amber
  '#0d9488', // teal
  '#ef4444', // red
  '#65a30d', // lime
  '#ec4899', // pink
  '#0891b2', // cyan
  '#ea580c', // orange
  '#8b5cf6', // violet
  '#16a34a', // green
  '#c026d3', // fuchsia
  '#6366f1' // indigo
] as const

/** 按序号取分类色（超出长度自动循环）。 */
export function chartCategoricalColor(index: number): string {
  return CHART_CATEGORICAL_COLORS[index % CHART_CATEGORICAL_COLORS.length]
}
