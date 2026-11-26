package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// routeKey 是命令表条目的唯一键：同一 path 的不同 HTTP 方法是不同命令，互不覆盖。
type routeKey struct {
	path   string
	method string
}

// 路由正则：抽取 server.go 里 mux.HandleFunc("路径", ...) 注册的路径。
var serverRouteRE = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)"`)

// parseServerRoutes 从 server.go 源码里抽出全部注册路由，返回「路径 → 是否出现」。
func parseServerRoutes(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		// go test 的工作目录是包目录 cmd/verifyctl，简报里的相对路径向上少一级；
		// 这里兼容从包目录与仓库根目录两种起点启动测试。
		alt := filepath.Join("..", path)
		src, err = os.ReadFile(alt)
	}
	if err != nil {
		t.Fatalf("读取 %s 失败（%v）", path, err)
	}
	routes := map[string]bool{}
	for _, m := range serverRouteRE.FindAllStringSubmatch(string(src), -1) {
		routes[m[1]] = true
	}
	if len(routes) == 0 {
		t.Fatalf("%s 未抽出任何路由，正则可能已失效", path)
	}
	return routes
}

// TestCommandsCoverAllRoutes 断言命令表覆盖 server.go 全部非静态路由，防止「残缺核心」。
// 同一个 path 允许多个 Command（如 GET 读 / DELETE 删），覆盖以 (path, method) 为最小单位。
func TestCommandsCoverAllRoutes(t *testing.T) {
	routes := parseServerRoutes(t, "../internal/web/server.go")
	covered := map[routeKey]bool{}
	coveredPaths := map[string]bool{}
	for _, cmd := range Commands {
		covered[routeKey{cmd.Path, cmd.Method}] = true
		coveredPaths[cmd.Path] = true
	}
	for r := range routes {
		if strings.HasPrefix(r, "/ui/") || r == "/" || r == "/favicon.ico" {
			continue
		}
		if !coveredPaths[r] {
			t.Errorf("命令表未覆盖路由 %s", r)
		}
	}
}

// TestSecondaryMethodCommandsCovered 防止多方法路由的次要方法永久不可达：
// 这些写/破坏性操作与同 path 的 GET 命令是不同能力，缺一条即算残缺。
func TestSecondaryMethodCommandsCovered(t *testing.T) {
	want := []routeKey{
		{"/api/video_tags", "POST"},
		{"/api/video_tags", "DELETE"},
		{"/api/sync/peers", "DELETE"},
		{"/api/import/sessions", "DELETE"},
		{"/api/rename/presets/", "PUT"},
	}
	covered := map[routeKey]bool{}
	for _, cmd := range Commands {
		covered[routeKey{cmd.Path, cmd.Method}] = true
	}
	for _, key := range want {
		if !covered[key] {
			t.Errorf("命令表未覆盖次要方法 %s %s", key.method, key.path)
		}
	}
}

// TestFindCommand 验证按名字查命令能找到已登记条目、找不到时返回 false。
func TestFindCommand(t *testing.T) {
	cmd, ok := findCommand("status")
	if !ok || cmd.Path != "/api/status" {
		t.Fatalf("findCommand(status) = %+v, %v；期望 /api/status, true", cmd, ok)
	}
	if _, ok := findCommand("不存在的命令"); ok {
		t.Fatal("findCommand 对不存在的命令应返回 false")
	}
}

// TestCommandsWellFormed 验证命令表条目字段完整、名字唯一、(path, method) 唯一、副作用取值合法。
func TestCommandsWellFormed(t *testing.T) {
	validEffects := map[string]bool{"none": true, "trigger": true, "write": true, "destructive": true}
	names := map[string]bool{}
	routes := map[routeKey]bool{}
	for _, cmd := range Commands {
		if cmd.Name == "" || cmd.Path == "" || cmd.Method == "" || cmd.SideEffect == "" {
			t.Errorf("命令字段不全：%+v", cmd)
		}
		if !validEffects[cmd.SideEffect] {
			t.Errorf("命令 %s 的副作用取值非法：%s", cmd.Name, cmd.SideEffect)
		}
		if names[cmd.Name] {
			t.Errorf("命令名重复：%s", cmd.Name)
		}
		names[cmd.Name] = true
		key := routeKey{cmd.Path, cmd.Method}
		if routes[key] {
			t.Errorf("命令路径+方法重复：%s %s", cmd.Method, cmd.Path)
		}
		routes[key] = true
		if cmd.Confirm && cmd.SideEffect != "destructive" {
			t.Errorf("命令 %s 要求确认但副作用不是 destructive", cmd.Name)
		}
	}
}
