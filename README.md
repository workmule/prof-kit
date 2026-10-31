# prof-kit

Go 服务性能采集与分析工具箱。一个二进制（`profkit`），多种形态：Web 服务、MCP server、一次性 fetch 报告——拉取任意 Go 服务 `/debug/pprof/*` 的 profile，提供火焰图/调用图/源码视图等完整分析能力，防火墙只需开一个入口。

> 调研与设计详情见 [docs/prof-web.md](docs/prof-web.md)。

## 技术方案

核心判断：**profile 分析核心全部来自 [google/pprof](https://github.com/google/pprof) 库**，本项目只写"壳"——一个 core 包 + 三个薄皮形态：

```
core 包（fetch + pprof 库分析输出）
├── profkit serve    Web 服务（参考 pprof-web，含路径白名单防 SSRF；-d 后台 + ctl 管控）
├── profkit mcp      MCP server（fetch_profile / profile_data / top / diff_profiles / list_profiles / flamegraph_data / goroutines）
└── profkit fetch    一次性采集 → 自包含 HTML 报告（top + 火焰图 SVG，本地可看）
```

不做交互式 CLI（`go tool pprof` 已覆盖），只保留 `fetch` 一次性子命令。

- 代理路径 `/proxy/{ip}/{port}/debug/pprof/`，仅放行标准 pprof 路径白名单（防开放中继/SSRF）
- 支持四种 profile：CPU（默认上限 60s，`-max-cpu-seconds` 可配置加长）、heap、goroutine、allocs
- profile 落盘本地（默认 `/tmp/pprof-web`，可持久化），事后可回看
- MCP 是主目标形态，基于 `modelcontextprotocol/go-sdk`，支持两种传输方式：
  - **stdio（本地）**：默认方式，IDE/AI 客户端以子进程拉起 `profkit mcp`，适合单机个人使用
  - **Streamable HTTP（远程）**：`profkit mcp --http <addr>`，团队共享一个采集入口，客户端远程连接；注意复用"部署注意"的认证与内网约束
- 不建议复刻 pprof 浏览器 UI，人看火焰图时用 [pprof-web](https://github.com/wzxjohn/pprof-web) 原版配合

## 使用

```bash
# 安装
make install        # 或 go install github.com/workmule/prof-kit/cmd/profkit@latest

# Web 服务（浏览器访问交互式 pprof UI）
profkit serve

# 后台启动 + 管控（unix socket 通道，不暴露网络端口）
profkit serve -d
profkit ctl status    # pid/addr/version
profkit ctl restart   # fd 传递式优雅重启：监听不断，在途采集（如 CPU profile）跑完才退
profkit ctl stop

# MCP server（供 IDE / AI 客户端接入）
profkit mcp

# MCP 远程模式（Streamable HTTP，供远程客户端接入）
profkit mcp --http :9090

# 一次性采集并生成 HTML 报告（top + 火焰图 SVG，默认 reports/ 下，-open 自动打开浏览器）
profkit fetch -d 30 127.0.0.1:6060 cpu
profkit fetch 127.0.0.1:6060            # 默认 heap，另支持 goroutine/allocs
profkit fetch -o report.html 127.0.0.1:6060 cpu   # 指定输出路径

# 采集后直接拉起官方 pprof WebUI（维度切换/调用图/源码视图，Ctrl-C 停止）
profkit fetch -ui -d 30 127.0.0.1:6060 cpu

# 对已保存的 profile 起官方 WebUI
profkit ui /tmp/pprof-web/heap_xxx.pb.gz

# 查看版本
profkit version

# 命令行直接分析（无需 profkit，go tool pprof 原生支持）
go tool pprof http://<ip>:<port>/debug/pprof/heap
```

### 配置项

| Flag | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `-l` | `PPROF_LISTEN_ADDRESS` | 空（挑空闲端口，绑 0.0.0.0） | 监听地址 |
| `-t` | `PPROF_TMP_PATH` | `/tmp/pprof-web` | profile 存储目录 |
| `-b` | `PPROF_BASE_PATH` | `/` | 反向代理部署时的 base path |
| `-max-cpu-seconds` | 无 | 60 | CPU 采集上限秒数（serve/fetch 均可设） |

Flag 优先级高于环境变量。

## 部署注意

1. **无认证**：服务可向内网任意 IP:port 发请求，等同内网探测跳板——务必不开公网 ingress，保持 ClusterIP + port-forward/堡垒机访问，或在 ingress 层加 Basic Auth/IP 白名单。
2. **单副本**：profile 存本地盘，`replicaCount` 必须 = 1；持久化用 PVC，别用 hostPath（Pod 会绑死节点）。
3. **上游超时**：CPU profile 采集阻塞最长 60s（可加长），前面 LB/nginx 的 read timeout 要 ≥ 采集时长 + 余量。
4. 60s 上限经 `-max-cpu-seconds` 配置（serve/fetch），Go runtime 本身无限制；加长时注意目标进程同时只能跑一个 CPU profiler。

## 状态

已实现：serve（含 daemon/ctl）、mcp（stdio + HTTP）、fetch（HTML 报告 / 官方 WebUI）、ui 子命令。构建与测试走 `make build / make test`。

## License

TBD
