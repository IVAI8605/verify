# 第三方数据归属：OpenCC 词典

本目录下的 `STPhrases.txt`、`STCharacters.txt`、`TSPhrases.txt`、`TSCharacters.txt`
来自 [OpenCC（Open Chinese Convert 開放中文轉換）](https://github.com/BYVoid/OpenCC)
项目的 `data/dictionary/` 目录，经 `github.com/longbridgeapp/opencc@v0.3.13` 的模块副本原样复制，未做内容修改。

- 上游许可证：Apache License 2.0（https://github.com/BYVoid/OpenCC/blob/master/LICENSE）
- 本项目仅做**打包与加载**：通过 `go:embed` 内嵌这 4 个文件，并用自行实现的前向最大匹配读取；
  转换算法为复刻 OpenCC 的字典链行为，未修改词典数据本身。
- 替换原因：原依赖链 `opencc` → `github.com/liuzl/da` → `github.com/liuzl/cedar-go` 中的
  `cedar-go` 采用 GPL-2.0，静态链接会波及本项目 MIT 许可的分发方式。
