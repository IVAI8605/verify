package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPostPathsParamSendsJSONArray 断言注册表里 POST 命令的 paths 参数真正以 JSON 数组进请求体，
// 而不是把 '["/a","/b"]' 当普通字符串发出去。直接取真实命令表，注册表漏改即失败。
func TestPostPathsParamSendsJSONArray(t *testing.T) {
	cmd, ok := findCommand("health trash")
	if !ok {
		t.Fatal("找不到命令 health trash")
	}
	if cmd.Method != http.MethodPost {
		t.Fatalf("health trash 应为 POST，实际 %s", cmd.Method)
	}
	if typ := paramType(cmd, "paths"); typ != "json" {
		t.Fatalf("health trash 的 paths 类型应为 json，实际 %q", typ)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"paths": `["/a","/b"]`,
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}

	arr, ok := got["paths"].([]any)
	if !ok {
		t.Fatalf("paths 未以 JSON 数组发送，实际类型 %T 值 %#v", got["paths"], got["paths"])
	}
	if len(arr) != 2 || arr[0] != "/a" || arr[1] != "/b" {
		t.Fatalf("paths 数组内容错误：%#v", arr)
	}
}

// TestPostWorkerParamSendsInt 断言 health scan 的 workers 参数以真整数进请求体。
func TestPostWorkerParamSendsInt(t *testing.T) {
	cmd, ok := findCommand("health scan")
	if !ok {
		t.Fatal("找不到命令 health scan")
	}
	if typ := paramType(cmd, "workers"); typ != "int" {
		t.Fatalf("health scan 的 workers 类型应为 int，实际 %q", typ)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("解析请求体失败：%v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	if _, _, err := c.Do(context.Background(), cmd, map[string]string{
		"workers": "4",
	}); err != nil {
		t.Fatalf("Do 失败：%v", err)
	}

	num, ok := got["workers"].(float64)
	if !ok {
		t.Fatalf("workers 未以整数发送，实际类型 %T 值 %#v", got["workers"], got["workers"])
	}
	if num != 4 {
		t.Fatalf("workers 数值错误：%v", num)
	}
}

// TestGetArraysStayString 守住「GET 只能走查询串、数组不能 JSON」这条线：
// trash export 是 GET，ids 必须仍是 string（逗号分隔），不得被改成 json。
func TestGetArraysStayString(t *testing.T) {
	cmd, ok := findCommand("trash export")
	if !ok {
		t.Fatal("找不到命令 trash export")
	}
	if cmd.Method != http.MethodGet {
		t.Fatalf("trash export 应为 GET，实际 %s", cmd.Method)
	}
	if typ := paramType(cmd, "ids"); typ != "string" {
		t.Fatalf("GET 的 trash export ids 应保持 string，实际 %q", typ)
	}
}

// paramType 返回命令里某参数声明的类型，找不到返回空串。
func paramType(cmd Command, name string) string {
	for _, p := range cmd.Params {
		if p.Name == name {
			return p.Type
		}
	}
	return ""
}
