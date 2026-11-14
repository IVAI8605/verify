package dictconv

import "testing"

// 期望值全部取自旧实现（github.com/longbridgeapp/opencc v0.3.13，s2t/t2s）在同样输入上的实际输出，
// 用来锁住替换后的行为：任何一条变化都说明匹配算法或词典加载与原来不一致。
var fanjianSamples = []struct {
	in     string
	toTrad bool
	want   string
}{
	// 简→繁：词组歧义与常用词
	{"头发", true, "頭髮"},
	{"后来", true, "後來"},
	{"皇后", true, "皇后"},
	{"台湾", true, "臺灣"},
	{"机器", true, "機器"},
	{"资讯", true, "資訊"},
	{"软件", true, "軟件"},
	{"磁盘", true, "磁盤"},
	{"档案", true, "檔案"},
	{"网络", true, "網絡"},
	{"内存", true, "內存"},
	{"干杯", true, "乾杯"},
	{"面条", true, "麪條"},
	{"一只猫", true, "一隻貓"},
	{"计算机", true, "計算機"},
	{"发展", true, "發展"},
	{"里面", true, "裏面"},
	// 简→繁：引擎里真实会转的文件名（ConvertNames 收的是含扩展名的完整文件名）
	{"这个视频.mp4", true, "這個視頻.mp4"},
	{"学习资料_第1集.mkv", true, "學習資料_第1集.mkv"},
	// 繁→简：词组歧义与常用词
	{"頭髮", false, "头发"},
	{"後來", false, "后来"},
	{"皇后", false, "皇后"},
	{"臺灣", false, "台湾"},
	{"機器", false, "机器"},
	{"資訊", false, "资讯"},
	{"軟體", false, "软体"},
	{"磁碟", false, "磁碟"},
	{"檔案", false, "档案"},
	{"網路", false, "网路"},
	{"記憶體", false, "记忆体"},
	{"乾杯", false, "干杯"},
	{"麵條", false, "面条"},
	{"一隻貓", false, "一只猫"},
	{"電腦", false, "电脑"},
	{"發展", false, "发展"},
	{"裡面", false, "里面"},
	// 繁→简：真实文件名样本
	{"這個視頻.mp4", false, "这个视频.mp4"},
	{"學習資料_第1集.mkv", false, "学习资料_第1集.mkv"},
	{"測試視頻.mp4", false, "测试视频.mp4"},
	// 无中文 / 已是目标字体：原样返回
	{"abc.MP4", false, "abc.MP4"},
	{"abc.MP4", true, "abc.MP4"},
	{"测试视频.mp4", false, "测试视频.mp4"},
	{"test.mp4", false, "test.mp4"},
	// 边界：重复键（厘/佣/公厘/回流/汇总/厘米/什么，后一行覆盖前一行）
	{"厘", true, "厘"},
	{"佣", true, "佣"},
	{"公厘", true, "公厘"},
	{"回流", true, "回流"},
	{"汇总", true, "匯總"},
	{"厘米", true, "厘米"},
	{"什么", true, "什麼"},
	// 边界：值列含多个候选项时只取第一个（干 → 幹）
	{"干", true, "幹"},
	{"干", false, "干"},
	// 边界：词典末行「开户→開戶」因文件无结尾换行被丢弃，靠单字表回退得到同样结果
	{"开户", true, "開戶"},
	// 边界：非 BMP 字符（4 字节 UTF-8）
	{"𠀾", true, "𠁞"},
	{"𠀾", false, "𠀾"},
	// 边界：16 字词条超过 10 字前瞻窗口，只能逐字转换
	{"善有善报恶有恶报若然不报时晨未到", true, "善有善報惡有惡報若然不報時晨未到"},
	{"酒逢知己千鍾少話不投機半句多", false, "酒逢知己千锺少话不投机半句多"},
	// 边界：中英混排、空串、单字夹在 ASCII 中间
	{"ABC测试123", true, "ABC測試123"},
	{"", true, ""},
	{"a中b", true, "a中b"},
	{"頭髮和头发", false, "头发和头发"},
}

func TestConvert(t *testing.T) {
	for _, tc := range fanjianSamples {
		conv := T2S()
		if tc.toTrad {
			conv = S2T()
		}
		if got := conv.Convert(tc.in); got != tc.want {
			t.Errorf("Convert(%q, 简→繁=%t) = %q，期望 %q", tc.in, tc.toTrad, got, tc.want)
		}
	}
}
