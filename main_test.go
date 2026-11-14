package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyDataDir 复现 v1.19 改名迁移路径（v1.20.x 三处缺陷的回归验证）。
func TestMigrateLegacyDataDir(t *testing.T) {
	base := t.TempDir() // 模拟 fnOS 应用数据目录
	legacy := filepath.Join(base, "dupclean", "data")
	newDir := filepath.Join(base, "verify", "data")

	// 旧数据：db + 配置（带 volumes）
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "dupclean.db"), []byte("OLDDB"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"volumes":["/vol1/1000/x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// 场景 1（v1.20.0 缺陷）：新目录被 cmd 预建为空目录 → 应接管
	if err := os.MkdirAll(newDir, 0o750); err != nil {
		t.Fatal(err)
	}
	migrateLegacyDataDir(newDir)
	if _, err := os.Stat(filepath.Join(newDir, "verify.db")); err != nil {
		t.Fatalf("空新目录应被接管（v1.20.0 缺陷回归）：%v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("旧目录应被移走")
	}

	// 场景 2（v1.20.1 缺陷）：新目录含「未初始化生成物」（空库+默认配置）→ 应接管
	// 重建旧数据与新目录
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "dupclean.db"), []byte("OLDDB2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "verify.db"), []byte("EMPTY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "config.json"), []byte(`{"volumes":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	migrateLegacyDataDir(newDir)
	b, err := os.ReadFile(filepath.Join(newDir, "verify.db"))
	if err != nil || string(b) != "OLDDB2" {
		t.Fatalf("未初始化新目录应被接管（v1.20.1 缺陷回归）：err=%v db=%q", err, string(b))
	}

	// 场景 3（v1.20.2 缺陷）：新目录有用户数据（volumes 非空）→ 必须跳过防覆盖
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "dupclean.db"), []byte("OLDDB3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "config.json"), []byte(`{"volumes":["/vol1/1000/y"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "verify.db"), []byte("USERDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	migrateLegacyDataDir(newDir)
	b, _ = os.ReadFile(filepath.Join(newDir, "verify.db"))
	if string(b) != "USERDATA" {
		t.Fatal("已初始化的新目录必须跳过防覆盖（v1.20.2 缺陷回归）")
	}
	if _, err := os.Stat(filepath.Join(legacy, "dupclean.db")); err != nil {
		t.Fatal("已初始化场景旧数据不应被动")
	}
}
