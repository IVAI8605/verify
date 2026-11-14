package engine

import (
	"context"

	"verify/internal/store"
	"verify/internal/taskguard"
)

// ImportSession 把「导入会话写库」纳入任务闸门（v3.25.2 P3-3）。
// 一次导入最多解析 128MB CSV、写入数万行明细，与扫描 / 转码并发时争 SQLite 写锁；
// 之前 KindImport 声明了却没有任何接入点，AGENTS.md 的长任务清单与实现不一致。
func (e *Engine) ImportSession(ctx context.Context, name, source string, rows []store.ImportRow) (store.ImportSession, error) {
	done, err := e.acquireTask(taskguard.KindImport, "导入查重结果", taskguard.GroupIO, taskguard.ResDBWrite)
	if err != nil {
		return store.ImportSession{}, err
	}
	defer done("done")
	return e.st.CreateImportSession(ctx, name, source, rows)
}
