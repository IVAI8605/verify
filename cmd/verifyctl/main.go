package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// defaultBaseURL 与服务端默认监听地址一致；--url 可覆盖。
const defaultBaseURL = "http://127.0.0.1:8898"

// version 由构建期 -X main.version=<版本> 注入；未注入时默认为 dev。
var version = "dev"

// run 执行一次 CLI 调用：解析全局参数、解析多词命令路径与命令参数、分发执行并输出信封。
// 令牌只作为请求凭据保留在参数里，任何输出路径都不回显其值。
func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("verifyctl", flag.ContinueOnError)
	fs.SetOutput(out)

	urlFlag := fs.String("url", defaultBaseURL, "Verify 实例地址，例如 http://127.0.0.1:8898")
	tokenFlag := fs.String("token", "", "访问令牌，仅用于 Authorization: Bearer，不回显")
	dataDirFlag := fs.String("data-dir", "", "Verify 数据目录，供本地只读辅助使用")
	formatFlag := fs.String("format", "", "输出格式：table 渲染人类可读表格，json（默认）输出 JSON 信封；非 JSON 响应忽略此项")
	rawFlag := fs.Bool("raw", false, "输出原始响应体")
	prettyFlag := fs.Bool("pretty", false, "缩进 JSON 响应，不改变字段")
	helpJSON := fs.Bool("help-json", false, "以 JSON 格式输出帮助")

	fs.Usage = func() {
		fmt.Fprintln(out, "用法：verifyctl [全局参数] <子命令> [命令参数]")
		fmt.Fprintln(out, "全局参数：")
		fs.PrintDefaults()
		fmt.Fprintln(out, "注意：--format/--raw/--pretty 等全局参数必须放在子命令之前，")
		fmt.Fprintln(out, "      放在子命令之后会被当作命令参数解析失败（如 `verifyctl groups --format table` 请写成")
		fmt.Fprintln(out, "      `verifyctl --format table groups`）。")
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
	if rest[0] == "version" {
		fmt.Fprintln(out, version)
		return 0
	}

	// 命令名可含空格（health trash / scan stop / groups video），按最长匹配解析。
	cmd, words, ok := resolveCommandPath(rest)
	if !ok {
		fmt.Fprintf(out, "未知子命令：%s\n", strings.Join(rest, " "))
		return 2
	}

	params, err := parseCommandParams(cmd, rest[words:])
	if err != nil {
		fmt.Fprintf(out, "命令 %s 参数解析失败（%v）\n", cmd.Name, err)
		return 2
	}
	if params["help-json"] == "true" {
		if err := writeCommandDetail(out, cmd); err != nil {
			fmt.Fprintf(out, "输出命令详情失败（%v）\n", err)
			return 1
		}
		return 0
	}
	if *prettyFlag {
		params["pretty"] = "true"
	}

	// 未接入的副作用类必须明确报错，避免脚本把空输出误判为操作已生效。
	if cmd.Run == nil {
		fmt.Fprintf(out, "命令 %s 的执行尚未接入，本阶段仅解析命令名\n", cmd.Name)
		return 2
	}
	if missing := missingRequiredParams(cmd, params); len(missing) > 0 {
		fmt.Fprintf(out, "命令 %s 缺少必填参数：%s\n", cmd.Name, strings.Join(missing, "、"))
		return 2
	}

	token, err := resolveToken(*tokenFlag, *dataDirFlag)
	if err != nil {
		_ = writeError(out, cmd.Name, &ErrDetail{Message: err.Error()})
		return 2
	}

	// /api/events 是 SSE 无限心跳流，不能用 io.ReadAll（会阻塞到超时再失败、拿不到事件）。
	// 走流式逐行输出到 stdout，Ctrl+C（SIGINT/SIGTERM）优雅停止并退出 0。
	if cmd.Path == "/api/events" {
		// events 命令自身声明了 --token；优先用它，回退全局 --token。
		// （此前只取全局 token，命令级 --token 被静默丢弃 —— I3）
		evToken := params["token"]
		if evToken == "" {
			evToken = token
		}
		return runEventStream(out, *urlFlag, evToken)
	}

	c := NewClient(*urlFlag, token)

	result, err := cmd.Run(context.Background(), c, params)
	if err != nil {
		// 危险操作的本地用法错误（缺 --yes / --confirm-token、令牌不匹配或过期）映射退出码 2，
		// 全程未发 HTTP 请求；用明文提示，与其它本地用法错误一致。
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(out, "命令 %s 未执行：%s\n", cmd.Name, ue.msg)
			return 2
		}
		status, detail := httpStatusFromError(err)
		if werr := writeError(out, cmd.Name, detail); werr != nil {
			fmt.Fprintf(out, "输出错误信封失败（%v）\n", werr)
		}
		return exitCodeForHTTP(status)
	}

	raw, err := runResultBytes(result)
	if err != nil {
		_ = writeError(out, cmd.Name, &ErrDetail{Message: err.Error()})
		return 1
	}
	// raw/thumb 返回文件字节或 JPEG，塞进 JSON 信封会直接编码失败；默认走原始输出。
	rawMode := *rawFlag || binaryResultCommand(cmd)
	// --format table：显式渲染人类可读表格；非 JSON / 二进制响应仍原样输出。
	// 错误信封始终走 writeError 的 JSON，不受 --format 影响。
	if strings.EqualFold(*formatFlag, "table") && !rawMode {
		if err := writeTableResult(out, raw); err != nil {
			fmt.Fprintf(out, "输出表格失败（%v）\n", err)
			return 1
		}
		return 0
	}
	if err := writeResult(out, cmd.Name, raw, rawMode, params["pretty"] == "true" && !rawMode); err != nil {
		fmt.Fprintf(out, "输出结果信封失败（%v）\n", err)
		return 1
	}
	return 0
}

// binaryResultCommand 列出返回二进制/CSV/HTML/附件字节而非 JSON 的路由，这些命令跳过信封、原样输出。
// 导出类（zip/CSV/HTML/指纹 JSON 文件）把产物写到 stdout，供重定向落盘；塞进 JSON 信封会编码失败。
// /api/export←CSV、/api/report←HTML、/api/oplog/export←CSV-BOM 都是非 JSON，必须纳入。
func binaryResultCommand(cmd Command) bool {
	switch cmd.Path {
	case "/api/raw", "/api/thumb",
		"/api/trash/export", "/api/sync/export", "/api/rule-search/export",
		"/api/export", "/api/report", "/api/oplog/export":
		return true
	default:
		return false
	}
}

// writeResult 输出信封；--pretty 在信封层做一次缩进，避免 Encoder 把 RawMessage 压回单行。
func writeResult(out io.Writer, command string, raw []byte, rawMode, pretty bool) error {
	// 通用兜底：非原始模式但响应不是合法 JSON（如遗漏声明的 CSV/HTML/纯文本）时，
	// 直接原样写出，避免 json.RawMessage 编码失败导致「输出结果信封失败」退出 1、丢数据。
	// 空响应体仍走信封（data:null），保持既有语义。
	if rawMode || (len(raw) > 0 && !json.Valid(raw)) {
		_, err := out.Write(raw)
		return err
	}
	if !pretty {
		return writeEnvelope(out, command, raw, false)
	}
	var buf bytes.Buffer
	if err := writeEnvelope(&buf, command, raw, false); err != nil {
		return err
	}
	if !json.Valid(buf.Bytes()) {
		_, err := out.Write(buf.Bytes())
		return err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, buf.Bytes(), "", "  "); err != nil {
		return err
	}
	indented.WriteByte('\n')
	_, err := out.Write(indented.Bytes())
	return err
}

// resolveCommandPath 从剩余参数里找最长的已注册命令名，返回命令与消耗的词数。
func resolveCommandPath(rest []string) (Command, int, bool) {
	maxWords := len(rest)
	if maxWords > 6 {
		maxWords = 6
	}
	for n := maxWords; n >= 1; n-- {
		name := strings.Join(rest[:n], " ")
		if cmd, ok := findCommand(name); ok {
			return cmd, n, true
		}
	}
	return Command{}, 0, false
}

// parseCommandParams 按注册表的 Param 解析命令级 flag 与位置参数，
// 并把 --dry-run/--yes/--confirm-token 映射进 params（命令层只读 params）。
func parseCommandParams(cmd Command, args []string) (map[string]string, error) {
	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// bool 参数注册为开关（--quick 无值即 true，--quick=false 显式关）；
	// 其余 string/int/float/json 仍是取值 flag。旧写法把 bool 也当 fs.String，
	// 导致 `scan --quick` 报「flag needs an argument」、`folder_rename --prefix_a --yes`
	// 把 --yes 当作 prefix_a 的值吞掉（I1：静默损坏）。setByFlag 仍用 fs.Visit，
	// 从而区分「--flag=false」与「根本没设」。
	strValues := make(map[string]*string, len(cmd.Params)+1)
	boolValues := make(map[string]*bool, len(cmd.Params))
	for _, p := range cmd.Params {
		if p.Type == "bool" {
			boolValues[p.Name] = fs.Bool(p.Name, p.Default == "true", "")
			continue
		}
		strValues[p.Name] = fs.String(p.Name, p.Default, "")
	}
	// index repair / health rename / health ignore 的 dry_run 是真实业务参数，
	// 同时接受下划线写法；本地 --dry-run 改存 __cli_dry_run，二者互不覆盖。
	// 注册表已声明就不重复注册（否则 flag 重名 panic），否则补一个字符串兜底。
	if _, ok := strValues["dry_run"]; !ok {
		if _, ok := boolValues["dry_run"]; !ok {
			strValues["dry_run"] = fs.String("dry_run", "", "")
		}
	}

	pretty := fs.Bool("pretty", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	yes := fs.Bool("yes", false, "")
	confirmToken := fs.String("confirm-token", "", "")
	helpJSON := fs.Bool("help-json", false, "")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	setByFlag := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setByFlag[f.Name] = true })

	// 关键：只把用户「显式提供」的参数放进 params。
	// 声明参数里的空默认值（含并未出现的 bool/int/json）一律不发，否则服务端把
	// `watch:""` 解成 *bool、`group_keys:""` 解成 []string 会直接 400。
	// 提供方式有二：--flag 显式指定（setByFlag），或作为位置参数给出（见下方循环）。
	params := make(map[string]string, len(strValues)+len(boolValues)+5)
	for name := range strValues {
		if setByFlag[name] {
			params[name] = *strValues[name]
		}
	}
	for name := range boolValues {
		if setByFlag[name] {
			params[name] = strconv.FormatBool(*boolValues[name])
		}
	}
	if *pretty {
		params["pretty"] = "true"
	}
	// dry_run / yes 是部分接口的真实业务参数，本地开关必须走 __cli_ 前缀的内部 key，
	// 否则 index repair / health rename / health ignore 的服务端预演永远不可达。
	if *dryRun && params["__cli_dry_run"] == "" {
		params["__cli_dry_run"] = "true"
	}
	if *yes {
		params["__cli_yes"] = "true"
	}
	if *confirmToken != "" {
		params["confirm_token"] = *confirmToken
	}
	if *helpJSON {
		params["help-json"] = "true"
	}

	// 位置参数按注册表声明顺序填给未被 flag 显式赋值的参数。
	next := 0
	for _, pos := range fs.Args() {
		// Go flag 在第一个非 flag 处停止解析，其后的 `-x` 会落进位置参数里被静默当值。
		// 明确拒绝以 - 开头的残留位置参数，防止 I1 那类「flag 被当值吞掉」的静默损坏。
		if strings.HasPrefix(pos, "-") {
			return nil, fmt.Errorf("参数须置于位置参数之前（检测到位置参数 %q 以 - 开头，疑似被当成取值吞掉）", pos)
		}
		for next < len(cmd.Params) && setByFlag[cmd.Params[next].Name] {
			next++
		}
		if next >= len(cmd.Params) {
			return nil, fmt.Errorf("多余的位置参数：%s", pos)
		}
		params[cmd.Params[next].Name] = pos
		next++
	}
	return params, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
