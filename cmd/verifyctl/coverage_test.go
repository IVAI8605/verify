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

// TestCoverageCount 断言命令表对 server.go 的「路径级」覆盖关系成立：
//   - 命令表去重路径集合与 server.go 全部非静态路由路径集合双向相等（不漏、不孤儿）；
//   - 去重路径数两侧一致；
//   - 命令总条数不少于非静态路由路径数（每条路径至少一条命令）。
//
// 边界说明（必须如实）：本测试只证明「路径级」覆盖，**不证明「方法级」覆盖**。
// server.go 用无方法前缀的 mux.HandleFunc 注册，同一路径支持哪些 HTTP 方法是在各自
// handler 内部用 r.Method 判定的，源码里没有机器可读的 (path,method) 集合；静态解析
// handler 体会误判（漏读/多读），故此处不硬凑方法级断言。
// 方法级覆盖由 TestSecondaryMethodCommandsCovered 兜底：它目前固定校验 5 条次要方法
// （video_tags POST/DELETE、sync/peers DELETE、import/sessions DELETE、rename/presets/ PUT）。
// 若服务端新增多方法路由只接一半，需同步扩充该用例的关键方法清单。
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

	// 命令表按路径去重，记录每条路径是否被覆盖。
	covered := make(map[string]bool)
	for _, c := range Commands {
		covered[c.Path] = true
	}

	// 1) 双向路径集合相等：既不能漏路由，也不能有孤儿命令路径。
	for p := range nonStatic {
		if !covered[p] {
			t.Errorf("命令表未覆盖路由路径 %s", p)
		}
	}
	for p := range covered {
		if !nonStatic[p] {
			t.Errorf("命令表出现未注册的孤儿路径 %s", p)
		}
	}

	// 2) 去重路径数两侧相等。
	if len(covered) != len(nonStatic) {
		t.Errorf("去重路径数不一致：命令表 %d，非静态路由 %d", len(covered), len(nonStatic))
	}

	// 3) 命令总条数不少于路由路径数（每条路由至少一条命令）。
	if len(Commands) < len(nonStatic) {
		t.Errorf("命令总条数 %d 少于非静态路由路径数 %d", len(Commands), len(nonStatic))
	}

	t.Logf("非静态路由路径 %d 条，命令表去重路径 %d 个，命令 %d 条（仅路径级覆盖；方法级由 TestSecondaryMethodCommandsCovered 兜底）",
		len(nonStatic), len(covered), len(Commands))
}
