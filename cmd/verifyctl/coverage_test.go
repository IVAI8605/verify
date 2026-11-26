package main

import "testing"

// TestAllCommandsImplemented 断言命令表里每条命令都实现了 Run，杜绝「登记了却执行未接入」的残缺核心。
// 这是防截断保证：任何一条 Run 为 nil，脚本都会把空输出误判为操作已生效。
func TestAllCommandsImplemented(t *testing.T) {
	for _, c := range Commands {
		if c.Run == nil {
			t.Errorf("命令 %s (%s %s) 未实现 Run", c.Name, c.Method, c.Path)
		}
	}
}

// TestCoverageCount 断言命令表与 server.go 路由的覆盖数量关系成立，并按多方法路由口径校准：
//   - 命令表的去重路径集合必须与 server.go 的全部非静态路由路径集合完全一致（不多不少）；
//   - 命令总条数 = 路由路径数 + 同路径额外方法数，多出来的条目必须全部来自「同一路径的
//     额外 HTTP 方法」，而不是凭空多出的孤儿命令。
//
// 静态资源豁免：/、/ui/、/favicon.ico（非 API 路由，CLI 不覆盖）。
func TestCoverageCount(t *testing.T) {
	allRoutes := parseServerRoutes(t, "../internal/web/server.go")
	nonStatic := make(map[string]bool)
	for p := range allRoutes {
		if p == "/" || p == "/ui/" || p == "/favicon.ico" {
			continue
		}
		nonStatic[p] = true
	}

	// 命令表按路径聚合，记录每条路径上的命令条数。
	cmdsPerPath := make(map[string]int)
	for _, c := range Commands {
		cmdsPerPath[c.Path]++
	}

	// 1) 双向路径集合相等：既不能漏路由，也不能有孤儿命令路径。
	for p := range nonStatic {
		if cmdsPerPath[p] == 0 {
			t.Errorf("命令表未覆盖路由路径 %s", p)
		}
	}
	for p := range cmdsPerPath {
		if !nonStatic[p] {
			t.Errorf("命令表出现未注册的孤儿路径 %s", p)
		}
	}

	// 2) 去重路径数两侧相等。
	if len(cmdsPerPath) != len(nonStatic) {
		t.Errorf("去重路径数不一致：命令表 %d，非静态路由 %d", len(cmdsPerPath), len(nonStatic))
	}

	// 3) 多方法口径：命令总条数 = 路由路径数 + 同路径额外方法数。
	extraMethods := 0
	for _, n := range cmdsPerPath {
		if n > 1 {
			extraMethods += n - 1
		}
	}
	wantTotal := len(nonStatic) + extraMethods
	if len(Commands) != wantTotal {
		t.Errorf("命令总条数 %d 与「路由路径 %d + 额外方法 %d = %d」不符",
			len(Commands), len(nonStatic), extraMethods, wantTotal)
	}
	if len(Commands) < len(nonStatic) {
		t.Errorf("命令总条数 %d 少于路由路径数 %d", len(Commands), len(nonStatic))
	}

	t.Logf("非静态路由路径 %d 条，命令 %d 条（其中同路径额外方法 %d 条）",
		len(nonStatic), len(Commands), extraMethods)
}
