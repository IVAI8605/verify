# Verify · 开发速查

> 这份就够日常看。历史规划、审查报告与归档已移出版本库；需要追溯请找维护者。

## 项目

fnOS 上的查重助手：扫描选定目录/文件，按内容哈希和文件名规则找重复文件，支持预览、隔离区、批量处理。
源码在本仓库根目录，Go 编写，产物为 `verify-<版本>.fpk`。

当前版本见根目录 `README.md` 与 `CHANGELOG.md`；构建产物 `dist/verify-<版本>.fpk` 不入库。

## 目录

| 路径 | 用途 |
|---|---|
| `main.go` | 入口 |
| `internal/engine` | 扫描与判定内核 |
| `internal/store` | 数据库与索引 |
| `internal/web` | Web 界面 |
| `internal/config`、`internal/health`、`internal/export`、`internal/fname`、`internal/video` | 配置、健康检查、导出、命名、视频探测 |
| `packaging/fpk` | fnOS 安装包骨架（`app/bin` 是构建注入的二进制，不入库） |
| `scripts/` | 构建与发布脚本 |
| `docs/` | 只保留这份速查 |
| `cache/` | 本地临时目录，可随时重建 |
| `.box-agent/`、`.workbuddy/`、`.screenshots/` | 本地工具产物，不入库 |

## 常用命令

```bash
# 先把 Go 加进 PATH
cd <仓库根目录>

go build ./...          # GOCACHE 已指到项目外共享缓存
go test ./...
gofmt -l .
```

打包 fpk 用 `scripts/` 下的脚本，产物统一输出到 `dist/`。

## 约定

- 默认本地提交；push / 建 Release / 发布 fpk 需用户明确授权。
- 清理只移不删，移动前留清单；不提交 `config.json`、`.env`、`.public-safety.local`、数据库文件。
- Go 编译缓存放项目外，避免项目目录反复膨胀。
- `docs/` 只维护这份速查，新写的结论直接补进来；旧材料移隔离区。
