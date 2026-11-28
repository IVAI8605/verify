package store

import (
	"context"
	"strings"
)

// ExtStat 是索引内按扩展名聚合的文件数与字节数。
type ExtStat struct {
	Ext   string
	Files int64
	Bytes int64
}

// ExtStats 从现有索引汇总文件类型分布（v4.5.4）。
// 首次升级到本版时还没有落盘的类型缓存，用索引直接补一次，避免概览饼图空白；
// 之后由扫描完成时写入的缓存提供带时间的结果。
func (s *Store) ExtStats(ctx context.Context) ([]ExtStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ext, COUNT(*), COALESCE(SUM(size),0)
		FROM files WHERE status='active' GROUP BY ext ORDER BY 3 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExtStat
	for rows.Next() {
		var st ExtStat
		if err := rows.Scan(&st.Ext, &st.Files, &st.Bytes); err != nil {
			return nil, err
		}
		st.Ext = strings.ToLower(strings.TrimPrefix(st.Ext, "."))
		out = append(out, st)
	}
	return out, rows.Err()
}
