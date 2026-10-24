# 08 · Claude-in-sbx 真实竖向验证记录

日期：2026-10-24｜范围：P0 第一条真实 Agent + 模型代理 + MCP + Dock 工具链路

## 1. 结论

已完成一条真实链路：

```text
Claude Code 2.1.300
  -> sbx v0.49.2 guest（无 workspace、skills off）
  -> sbx local MCP gateway
  -> 临时注册 pulsar-dock-local
  -> Dock stdio MCP gateway
  -> task.create_child
```

模型路径为：

```text
Claude guest
  -> host.docker.internal:3425
  -> Magpie
  -> deepseek/deepseek-flash[1m]
```

Claude 成功调用 Dock 的 `task.create_child`，返回 `task-0001 / created`，随后又用 `task.status` 查询同一个 `task-0001`，返回同样的 goal 与 `created` 状态。原始 stream-json 工具事件经过断言提取到 `validation/2026-10-24/claude-mcp-tool-evidence.json`。这证明了 Dock 当前 MCP 协作接口可以被真实 Agent 发现、调用和查询，并能运行在真实 sbx guest 内。

这不是完整产品完成，也不是安全认证。真实 MCP 服务、Skill、生产凭据注入、子环境实际创建、任务执行和成果交接仍未完成。

## 2. 实测配置

| 项目 | 配置 |
|---|---|
| Agent | Claude Code 2.1.300 |
| Guest | sbx v0.49.2，固定 linux/amd64 minimal 模板摘要 |
| Workspace | none；没有宿主项目挂载 |
| Shared skills | off |
| CPU/内存 | 2 CPU / 2GiB |
| MCP | sbx gateway 加载临时 `pulsar-dock-local` |
| Dock MCP | stdio 网关 `dock-gateway.exe` |
| 模型 | `deepseek/deepseek-flash[1m]`，通过 Magpie |
| Guest 模型配置 | `ANTHROPIC_BASE_URL=http://host.docker.internal:3425`、`ANTHROPIC_API_KEY=magpie`、`ANTHROPIC_AUTH_TOKEN=magpie` |
| 真实上游 key | 未进入 guest；未在本轮写入或读取真实 key |
| 模型工具目标 | `task.create_child` 后查询 `task.status` |
| 返回 | 两次工具结果均成功；`task-0001`、`state=created`、goal note 一致 |

`magpie` 是 guest 侧 sentinel 配置值，不能拿它推断真实上游认证已经进入 guest。Magpie 在宿主侧负责实际模型连接。

## 3. 过程结果

### 宿主侧首次尝试

使用严格临时 MCP 配置启动 Claude：

```text
claude --mcp-config validation/2026-10-23/claude-dock-mcp.json \
  --strict-mcp-config --print --no-session-persistence
```

第一次失败原因是模型代理返回 HTTP 200 的 HTML 页面，Claude 报 `malformed response`。MCP 网关还没有进入工具调用阶段。

用户通过 Magpie 切换模型路由后，宿主侧 Claude 成功调用 Dock MCP，返回 `task-0001 / created`。

### Guest 侧第一次尝试

Claude guest 的 sbx MCP gateway 健康检查通过，但没有 Claude 认证配置，返回：

```text
Not logged in · Please run /login
```

随后通过宿主 Magpie 入口注入非敏感 sentinel 配置，并只为该沙箱开放 `localhost:3425` 的 scoped 网络规则，模型请求成功。

### Guest 侧成功结果

Claude guest 通过 sbx 的 MCP gateway 发现 Dock 工具并调用：

```text
task.create_child(
  goal = "Claude inside sbx MCP smoke through Magpie",
  input_artifact = "sha256:guest-fixture"
)
```

Dock 返回：

```json
{
  "task_id": "task-0001",
  "state": "created"
}
```

## 4. 已验证和未验证

已验证：

- Claude Code 可以在 sbx guest 内正常启动；
- sbx MCP gateway 可以连接本地注册的 Dock MCP server；
- Dock stdio MCP 网关可以被真实 Claude 调用；
- Magpie 可以作为 guest 到模型服务的宿主代理；
- guest 不需要持有真实上游 API key；
- scoped 网络规则可以只开放 Magpie 端口；
- Dock 网关代码通过 `go test ./...`、`go vet ./...` 和构建。

未验证：

- Claude 是否能在同一任务里继续调用 `task.status` 并循环等待；
- `task.create_child` 是否真正触发 SbxBackend 创建第二个环境（本轮只创建了内存态任务记录）；
- MCP 工具参数是否经过完整的环境身份与父任务策略检查；
- Claude 的真实文件修改、测试和成果快照回收；
- 真实 MCP 服务和 Skill；
- 生产凭据、OAuth、流式模型长连接和异常恢复；
- 其他 Agent/Harness 的同等路径。

## 5. 当前代码范围

当前新增的原型代码：

- `internal/runtime/backend.go`：RuntimeBackend、EnvironmentSpec 和能力契约；
- `internal/dockmcp/service.go`：三个最小 Dock MCP 工具；
- `internal/dockmcp/service_test.go`：创建/查询和未知任务拒绝测试；
- `cmd/dock-gateway/main.go`：stdio MCP 网关入口；
- `go.mod` / `go.sum`：Go 1.26 与官方 MCP Go SDK v1.8.0；
- `validation/2026-10-24-claude-mcp-smoke.json`：宿主侧成功记录；
- `validation/2026-10-24-claude-in-sbx-mcp.json`：真实 guest 链路记录；
- `validation/2026-10-24/claude-mcp-tool-evidence.json`：从 Claude stream-json 原始事件提取并断言的两次工具调用/返回。

运行测试后，Claude 测试沙箱和临时 `pulsar-dock-local` 注册已经删除；`sbx ls` 和 `sbx mcp ls` 均为空。当前网关服务是原型：`task.create_child` 只写内存态 task record，不会创建子 VM；不要当成已经实现的生产 Controller 或完整 SbxBackend。

## 6. 下一步

下一步不是再增加 Dock 工具数量，而是把 `task.create_child` 从内存态改成真实闭环：

1. Controller 写入 SQLite 任务记录并绑定父任务、环境和授权；
2. `SbxBackend.Create/Start` 创建子环境；
3. 只给子环境注入限定的 Dock MCP 入口；
4. 传递固定输入快照；
5. 子 Agent 返回成果和状态；
6. 主 Agent 查询/等待并收到结果；
7. 用户查看差异后再决定应用成果。

每一步都沿用已落实的 RuntimeBackend 契约，Backend 失败时 fail closed，不退回宿主直接运行。
