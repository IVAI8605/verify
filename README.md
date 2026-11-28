# 查重助手（Verify）

飞牛 NAS 上的重复文件清理工具。单个 Go 二进制 + 内嵌中文 Web UI + 本地 SQLite，不依赖外部服务。当前版本 **v4.5.4**。

## 功能

- **查重**：精确内容（BLAKE3 全量哈希）、快速预哈希、同名、模糊拼音名、语义分组，以及按大小/属性/媒体类型组合的搜索规则；结果统一在一个页面按类型切换。
- **媒体相似检测**：相似视频、相同片头、重复音频、相似图片、重复文件夹、竖横屏识别；视频分析优先用核显硬解，失败自动回退软解。
- **文件健康**：空文件、空目录、扩展名与内容不符、损坏文件、命名不规范、繁体名、临时文件共 7 类检测，支持批量修正、误报忽略与整类处理。
- **整理与清理**：隔离区（可恢复）、跨卷移动、批量改名（11 种方法链）、硬链接合并、片头剪除、H.265 转码、大文件 Top N。
- **自动化与安全**：实时监控、定时数据保鲜、清理计划、操作审计、访问密码、保护文件夹、路径防线校验。
- **运维友好**：中文日志、缓存管理、内置帮助中心、CSV 与 HTML 报告导出。

## 快速开始

### 飞牛 NAS

从 [Releases](https://github.com/IVAI8605/verify/releases) 下载对应版本的 `.fpk`，在应用中心手动安装。当前包未签名，安装时需在签名风险确认处放行。

安装向导第一步会让你选择 **Web 访问端口**，默认 **18898**（避开 8898、8080 这类常被其他应用占用的端口）。若提示端口冲突，换一个空闲端口即可，装完访问 `http://<NAS>:<你选的端口>`。

数据保存在 `@appdata/verify/data`，卸载不会删除该目录。

### 本地运行

```bash
go build -o verify .
./verify -data /path/to/data -addr :18898
```

首次运行生成 `config.json`，扫描目录、排除目录等可在 Web UI 设置页修改。视频/音频分析依赖系统 `ffmpeg` / `ffprobe`（默认 `/usr/bin/ffmpeg`，可用 `VERIFY_FFMPEG` / `VERIFY_FFPROBE` 覆盖）。

## 使用须知

- 删除类操作默认移入隔离区，可恢复；永久删除与清空隔离区需先写审计记录。
- 「保护文件夹」内的文件照常参与查重，但任何清理操作都会被拦截。
- 内置帮助中心共 14 章，页面中的 `?` 可直达对应章节。

## 权限与隐私

应用以专用 `package` 用户运行，**不请求 root**，不创建共享目录、不注册 Docker 项目或系统命令链接；只读写用户在飞牛「系统设置 → 应用 → 查重助手」里授权的目录。唯一附加系统组是 `render`，用于可选的 Intel QSV 硬件解码，设备不可用时自动软解。

首次打开页面必须设置访问密码（至少 8 位），之后登录只用密码；密码以 PBKDF2-HMAC-SHA256 加盐哈希保存，配置与日志中没有明文。旧版 `access_token` 升级后仍可登录一次并建议切换到密码，之后可保留作为脚本/API 的 Bearer 凭据。命令行 `verifyctl` 在密码模式下用 `--password` 或环境变量 `VERIFY_PASSWORD` 走 HTTP Basic（用户名为空），密码只进请求头，不进 URL；旧令牌模式继续用 `--token`。

完整声明、超出最小权限的部分与核对/收回方法见 [docs/权限说明.md](docs/权限说明.md)；应用内「操作手册 → 权限与隐私」有同一份说明。

## 开发

前置：Go 1.26+、bash + GNU coreutils；仅打包 fpk 需要 [fnpack 1.3.3](https://developer.fnnas.com/docs/cli/fnpack/)。

```bash
# 本地调试
go build -o verify .

# 静态构建 Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "-s -w" \
  -o packaging/fpk/app/bin/verify .

# 打包（产物输出到 dist/）
bash scripts/build-fpk.sh

# 本地门禁
bash scripts/quick-check.sh
```

前端为零构建的原生 JS/CSS，由 `go:embed` 内嵌，无 CDN、无运行时依赖。`webtypes/types.d.ts` 由后端响应契约自动生成，改响应字段后运行 `python3 tools/gen_types.py`。

## 文档

- [CHANGELOG.md](CHANGELOG.md) — 逐版本更新记录
- [docs/权限说明.md](docs/权限说明.md) — 权限声明、最小权限对照与收回方法
- [docs/README.md](docs/README.md) — 开发速查与方案文档

## 许可证

[MIT](LICENSE)。繁简转换词典数据来自 [OpenCC](https://github.com/BYVoid/OpenCC)（Apache-2.0），详见 `internal/engine/data/opencc/NOTICE.md`。
