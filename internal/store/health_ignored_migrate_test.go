package store

import (
	"context"
	"strings"
	"testing"
)

// TestMigrateHealthIgnoredPrimaryKey 验证 4.2.0 的 path 主键表会迁移成 (path,kind)。
func TestMigrateHealthIgnoredPrimaryKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 4.2.0 的旧表：先删掉新表，再建旧结构并塞一条数据
	if _, err := st.db.ExecContext(ctx, `DROP TABLE health_ignored`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TABLE health_ignored (
  path TEXT PRIMARY KEY, kind TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
  mtime INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO health_ignored(path,kind,size,mtime) VALUES('/v/a.mp4','broken',7,8)`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	st2, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	var sqlText string
	if err := st2.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='health_ignored'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(sqlText, " ", ""), "PRIMARYKEY(path,kind)") {
		t.Fatalf("迁移后应为 (path,kind) 主键，实际 %s", sqlText)
	}
	items, err := st2.ListHealthIgnore(ctx)
	if err != nil || len(items) != 1 || items[0].Path != "/v/a.mp4" {
		t.Fatalf("迁移后数据应保留：%+v err=%v", items, err)
	}
}
