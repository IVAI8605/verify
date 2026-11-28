package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"verify/internal/applog"
)

// v4.5.4：文件类型分布原先只存在内存里，进程一重启（含应用升级）就变空，
// 概览页的环形图会整块消失，而索引里的文件还在。这里把最近一次枚举完成的
// 类型分布落盘到数据目录，启动时读回，重启后仍能看到「上次扫描」的结果。
const typeStatsFileName = "type-stats.json"

type typeStatsCache struct {
	At    int64      `json:"at"`
	Types []TypeStat `json:"types"`
}

func typeStatsPath(dataDir string) string {
	return filepath.Join(dataDir, typeStatsFileName)
}

func loadTypeStats(dataDir string) ([]TypeStat, int64, error) {
	if dataDir == "" {
		return nil, 0, nil
	}
	b, err := os.ReadFile(typeStatsPath(dataDir)) // #nosec G304 -- 固定文件名，目录来自启动参数而非请求
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	var c typeStatsCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, 0, err
	}
	if c.At < 0 || len(c.Types) == 0 {
		return nil, 0, nil
	}
	return c.Types, c.At, nil
}

func saveTypeStats(dataDir string, types []TypeStat) (int64, error) {
	return writeTypeStats(dataDir, types, time.Now().Unix())
}

// writeTypeStats 的 at=0 表示数据来自现有索引而不是一次扫描，界面据此标注「按现有索引」。
func writeTypeStats(dataDir string, types []TypeStat, at int64) (int64, error) {
	if dataDir == "" || len(types) == 0 {
		return 0, nil
	}
	b, err := json.Marshal(typeStatsCache{At: at, Types: types})
	if err != nil {
		return 0, err
	}
	path := typeStatsPath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return at, nil
}

// loadCachedTypeStats 在启动时恢复上次扫描的类型分布，避免重启后图表空白。
func (e *Engine) loadCachedTypeStats() {
	types, at, err := loadTypeStats(e.cfg.Get().DataDir)
	if err != nil {
		applog.Warn("读取文件类型分布缓存失败：%s", err)
	}
	if len(types) == 0 {
		e.backfillTypeStatsFromIndex()
		return
	}
	e.updateState(func(s *State) {
		s.Types = types
		s.TypesAt = at
		// 概览的「最近扫描」与图表同源，避免出现「有图表但显示尚未扫描」的矛盾。
		if at > 0 && s.LastScan == 0 {
			s.LastScan = at
		}
	})
}

// backfillTypeStatsFromIndex 处理首次升级：此前版本没有落盘缓存，直接用索引里
// 已有的扩展名聚合补一次，让概览图立刻有内容；at=0，界面标注为「按现有索引」。
func (e *Engine) backfillTypeStatsFromIndex() {
	rows, err := e.st.ExtStats(e.baseCtx)
	if err != nil {
		applog.Warn("从索引汇总文件类型失败：%s", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	types := make([]TypeStat, 0, len(rows))
	for _, r := range rows {
		types = append(types, TypeStat{Ext: r.Ext, Files: r.Files, Bytes: r.Bytes})
	}
	if _, err := writeTypeStats(e.cfg.Get().DataDir, types, 0); err != nil {
		applog.Warn("保存索引汇总的类型分布失败：%s", err)
	}
	e.updateState(func(s *State) { s.Types = types })
}

// persistTypeStats 在枚举完成后落盘本次类型分布；失败只告警，不影响扫描结论。
func (e *Engine) persistTypeStats() {
	types := e.stats.typeStats()
	if len(types) == 0 {
		return
	}
	at, err := saveTypeStats(e.cfg.Get().DataDir, types)
	if err != nil {
		applog.Warn("保存文件类型分布缓存失败：%s", err)
		return
	}
	e.updateState(func(s *State) { s.TypesAt = at })
}
