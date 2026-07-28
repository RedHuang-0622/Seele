# Seele v0.5 迁移 — 活动拆解与执行方案

## 邻接表

```
A -> E          (agent/api/ → agent/ 主结构)
B -> E, F       (agent/tool/ → agent/ 主结构 + provider import)
C -> E          (agent/gateway/ → agent/ 主结构)
D -> E          (context/ → agent/ 主结构)
E -> G          (agent/ 主结构 → 删除旧包)
F -> G          (provider import → 删除旧包)
G -> H          (删除旧包 → 示例更新)
H -> I          (示例更新 → 全量测试)
```

## 活动属性表

| 活动 | 名称 | 工期(h) | ES | EF | LS | LF | 浮动 | 关键? |
|------|------|---------|----|----|----|----|------|-------|
| A | agent/api/ 包迁移 | 2 | 0 | 2 | 0 | 2 | 0 | 是 |
| B | agent/tool/ 包合并 | 3 | 0 | 3 | 0 | 3 | 0 | 是 |
| C | agent/gateway/ 新建 | 3 | 0 | 3 | 0 | 3 | 0 | 是 |
| D | context/ 包合并 | 4 | 0 | 4 | 0 | 4 | 0 | 是 |
| E | agent/ 主结构整合 | 2 | 4 | 6 | 4 | 6 | 0 | 是 |
| F | provider/ import 更新 | 1 | 3 | 4 | 5 | 6 | 2 | 否 |
| G | 删除旧包 | 2 | 6 | 8 | 6 | 8 | 0 | 是 |
| H | 示例更新 | 2 | 8 | 10 | 8 | 10 | 0 | 是 |
| I | 全量测试验证 | 1 | 10 | 11 | 10 | 11 | 0 | 是 |

## 关键路径

**D → E → G → H → I** (总工期: 11小时)

解释：`context/` 合并是工作量最大的任务（4h），它驱动关键路径。

## 最大并行会话数

**4**（Phase 1：A/B/C/D 四路并行）

## 活动图

```mermaid
gantt
    title Seele v0.5 迁移活动图
    dateFormat  HH:mm
    axisFormat  %H:%M
    section Phase 1 (并行)
    A agent/api/      :a1, 0h, 2h
    B agent/tool/     :b1, 0h, 3h
    C agent/gateway/  :c1, 0h, 3h
    D context/         :d1, 0h, 4h
    section Phase 2 (串行)
    E agent/ 主结构   :e1, after d1, 2h
    F provider import :f1, after b1, 1h
    section Phase 3 (清理)
    G 删除旧包        :g1, after e1, 2h
    H 示例更新        :h1, after g1, 2h
    I 全量测试        :i1, after h1, 1h
```

## 子代理分配

| 子代理 | 任务 | 命令 |
|--------|------|------|
| #1 | A: agent/api/ | claude --bg --print --prompt "..." |
| #2 | B: agent/tool/ | claude --bg --print --prompt "..." |
| #3 | C: agent/gateway/ | claude --bg --print --prompt "..." |
| #4 | D: context/ | claude --bg --print --prompt "..." |
| #5 (Phase 2) | E: agent/ 主结构 | claude --bg --print --prompt "..." |
| #6 (Phase 2) | F: provider/ import | claude --bg --print --prompt "..." |
| #7 (Phase 3) | G: 删除旧包 | claude --bg --print --prompt "..." |
| #8 (Phase 3) | H: 示例更新 | claude --bg --print --prompt "..." |
| #9 (Phase 3) | I: 全量测试 | claude --bg --print --prompt "..." |
