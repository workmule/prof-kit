# AGENTS.md — prof-kit 项目约定

## 项目概览

Go 服务性能采集与分析工具箱。二进制名 `profkit`（不带连字符，Go 包名不允许 dash），module 为 `github.com/<you>/prof-kit`。

**双形态架构**（2026-10-30 裁定，详见 docs/prof-web.md）：

```
core 包（fetch + pprof 库分析输出）    ← 唯一有业务逻辑的地方
├── profkit serve    Web 服务（参考 wzxjohn/pprof-web，路径白名单防 SSRF）
└── profkit mcp      MCP server（modelcontextprotocol/go-sdk；stdio 默认，--http 远程）
```

## 硬约束

1. **分析核心不自研**：火焰图/top/源码视图等全部来自 `github.com/google/pprof` 库，本项目只写 fetch、存储、路由等胶水代码。发现自己在实现 profile 解析/分析逻辑 = 走偏了。
2. **白名单不可弱化**：代理路径仅放行标准 pprof 路径白名单（防开放中继/SSRF）。新增代理目标必须先过白名单评审。
3. **不做交互式 CLI**（2026-10-30 裁定，同日修正）：`go tool pprof` 覆盖交互分析，不重建交互循环；但保留**一次性子命令 `profkit fetch`**——采集后直接产出自包含 HTML 报告（top + 火焰图 SVG，本地双击可看）。fetch 的 HTML 渲染属展示胶水，分析聚合仍在 core（复用 Top/Tree）。
4. **配置三入口**：flag > 环境变量 > 默认值，顺序不可变（`-l/-t/-b`，见 README 配置表）。
5. **60s CPU 采集上限必须是配置项**，不写死；改长时必须在文档标注上游 LB/nginx 超时约束。

## 常用命令

```bash
go build ./...        # 构建
go test ./...         # 全量测试
go vet ./...          # 静态检查
go run . serve        # 本地起 Web 服务
go run . mcp          # 本地起 MCP（stdio）
```

## 代码规范

- 包名/文件名/标识符 kebab-case 只允许出现在 module 路径，代码层一律无 dash。
- 测试与生产代码同包（白盒优先），表驱动测试（`t.Run` + `[]struct{...}`）。
- profile 落盘目录约定 `/tmp/pprof-web`（与 pprof-web 生态一致，勿随意改默认值）。

## 文档索引

| 文档 | 说明 |
|---|---|
| docs/prof-web.md | 调研与决策记录：pprof-web 分析、60s 限制、命名决策、CLI 裁掉理由、MCP 双 transport |
| README.md | 面向使用者的介绍、使用方式、部署注意 |

## 安全红线

本工具本质是"向内网任意 IP:port 发请求"的跳板。任何形态（serve/mcp）暴露时：不开公网、加认证或 IP 白名单、保持最小网络可达面。涉及暴露面的改动必须在提交说明中显式说明网络边界变化。
