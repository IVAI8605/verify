package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestHelpJSONMatchesRegistry 验证 help-json 输出条数与命令表一致，且每条都带副作用与路径。
func TestHelpJSONMatchesRegistry(t *testing.T) {
	var buf bytes.Buffer
	if err := writeCommandList(&buf); err != nil {
		t.Fatalf("writeCommandList 失败：%v", err)
	}
	var list []Command
	if err := json.Unmarshal(buf.Bytes(), &list); err != nil {
		t.Fatalf("解析 help-json 失败：%v", err)
	}
	if len(list) != len(Commands) {
		t.Fatalf("help-json %d 条 != 命令表 %d 条", len(list), len(Commands))
	}
	for _, c := range list {
		if c.SideEffect == "" || c.Path == "" {
			t.Errorf("%s 缺 side_effect/path", c.Name)
		}
	}
}

// TestHelpJSONFields 验证自描述 JSON 含约定字段，且不含 Run 函数指针序列化残留。
func TestHelpJSONFields(t *testing.T) {
	var buf bytes.Buffer
	if err := writeCommandList(&buf); err != nil {
		t.Fatalf("writeCommandList 失败（%v）", err)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &items); err != nil {
		t.Fatalf("help-json 不是合法 JSON 数组（%v）；输出=%q", err, buf.String())
	}
	if len(items) == 0 {
		t.Fatal("help-json 不应为空")
	}
	for _, key := range []string{"name", "description", "params", "side_effect", "confirm_required", "path"} {
		if _, ok := items[0][key]; !ok {
			t.Errorf("help-json 条目缺字段 %s：%v", key, items[0])
		}
	}
	if _, ok := items[0]["Run"]; ok {
		t.Error("help-json 不应序列化 Run 函数指针")
	}
}

// TestWriteCommandDetail 验证单命令自描述输出能还原命令名与路径。
func TestWriteCommandDetail(t *testing.T) {
	cmd, ok := findCommand("status")
	if !ok {
		t.Fatal("命令表应有 status")
	}
	var buf bytes.Buffer
	if err := writeCommandDetail(&buf, cmd); err != nil {
		t.Fatalf("writeCommandDetail 失败（%v）", err)
	}
	var got Command
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("命令详情不是合法 JSON（%v）；输出=%q", err, buf.String())
	}
	if got.Name != "status" || got.Path != "/api/status" {
		t.Fatalf("命令详情 = %+v；期望 status /api/status", got)
	}
}

// TestHelpJSONFlagWiring 验证 --help-json 走命令清单输出，退出码为 0。
func TestHelpJSONFlagWiring(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"--help-json"}, &buf)
	if code != 0 {
		t.Fatalf("--help-json 退出码=%d，期望 0；输出=%q", code, buf.String())
	}
	var list []Command
	if err := json.Unmarshal(buf.Bytes(), &list); err != nil {
		t.Fatalf("--help-json 输出不是 JSON（%v）；输出=%q", err, buf.String())
	}
	if len(list) != len(Commands) {
		t.Fatalf("--help-json 输出 %d 条 != 命令表 %d 条", len(list), len(Commands))
	}
}
