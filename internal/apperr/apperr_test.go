// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package apperr

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestFormatCommonErrors(t *testing.T) {
	notExist := fmt.Errorf("open /x: %w", fs.ErrNotExist)
	if got := Format(notExist); !strings.HasPrefix(got, "文件或目录不存在（") || !strings.HasSuffix(got, "）") {
		t.Fatalf("不存在错误格式错误: %q", got)
	}
	cases := []struct {
		err  error
		want string
	}{
		{fs.ErrPermission, "没有读取权限（permission denied）"},
		{fmt.Errorf("database is locked"), "数据库正忙（database is locked）"},
		{fmt.Errorf("no space left on device"), "存储空间不足（no space left on device）"},
	}
	for _, c := range cases {
		if got := Format(c.err); got != c.want {
			t.Fatalf("Format(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestFormatKeepsChinese(t *testing.T) {
	if got := Format(fmt.Errorf("未发现可扫描的存储卷")); got != "未发现可扫描的存储卷" {
		t.Fatalf("纯中文错误不应加括号，实际 %q", got)
	}
	if got := Format(nil); got != "" {
		t.Fatalf("nil 应返回空字符串，实际 %q", got)
	}
}
