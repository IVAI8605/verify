// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"testing"
)

// TestUpsertVideoTagStableID 验证同名标签二次 upsert 仍返回同一 ID。
// SQLite 在 ON CONFLICT UPDATE 分支下 LastInsertId 不可靠，必须回查 name 拿准 ID。
func TestUpsertVideoTagStableID(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	id1, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExact, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id1 <= 0 {
		t.Fatalf("首次 upsert 应返回正 ID，实际 %d", id1)
	}

	id2, err := s.UpsertVideoTag(ctx, "剧集名", TagModeSimilar, TagSourceDirname, "", false, 3)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("同名 upsert 应复用 ID：%d != %d", id2, id1)
	}

	tags, err := s.GetVideoTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Fatalf("同名 upsert 不应新增行，实际 %d 行", len(tags))
	}
	if tags[0].MatchMode != TagModeSimilar || tags[0].JoinGroup || tags[0].SortOrder != 3 {
		t.Fatalf("upsert 未更新字段: %+v", tags[0])
	}
}

// TestDeleteVideoTagCascades 验证删除标签时级联清理 file_tags 关联值，不留孤儿行。
func TestDeleteVideoTagCascades(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	id, err := s.UpsertVideoTag(ctx, "年份", TagModeExact, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fid := range []int64{1, 2} {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`, fid, id, "2024"); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteVideoTag(ctx, id); err != nil {
		t.Fatal(err)
	}

	var left int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM file_tags WHERE tag_id=?`, id).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("file_tags 应级联删除，仍残留 %d 行", left)
	}
	tags, err := s.GetVideoTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 0 {
		t.Fatalf("标签应已删除，仍剩 %d 行", len(tags))
	}
}

// insertTestFiles 插入若干文件记录，返回按插入顺序的 id。
// 只填 path/name/size，其余字段用 schema 默认值，够标签计算与聚组使用。
func insertTestFiles(t *testing.T, s *Store, files [][2]any) []int64 {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, len(files))
	for _, f := range files {
		res, err := s.DB().ExecContext(ctx,
			`INSERT INTO files(path, name, size) VALUES(?,?,?)`, f[0], f[1], 100)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestAttachFileTags 验证结果页标签徽标的数据来源（v2.8.0）：
// 只填参与分组的标签，且按文件名分组之外的文件也能拿到值。
func TestAttachFileTags(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ids := insertTestFiles(t, s, [][2]any{
		{"/vol6/资源/剧A/e01.mkv", "e01.mkv"},
		{"/vol6/资源/剧A/e02.mkv", "e02.mkv"},
	})
	joinID, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExact, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	hiddenID, err := s.UpsertVideoTag(ctx, "仅显示", TagModeDisplayOnly, TagSourceDirname, "", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []int64{joinID, hiddenID} {
		for _, fid := range ids {
			if _, err := s.DB().ExecContext(ctx,
				`INSERT OR REPLACE INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`, fid, tid, "剧A"); err != nil {
				t.Fatal(err)
			}
		}
	}

	groups := []Group{{Key: "g1", Files: []FileRow{{ID: ids[0], Path: "/vol6/资源/剧A/e01.mkv", Name: "e01.mkv"},
		{ID: ids[1], Path: "/vol6/资源/剧A/e02.mkv", Name: "e02.mkv"}}}}
	if err := s.AttachFileTags(ctx, groups); err != nil {
		t.Fatal(err)
	}
	for _, f := range groups[0].Files {
		if len(f.Tags) != 1 {
			t.Fatalf("应只附加参与分组的标签，实际 %d 个: %+v", len(f.Tags), f.Tags)
		}
		if f.Tags[0].Name != "剧集名" || f.Tags[0].Value != "剧A" {
			t.Fatalf("标签内容不对: %+v", f.Tags[0])
		}
	}
}

// TestComputeFileTagsDirname 验证目录名派生：同一目录的文件得到同一个标签值，重算幂等。
func TestComputeFileTagsDirname(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	insertTestFiles(t, s, [][2]any{
		{"/vol6/资源/剧A/e01.mkv", "e01.mkv"},
		{"/vol6/资源/剧A/e02.mkv", "e02.mkv"},
		{"/vol6/资源/剧B/e01.mkv", "e01.mkv"},
	})
	id, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExact, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ComputeFileTags(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应写入 3 条标签值，实际 %d", n)
	}
	var cnt int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file_tags WHERE tag_id=? AND value=?`, id, "剧A").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 2 {
		t.Fatalf("剧A 应有 2 个文件，实际 %d", cnt)
	}

	// 重复重算不应产生重复行（幂等）
	n2, err := s.ComputeFileTags(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != n {
		t.Fatalf("幂等重算数量不一致：%d != %d", n2, n)
	}
	var total int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM file_tags WHERE tag_id=?`, id).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("重算后 file_tags 应仍为 3 行，实际 %d", total)
	}
}

// TestComputeFileTagsRegex 验证文件名正则取值，并确认非法规则在保存阶段就被拒绝。
func TestComputeFileTagsRegex(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	insertTestFiles(t, s, [][2]any{
		{"/vol6/资源/某剧集.S01E02.mkv", "某剧集.S01E02.mkv"},
		{"/vol6/资源/某剧集.S01E03.mkv", "某剧集.S01E03.mkv"},
		{"/vol6/资源/无关文件.txt", "无关文件.txt"},
	})
	// 无捕获组：应拒绝
	if _, err := s.UpsertVideoTag(ctx, "坏规则", TagModeExact, TagSourceFilenameRegex, `S\d{2}E\d{2}`, true, 0); err == nil {
		t.Fatal("没有捕获组的规则应被拒绝")
	}
	// 正则语法错误：应拒绝
	if _, err := s.UpsertVideoTag(ctx, "坏规则2", TagModeExact, TagSourceFilenameRegex, `(`, true, 0); err == nil {
		t.Fatal("语法错误的正则应被拒绝")
	}

	id, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExact, TagSourceFilenameRegex, `^(.+?)\.S\d{2}E\d{2}`, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ComputeFileTags(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("只有 2 个文件匹配规则，实际写入 %d", n)
	}
	var cnt int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file_tags WHERE tag_id=? AND value=?`, id, "某剧集").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 2 {
		t.Fatalf("正则捕获值应有 2 条，实际 %d", cnt)
	}
}

// TestTagGroupsModes 验证四种匹配模式的聚组差异：精确区分空白变体，含空白变体与相似模式会合并。
func TestTagGroupsModes(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ids := insertTestFiles(t, s, [][2]any{
		{"/vol6/资源/a/e01.mkv", "e01.mkv"},
		{"/vol6/资源/a/e02.mkv", "e02.mkv"},
		{"/vol6/资源/b/e01.mkv", "e01.mkv"},
		{"/vol6/资源/b/e02.mkv", "e02.mkv"},
	})
	cases := []struct {
		mode string
		vals []string
		want int // 期望的组数（每组至少 2 个文件）
	}{
		{TagModeExact, []string{"某剧集", "某剧集", "某 剧集", "某 剧集"}, 2},
		{TagModeExactWS, []string{"某剧集", "某剧集", "某 剧集", "某 剧集"}, 1},
		{TagModeSimilar, []string{"某剧集", "某剧集", "某剧集 第二季", "某剧集 第二季"}, 1},
		{TagModeDisplayOnly, []string{"某剧集", "某剧集", "某 剧集", "某 剧集"}, 0},
	}
	for _, c := range cases {
		tagID, err := s.UpsertVideoTag(ctx, "标签-"+c.mode, c.mode, TagSourceDirname, "", true, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range c.vals {
			if _, err := s.DB().ExecContext(ctx,
				`INSERT OR REPLACE INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`, ids[i], tagID, v); err != nil {
				t.Fatal(err)
			}
		}
		groups, total, err := s.TagGroups(ctx, tagID, GroupQuery{Page: 1, PageSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		if int(total) != c.want {
			t.Fatalf("模式 %s 期望 %d 组，实际 %d", c.mode, c.want, total)
		}
		if len(groups) != c.want {
			t.Fatalf("模式 %s 返回组数不一致：%d != %d", c.mode, len(groups), c.want)
		}
	}
}

// TestTagGroupsSkipsSingleFile 验证只有一个文件的标签值不构成分组。
func TestTagGroupsSkipsSingleFile(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	id, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExact, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	fileIDs := insertTestFiles(t, s, [][2]any{
		{"/vol6/资源/单集/e01.mkv", "e01.mkv"},
		{"/vol6/资源/多集/e01.mkv", "e01.mkv"},
		{"/vol6/资源/多集/e02.mkv", "e02.mkv"},
	})
	vals := []string{"单集", "多集", "多集"}
	for i, v := range vals {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT OR REPLACE INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`, fileIDs[i], id, v); err != nil {
			t.Fatal(err)
		}
	}
	groups, total, err := s.TagGroups(ctx, id, GroupQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 {
		t.Fatalf("应只剩「多集」1 组，实际 total=%d len=%d", total, len(groups))
	}
	if groups[0].Name != "多集" || groups[0].Count != 2 {
		t.Fatalf("分组内容不对: %+v", groups[0])
	}
}

// TestTagGroupMembers 验证按分组键取回组内全部路径（v3.10.8，P1-3）。
// 整组隔离前的「至少保留一个」校验依赖它，键不存在时必须返回空而不是随便给一组。
func TestTagGroupMembers(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	tagID, err := s.UpsertVideoTag(ctx, "剧集名", TagModeExactWS, TagSourceDirname, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	fileIDs := insertTestFiles(t, s, [][2]any{
		{"/vol6/标签/a/e01.mkv", "e01.mkv"},
		{"/vol6/标签/b/e01.mkv", "e01.mkv"},
		{"/vol6/标签/c/e02.mkv", "e02.mkv"},
	})
	// 去空白模式下「某 剧集」与「某剧集」应归为同一组
	vals := []string{"某 剧集", "某剧集", "别的剧集"}
	for i, v := range vals {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT OR REPLACE INTO file_tags(file_id, tag_id, value) VALUES(?,?,?)`, fileIDs[i], tagID, v); err != nil {
			t.Fatal(err)
		}
	}
	groups, _, err := s.TagGroups(ctx, tagID, GroupQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("期望 1 个分组，实际 %d", len(groups))
	}
	paths, err := s.TagGroupMembers(ctx, tagID, groups[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("期望返回 2 个成员路径，实际 %d：%v", len(paths), paths)
	}
	for _, p := range paths {
		if p != "/vol6/标签/a/e01.mkv" && p != "/vol6/标签/b/e01.mkv" {
			t.Fatalf("返回了不该属于该组的路径：%s", p)
		}
	}
	if got, err := s.TagGroupMembers(ctx, tagID, groups[0].Key+"-不存在"); err != nil || got != nil {
		t.Fatalf("未知键应返回空，实际 %v / %v", got, err)
	}
}
