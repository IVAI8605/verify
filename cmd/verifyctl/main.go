package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version 由构建期 -X main.version=<版本> 注入；未注入时默认为 dev。
var version = "dev"

// run 执行一次 CLI 调用：解析全局参数并分发子命令，输出写到 out，返回进程退出码。
// 令牌只作为请求凭据保留在参数里，任何输出路径都不回显其值。
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("verifyctl", flag.ContinueOnError)
	fs.SetOutput(out)

	// 一期仅解析全局参数，具体行为由后续任务接入命令注册表与 HTTP 客户端。
	fs.String("url", "", "Verify 实例地址，例如 http://127.0.0.1:8898")
	fs.String("token", "", "访问令牌，仅用于 Authorization: Bearer，不回显")
	fs.String("data-dir", "", "Verify 数据目录，供本地只读辅助使用")
	fs.String("format", "", "输出格式：table 或 json")
	fs.Bool("raw", false, "输出原始响应体")
	helpJSON := fs.Bool("help-json", false, "以 JSON 格式输出帮助")

	fs.Usage = func() {
		fmt.Fprintln(out, "用法：verifyctl [全局参数] <子命令>")
		fmt.Fprintln(out, "全局参数：")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(out, "参数解析失败（%v）\n", err)
		return 2
	}

	// --help-json 只做自描述：无论后面是否带子命令，都输出全量命令清单并正常退出。
	if *helpJSON {
		if err := writeCommandList(out); err != nil {
			fmt.Fprintf(out, "输出命令清单失败（%v）\n", err)
			return 1
		}
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 0
	}

	switch rest[0] {
	case "version":
		fmt.Fprintln(out, version)
		return 0
	default:
		if cmd, ok := findCommand(rest[0]); ok {
			// 执行逻辑尚未接入：必须明确报错，避免脚本把空输出误判为操作已生效。
			fmt.Fprintf(out, "命令 %s 的执行尚未接入，本阶段仅解析命令名\n", cmd.Name)
			return 2
		}
		fmt.Fprintf(out, "未知子命令：%s\n", rest[0])
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
