---
name: mcp-tool-design
description: 为 profkit mcp 设计或新增 MCP 工具。当用户要给 MCP server 加工具、讨论工具返回什么内容（文本/JSON/文件路径）、或要设计 fetch_profile / top / flamegraph 等工具出入参时使用。
---

# MCP 工具设计（profkit mcp）

**难点在工具设计而非代码**（调研结论，docs/prof-web.md §5）：MCP 返回什么直接决定客户端（IDE/AI）能做什么分析。加工具前先回答"客户端拿到返回值后能干什么"。

## 现有工具集

| 工具 | 入参 | 出参（设计原则） |
|---|---|---|
| `fetch_profile` | 目标 ip:port、profile 类型、时长 | profile 文件路径（落盘后返回，供后续工具/客户端复用） |
| `top` | profile 路径或目标、N | 文本 top N 报告（人类与 LLM 都可直接读） |
| `flamegraph_data` | profile 路径 | 调用树 JSON（结构化，供客户端二次加工/可视化） |
| `goroutines` | 目标 ip:port | 文本摘要 + 完整 dump 路径 |

## 设计规则

1. **返回值三选一，按消费方定**：
   - LLM 直接分析 → 精简文本（top N、计数摘要），**控制体积**：goroutine 全 dump 动辄几 MB，只回摘要+落盘路径
   - 客户端二次加工 → 结构化 JSON
   - 大二进制（profile 原始文件）→ 永远返回落盘路径，不内联
2. **先参数后新工具**：能用现有工具加参数覆盖的（如 `top` 加 `-n`），不加新工具。
3. **薄皮调 core**：工具 handler 只做参数校验 + 调 core 包 + 格式化输出，分析逻辑一律下沉 core（与 serve 共用）。
4. **错误可行动**：失败信息告诉客户端下一步（"目标不可达：确认 ip:port 与防火墙"；"CPU 采集超时：降低 -d 或调大 profkit 侧上限"）。
5. **安全同红线**：工具入参中的目标地址与 serve 白名单同一套校验；MCP 形态不给"绕过白名单"的能力。

## 新增工具流程

1. 用 TDD 技能先定行为的验收用例（工具层用 go-sdk 的测试支持）
2. core 包实现/复用分析逻辑
3. handler 薄皮接线 + 工具描述（description 写清何时用，LLM 靠它选工具）
4. 更新 README 工具表与本技能的现有工具集表
