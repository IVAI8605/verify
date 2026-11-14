// Copyright (c) 2024-2026 IVAI8605. 查重助手 verify — 飞牛 NAS 重复文件清理工具。
// SPDX-License-Identifier: MIT

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
)

// imgHashes 把 4 个 uint64 序列化为 img_hash 存储格式。
func imgHashes(a, b, c, d uint64) string {
	f := func(v uint64) string { return strconv.FormatUint(v, 16) }
	return f(a) + "|" + f(b) + "|" + f(c) + "|" + f(d)
}

func insertImgFile(t *testing.T, s *Store, path, ext string) int64 {
	t.Helper()
	id, err := s.UpsertOne(context.Background(), FileInfo{Path: path, Volume: "/v", Name: base(path), Ext: ext, Size: 100, Mtime: 12345, Ctime: 12345}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestImgCandidatesLifecycle 候选拉取、写入、size+mtime 失效。
func TestImgCandidatesLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	id1 := insertImgFile(t, s, "/v/a.jpg", "jpg")
	insertImgFile(t, s, "/v/b.txt", "txt") // 非图片扩展名
	id3 := insertImgFile(t, s, "/v/c.png", "png")

	cands, err := s.ImgCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("应只有 2 个图片候选，实际 %d", len(cands))
	}

	// 写入指纹
	if err := s.SetImgBatch(ctx, []ImgResult{
		{ID: id1, Hash: imgHashes(0x11, 0x22, 0x33, 0x44), W: 100, H: 80, Size: 100, Mtime: 12345},
		{ID: id3, Err: "无法解码", Size: 100, Mtime: 12345},
	}); err != nil {
		t.Fatal(err)
	}
	cands, _ = s.ImgCandidates(ctx, 100)
	if len(cands) != 0 {
		t.Fatalf("已处理且未变化，候选应为 0，实际 %d", len(cands))
	}

	// 同内容替换：size 不变、mtime 变 → 必须重算（probe_cache 同款坑）
	if _, err := s.db.Exec(`UPDATE files SET mtime=99999 WHERE id=?`, id1); err != nil {
		t.Fatal(err)
	}
	cands, _ = s.ImgCandidates(ctx, 100)
	if len(cands) != 1 || cands[0].ID != id1 {
		t.Fatalf("mtime 变化后应重算指纹，实际 %d 个", len(cands))
	}
}

// TestPhotoGroups 聚类：相同指纹同组、超阈值不进组、单文件不成组。
func TestPhotoGroups(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	// 组1：三份拷贝（哈希整串相同）
	ids := []int64{}
	for _, p := range []string{"/v/g1/a.jpg", "/v/g1/b.jpg", "/v/g2/c.jpg"} {
		ids = append(ids, insertImgFile(t, s, p, "jpg"))
	}
	// 组2：与组1 旋转对应（单值哈希共享、整串不同）。
	// 用 64 位随机值，避免短值意外构成镜像关系（如 mirror(0xcccc)=0x3333）。
	idRot := insertImgFile(t, s, "/v/g3/d.jpg", "jpg")
	// 孤立图
	idSolo := insertImgFile(t, s, "/v/solo.jpg", "jpg")

	const hA = 0xa1b2c3d4e5f60718
	const hB = 0x19283a4b5c6d7e8f
	const hC = 0x0fedcba987654321
	const hD = 0x13579bdf2468ace0
	items := []ImgResult{
		{ID: ids[0], Hash: imgHashes(hA, hB, hC, hD), Size: 100, Mtime: 12345},
		{ID: ids[1], Hash: imgHashes(hA, hB, hC, hD), Size: 100, Mtime: 12345},
		{ID: ids[2], Hash: imgHashes(hA, hB, hC, hD), Size: 100, Mtime: 12345},
		// 旋转 90° 的图：哈希序列循环移位——与组1 共享单值哈希，整串不同
		{ID: idRot, Hash: imgHashes(hB, hC, hD, hA), Size: 100, Mtime: 12345},
		// 独立指纹
		{ID: idSolo, Hash: imgHashes(0x0102030405060708, 0x090a0b0c0d0e0f10, 0x1112131415161718, 0x191a1b1c1d1e1f20), Size: 100, Mtime: 12345},
	}
	if err := s.SetImgBatch(ctx, items); err != nil {
		t.Fatal(err)
	}

	groups, n, err := s.PhotoGroups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(groups) != 1 {
		t.Fatalf("应聚出 1 组，实际 %d 组（n=%d）", len(groups), n)
	}
	g := groups[0]
	if g.Count != 4 {
		t.Fatalf("旋转对应的两份应并入同组，实际 %d 个文件", g.Count)
	}
}

// TestPhotoGroupsDistance 精确距离过滤：共享桶但距离超限的不进组。
func TestPhotoGroupsDistance(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	// 链式共享：A、B 共享 hA（距离 0 成组），C 与 B 共享全 1 哈希并入簇，
	// 但 C 与代表元 A 的精确距离约 33 > 10 → C 应被精确阶段剔除。
	idA := insertImgFile(t, s, "/v/x.jpg", "jpg")
	idB := insertImgFile(t, s, "/v/y.jpg", "jpg")
	idC := insertImgFile(t, s, "/v/z.jpg", "jpg")
	if err := s.SetImgBatch(ctx, []ImgResult{
		{ID: idA, Hash: imgHashes(0xa1b2c3d4e5f60718, 0, 0, 0), Size: 1, Mtime: 1},
		{ID: idB, Hash: imgHashes(0xa1b2c3d4e5f60718, 0xffffffffffffffff, 0xffffffffffffffff, 0xffffffffffffffff), Size: 1, Mtime: 1},
		{ID: idC, Hash: imgHashes(0xffffffffffffffff, 0xffffffffffffffff, 0xffffffffffffffff, 0xffffffffffffffff), Size: 1, Mtime: 1},
	}); err != nil {
		t.Fatal(err)
	}
	groups, _, err := s.PhotoGroups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("精确阶段应剔除与代表元距离超限的成员，实际 %d 组: %+v", len(groups), groups)
	}
}

// TestPhotoGroupsScale 一万张随机指纹分桶性能：秒级完成（验收项）。
func TestPhotoGroupsScale(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过")
	}
	ctx := context.Background()
	s := openHealthStore(t)
	var items []ImgResult
	for i := 0; i < 10000; i++ {
		p := fmt.Sprintf("/v/img/%05d.jpg", i)
		id := insertImgFile(t, s, p, "jpg")
		r := uint64(i*2654435761) & 0xffffffffffffffff
		items = append(items, ImgResult{ID: id, Hash: imgHashes(r, r^0x5555, r^0xaaaa, r^0xffff), Size: 1, Mtime: 1})
	}
	if err := s.SetImgBatch(ctx, items); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.PhotoGroups(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 无时间断言（CI 抖动），能跑完即验证了分桶路径；本地手测秒级。
}

// insertWithHash 插入带 hash 的文件行（文件夹查重测试用）。
func insertWithHash(t *testing.T, s *Store, path, hash string) {
	t.Helper()
	_, err := s.UpsertOne(context.Background(), FileInfo{
		Path: path, Volume: "/v", Name: base(path), Ext: "txt", Size: 10, Mtime: 1, Ctime: 1,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE files SET hash=? WHERE path=?`, hash, path); err != nil {
		t.Fatal(err)
	}
}

// TestFolderGroupsRequiresTwoDirs 文件夹重复必须 ≥2 个不同目录：
// 同一目录内的重复文件（不同文件名、相同内容）属于精确查重，不构成文件夹组。
func TestFolderGroupsRequiresTwoDirs(t *testing.T) {
	ctx := context.Background()
	s := openHealthStore(t)

	// d1 与 d2 文件集合相同（a.txt+b.txt）→ 互为副本；d3 集合不同 → 不入组
	insertWithHash(t, s, "/v/d1/a.txt", "h1")
	insertWithHash(t, s, "/v/d1/b.txt", "h2")
	insertWithHash(t, s, "/v/d2/a.txt", "h1")
	insertWithHash(t, s, "/v/d2/b.txt", "h2")
	insertWithHash(t, s, "/v/d3/a.txt", "h1")
	insertWithHash(t, s, "/v/d3/c.txt", "h3")
	// 回归验证「互为副本的 1 个文件夹」bug：solo 目录内两个同内容不同名文件，
	// 旧逻辑会让 solo 自己和自己成组；修复后单目录不成组
	insertWithHash(t, s, "/v/solo/x.txt", "h5")
	insertWithHash(t, s, "/v/solo/y.txt", "h5")

	groups, n, err := s.FolderGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(groups) != 1 {
		t.Fatalf("应只聚出 d1/d2 一组，实际 %d 组: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Count != 4 {
		t.Fatalf("组应有 4 个文件（两目录各 2 个），实际 %d", g.Count)
	}
	for _, f := range g.Files {
		if filepath.Dir(f.Path) == "/v/d3" {
			t.Fatalf("签名不同的 d3 不应入组: %s", f.Path)
		}
	}
}
