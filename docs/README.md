# 跨模块文档

`docs/` 只保存无法归属于单一源码包的内容：跨模块架构、长期边界、迁移记录、评审和测试方案。单个 package 的功能与 API 请阅读对应目录的 `README.md`。写作与放置规则见 [DOCUMENTATION_STANDARD.md](DOCUMENTATION_STANDARD.md)。

## 入口

- [arch/](arch/README.md)：长期架构边界与扩展契约。
- [history/](history/README.md)：按日期归档的一次性方案、评审和验证记录。
- [review/](review/)：历史评审资料。
- [plan/](plan/)：历史设计与迁移计划。

## 当前根能力重构

- [Seele 根能力与运行时边界](arch/09-seele-runtime-boundary.md)
- [Seele 扩展契约与详细设计](arch/10-seele-extension-contracts.md)
- [Seele 边界测试与验收方案](arch/11-seele-boundary-test-strategy.md)
