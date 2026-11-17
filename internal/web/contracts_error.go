// API 响应契约 —— 错误收敛机制（v4.0.0）
//
// 背景：writeJSON 在 HTTP 出口统一收敛错误文案（SEC-8），
// 把 errno / SQLite 原文换成用户能看懂的中文摘要。
// v4.0.0 起响应体从 map[string]any 换成具名 struct，
// 收敛逻辑要能同时认两种形态，否则会静默失效——
// 失效意味着底层英文错误原文会直接回显给浏览器，是安全性回归。
//
// 机制：带 error 字段的响应体实现本接口，
// 收敛时用 withError 返回一份「替换掉 error 内容」的副本。
// 约定 withError 不修改接收者，避免误改调用方持有的结构。
package web

// errorPayload 是「带 error 字段的失败响应」的统一形态。
//
// 实现要求：
//   - errorText 返回响应体里 error 字段的原始内容（未收敛）；
//   - withError 返回一个副本，其中 error 字段被替换为传入的文案，
//     其它字段原样保留；不得修改接收者自身。
type errorPayload interface {
	errorText() string
	withError(clean string) any
}

// errorResponse 实现 errorPayload。
func (e errorResponse) errorText() string { return e.Error }

func (e errorResponse) withError(clean string) any {
	e.Error = clean
	return e
}

// authErrorResponse 是「需要访问令牌」的响应。
// 单独一个类型是因为它带一个额外的 auth 布尔字段，前端靠它显示登录框。
type authErrorResponse struct {
	Error string `json:"error"`
	Auth  bool   `json:"auth"`
}

func (e authErrorResponse) errorText() string { return e.Error }

func (e authErrorResponse) withError(clean string) any {
	e.Error = clean
	return e
}

// countErrorResponse 是「带计数的失败响应」（如扫描冲突时的 n）。
type countErrorResponse struct {
	Error string `json:"error"`
	N     int    `json:"n"`
}

func (e countErrorResponse) errorText() string { return e.Error }

func (e countErrorResponse) withError(clean string) any {
	e.Error = clean
	return e
}

// searchReadyErrorResponse 是「搜索结果未就绪」的响应。
// 带 state 与 detail，前端据此区分「还没搜」与「搜不了」。
type searchReadyErrorResponse struct {
	Error  string `json:"error"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

func (e searchReadyErrorResponse) errorText() string { return e.Error }

func (e searchReadyErrorResponse) withError(clean string) any {
	e.Error = clean
	return e
}
