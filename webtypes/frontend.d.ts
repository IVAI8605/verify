// 前端专用类型（v4.0.0）
//
// 这里放「后端没有、但前端自己需要」的类型。
// 后端响应契约由 tools/gen_types.py 从 Go 的 json tag 自动生成到
// webtypes/types.d.ts，不要往那个文件里加东西——重新生成会覆盖。
//
// 约束：类型声明不产出任何运行时代码，产物仍是 ui/*.js 原生脚本。

// ── 表格列定义 ──────────────────────────────────────────────────────────
// 对应 helper.js 的 resultColumns() / healthColumns() 返回的列描述。
//   id    列标识，同时用作 DOM 上的 class（表格列宽持久化按它索引）
//   label 表头文字
//   width 列宽（px）；0 表示自动占满剩余宽度
//   cls   附加的 CSS class，可选
//   align 对齐方式，可选（如 'right'）
//   sort  排序键，可选；存在时表头可点击排序
interface ResultColumn {
  id: string;
  label: string;
  width: number;
  cls?: string;
  align?: string;
  sort?: string;
}
