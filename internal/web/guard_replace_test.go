// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGuardReplacementChars 覆盖 v3.11.0 的编码异常写保护：
// 前端回传被替换成 U+FFFD 的路径时必须在入口拒绝并给中文原因；
// 正常请求体必须原样放行，不能影响其它写接口。
func TestGuardReplacementChars(t *testing.T) {
	badBody := `{"paths":["/vol1/bad-\ufffd.mkv"]}`
	r := httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(badBody))
	r.ContentLength = int64(len(badBody))
	rec := httptest.NewRecorder()
	if guardReplacementChars(rec, r) {
		t.Fatal("含 U+FFFD 的写请求应被拒绝")
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "路径包含无法识别的文件名字符") {
		t.Fatalf("期望 400 与中文原因，实际 %d %s", rec.Code, rec.Body.String())
	}

	goodBody := `{"paths":["/vol1/ok.mkv"]}`
	r = httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(goodBody))
	r.ContentLength = int64(len(goodBody))
	rec = httptest.NewRecorder()
	if !guardReplacementChars(rec, r) {
		t.Fatalf("正常请求应放行，实际 %d %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("放行时不应写响应，实际 %d", rec.Code)
	}

	// 外部脚本可能把替换字符写成 JSON 转义 \ufffd，原始字节里看不到 U+FFFD，也必须拦。
	escBody := `{"paths":["/vol1/bad-\ufffd.mkv"]}`
	r = httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(escBody))
	r.ContentLength = int64(len(escBody))
	rec = httptest.NewRecorder()
	if guardReplacementChars(rec, r) {
		t.Fatal("JSON 转义形式的 U+FFFD 也应被拒绝")
	}

	// 只读的评分/解释接口即使带坏路径也不能整包 400，否则智能保留建议会整批失败。
	for _, path := range []string{"/api/retain/score", "/api/retain/explain"} {
		r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(badBody))
		r.ContentLength = int64(len(badBody))
		rec = httptest.NewRecorder()
		if !guardReplacementChars(rec, r) {
			t.Fatalf("%s 是只读接口，应放行，实际 %d", path, rec.Code)
		}
	}
}

// TestGuardReplacementCharsChunked 覆盖第十一轮 P2-3：分块传输没有 Content-Length，
// 原实现会直接放行；现在必须读全量校验，且放行后请求体仍可被 handler 读取。
func TestGuardReplacementCharsChunked(t *testing.T) {
	badBody := "{\"paths\":[\"/vol1/bad-\uFFFD.mkv\"]}"
	r := httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(badBody))
	r.ContentLength = -1
	r.TransferEncoding = []string{"chunked"}
	rec := httptest.NewRecorder()
	if guardReplacementChars(rec, r) {
		t.Fatal("分块传输的坏路径应被拒绝")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", rec.Code)
	}

	goodBody := `{"paths":["/vol1/ok.mkv"]}`
	r = httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(goodBody))
	r.ContentLength = -1
	r.TransferEncoding = []string{"chunked"}
	rec = httptest.NewRecorder()
	if !guardReplacementChars(rec, r) {
		t.Fatalf("正常分块请求应放行，实际 %d", rec.Code)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != goodBody {
		t.Fatalf("放行后请求体应可重复读取，实际 %q err=%v", got, err)
	}
}

// TestGuardReplacementCharsLargeBody 覆盖第十一轮 P2-3：批量改名动辄 5000+ 文件，
// 请求体超过原 1MiB 上限时曾被直接放行；现在必须照常拦截。
func TestGuardReplacementCharsLargeBody(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"paths":[`)
	// 填充到 2MiB 以上，坏路径放在末尾，确保是「读全量」而不是只查开头才命中。
	for b.Len() < 2<<20 {
		b.WriteString(`"/vol1/ok-file.mkv",`)
	}
	b.WriteString(`"/vol1/bad-` + "\uFFFD" + `.mkv"]}`)
	body := b.String()
	r := httptest.NewRequest(http.MethodPost, "/api/trash", strings.NewReader(body))
	r.ContentLength = -1
	rec := httptest.NewRecorder()
	if guardReplacementChars(rec, r) {
		t.Fatal("超过 1MiB 的请求体也必须校验")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400，实际 %d", rec.Code)
	}
}

// TestWritePathPrefixes 覆盖第十一轮 P2-2：白名单曾经写成 "/api/rename/undo/"，
// 拼出 "//" 永不命中；这里断言名单写法合法，且每条写路由都能被 isWritePath 识别。
func TestWritePathPrefixes(t *testing.T) {
	for _, p := range writePathPrefixes {
		if strings.Contains(p, "//") || strings.HasSuffix(p, "/") {
			t.Fatalf("写路径白名单存在非法写法：%q（会导致拼接出双斜杠，永不命中）", p)
		}
	}
	writeRoutes := []string{
		"/api/trash", "/api/trash/restore", "/api/trash/delete", "/api/trash/empty",
		"/api/groups/rename", "/api/groups/rename/undo", "/api/file/rename",
		"/api/rename/execute", "/api/rename/undo/1720000000000",
		"/api/move", "/api/move/undo", "/api/move/dir", "/api/move/dir/undo", "/api/relink", "/api/trim",
		"/api/transcode", "/api/transcode/cancel", "/api/fanjian", "/api/fanjian/undo",
		"/api/folder_rename", "/api/folder_rename/undo",
		"/api/health/rename", "/api/health/fix-encoding", "/api/health/trash", "/api/health/undo",
		"/api/emptydir/clean", "/api/fs/mkdir", "/api/cleanup/run",
	}
	for _, p := range writeRoutes {
		if !isWritePath(p) {
			t.Fatalf("写路由 %s 未被 isWritePath 命中，编码异常护栏会漏检", p)
		}
	}
	for _, p := range []string{
		"/api/trash/preview", "/api/trash/export", "/api/move/preview", "/api/move/dir/preview", "/api/trim/probe",
		"/api/folder_rename/preview", "/api/rename/preview", "/api/relink/preview",
		"/api/retain/score", "/api/retain/explain",
	} {
		if isWritePath(p) {
			t.Fatalf("只读接口 %s 被误判为写路径", p)
		}
	}
}
