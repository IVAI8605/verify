// Package dictconv 负责文件名繁简转换，词典数据内嵌在 opencc/ 子目录。
//
// 为什么自己写而不是继续用 github.com/longbridgeapp/opencc：
// 该库依赖 github.com/liuzl/da → github.com/liuzl/cedar-go，后者是 GPL-2.0，
// 静态链接会把整个二进制卷进 GPL，与本项目的 MIT 许可冲突。
// 这里只复用 OpenCC 上游 Apache-2.0 的词典数据（见 opencc/NOTICE.md），
// 匹配算法按 opencc.Convert 的行为复刻，输出逐条对齐。
package dictconv

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed opencc/STPhrases.txt
var stPhrasesData string

//go:embed opencc/STCharacters.txt
var stCharsData string

//go:embed opencc/TSPhrases.txt
var tsPhrasesData string

//go:embed opencc/TSCharacters.txt
var tsCharsData string

// maxMatchRunes 对齐原实现的前瞻窗口：opencc.Convert 每次只在当前位往后 10 个字符
// （rune）的窗口里找最长前缀，词条超过 10 个字永远不会命中。
// 词典里确实有 14–16 字的词条，因此必须保留这个截断，否则会多出新匹配。
const maxMatchRunes = 10

// Converter 是一组词典：短语表优先，单字表只做回退。
// 原实现按 s2t.json / t2s.json 的 dicts 顺序依次尝试，短语表一旦命中就停止查单字表。
type Converter struct {
	phrases map[string]string
	chars   map[string]string
}

var (
	loadOnce sync.Once
	s2t      *Converter
	t2s      *Converter
)

// S2T 返回简→繁转换器（首次调用时解析词典）。
func S2T() *Converter {
	load()
	return s2t
}

// T2S 返回繁→简转换器（首次调用时解析词典）。
func T2S() *Converter {
	load()
	return t2s
}

func load() {
	loadOnce.Do(func() {
		s2t = newConverter(stPhrasesData, stCharsData)
		t2s = newConverter(tsPhrasesData, tsCharsData)
	})
}

func newConverter(phraseData, charData string) *Converter {
	return &Converter{phrases: parseDict(phraseData), chars: parseDict(charData)}
}

// Convert 对 in 做前向最大匹配：先在窗口内找最长词组，找不到再回退到单字，仍找不到则原样保留。
func (c *Converter) Convert(in string) string {
	r := []rune(in)
	var b strings.Builder
	b.Grow(len(in))
	for i := 0; i < len(r); {
		n := len(r) - i
		if n > maxMatchRunes {
			n = maxMatchRunes
		}
		window := r[i : i+n]
		if v, l, ok := lookup(c.phrases, window); ok {
			b.WriteString(v)
			i += l
			continue
		}
		if v, l, ok := lookup(c.chars, window); ok {
			b.WriteString(v)
			i += l
			continue
		}
		b.WriteRune(r[i])
		i++
	}
	return b.String()
}

// lookup 在表里找最长的前缀命中，返回替换值与消耗的 rune 数。
// 原实现用 len(k)（字节数）比长短；由于参与比较的键都以同一条前缀开头，
// 字节长度排序与 rune 长度排序一致，这里按 rune 从长到短查即可。
func lookup(dict map[string]string, window []rune) (string, int, bool) {
	for l := len(window); l >= 1; l-- {
		if v, ok := dict[string(window[:l])]; ok {
			return v, l, true
		}
	}
	return "", 0, false
}

// parseDict 按原 da.Build 的读法解析 OpenCC 文本词典：
//   - 逐行 TrimSpace，按 \t 拆列，不足两列的行跳过；
//   - 值取第二列的第一个空白分隔词（原实现在两列行上取 strings.Fields(items[1])[0]）；
//   - 重复键以最后一行为准（cedar 的 Insert 对同键是覆盖写）。
//
// 末行处理刻意与原实现保持一致：da.Build 读到 io.EOF 时直接 break，不处理最后一段
// 没有换行结尾的文本。STPhrases.txt 正是没有结尾换行，它的末行「开户→開戶」在原实现里
// 从未生效；这里补上会改变 s2t 的输出，所以同样丢弃。
func parseDict(data string) map[string]string {
	lines := strings.Split(data, "\n")
	m := make(map[string]string, len(lines))
	for _, line := range lines[:len(lines)-1] {
		items := strings.Split(strings.TrimSpace(line), "\t")
		if len(items) < 2 || items[0] == "" {
			continue
		}
		fields := strings.Fields(items[1])
		if len(fields) == 0 {
			continue
		}
		m[items[0]] = fields[0]
	}
	return m
}
