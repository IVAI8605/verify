// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package web

// 回归 v4.1.1：page_size 上限放宽到 1000 后，store 的默认 200 cap 没同步，
// 导致 page_size=1000 只是回显、实际只返回 200 条。这里锁住真实条数。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroupPageSize1000ReturnsMoreThan200(t *testing.T) {
	s, st, root := newTrashAPIServer(t)
	ctx := context.Background()
	// 300 个不同 hash，每个 hash 2 个成员 = 300 个重复组（单成员组会被过滤掉）。
	for i := 1; i <= 300; i++ {
		for j := 1; j <= 2; j++ {
			p := fmt.Sprintf("%s/f-%03d-%d.mkv", root, i, j)
			if _, err := st.DB().ExecContext(ctx,
				`INSERT INTO files(path,volume,name,size,mtime,ext,status,hash,quick)
				 VALUES(?,?,?,?,?,?,'active',?,?)`,
				p, root, fmt.Sprintf("f-%03d-%d.mkv", i, j), 100, 1, ".mkv",
				fmt.Sprintf("h%03d", i), fmt.Sprintf("q%03d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}

	rec := httptest.NewRecorder()
	s.handleGroups(rec, httptest.NewRequest(http.MethodGet, "/api/groups?page_size=1000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	var body struct {
		Groups []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Groups) != 300 {
		t.Fatalf("page_size=1000 应返回全部 300 组，实际 %d", len(body.Groups))
	}
}
