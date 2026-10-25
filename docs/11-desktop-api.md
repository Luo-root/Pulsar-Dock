# Desktop API facade

`internal/desktopapi` 是给可信桌面控制层调用的 transport-neutral Go facade。当前没有绑定 native、HTTP 或前端，也不代表前端已接入。构造必须由主机完成：注入同一个 `*controller.Service`、同一个 `*taskstore.Store`、固定 caller、固定 agent ID，以及来自可信主机配置的 catalogue。caller、agent、runtime、argv、凭据不能由 UI 请求声明。

## 构造与 Agent catalogue

```go
f, err := desktopapi.New(service, store, "desktop-user", "claude", []desktopapi.AgentConfig{
  {ID: "claude", DisplayName: "Claude", Installed: true, CLIVerified: true, ModelVerified: true,
    Operations: []desktopapi.Operation{desktopapi.OperationCreate, desktopapi.OperationExecute}},
  {ID: "codex", DisplayName: "Codex", Installed: false, Operations: nil},
  {ID: "local-review", DisplayName: "Local Review", Installed: true, CLIVerified: false, ModelVerified: false,
    Operations: []desktopapi.Operation{desktopapi.OperationCreate}},
})
```

catalogue 至少可以列出多个 Agent，但只有 `installed && cliVerified && modelVerified` 且声明操作的条目才会被 facade 接受为可调用。未验证能力不能假称 supported。`Agents(ctx)` 返回 JSON lowerCamel 字段：`id`、`displayName`、`installed`、`cliVerified`、`modelVerified`、`operations`。

## 方法

- `CreateTask(ctx, CreateTaskRequest) (Task, error)`：受限 goal、幂等键，可选已绑定的 `parentTaskId`。agentId 若提供必须等于构造时固定 agent；不接受 caller、runtime、argv、credentials。
- `GetTask(ctx, rootId, taskId) (Task, error)`：始终以构造时 caller + rootId 查询，跨 caller 或跨 root 不会串 scope。
- `ExecuteTask(ctx, rootId, taskId) (ExecuteResult, error)`：阻塞至最终命令结果；这不是 streaming。`output` 是不可信 stdout，只在成功返回，不表示 Agent 语义成功。
- `StopTask(ctx, rootId) error`：调用 Controller 的 root revoke/停止流程。
- `Agents(ctx) ([]AgentInfo, error)`：返回可信主机注入的 catalogue 副本。

Task DTO 字段：`id`、`parentId`、`rootId`、`goal`、`state`、`createdAt`、`updatedAt`。故意不返回 `callerId`、`environmentId`、runtime handle、凭据或内部错误文本。ExecuteResult 为 `task`、`commandCompleted`、可选 `output`。

明确未实现并始终返回 `unsupported`：`StartStreaming`、`RequestPermission`、`ApplyArtifact`。不得把这些操作实现为成功 no-op。

## 错误码

错误均为安全稳定的 `{code,message}` DTO，不透传底层错误内容：

- `invalid_request`：输入 bounds、编码、格式或可信构造参数不合法。
- `not_found`：任务不存在或不属于固定 caller/root scope。
- `conflict`：幂等请求或任务状态冲突。
- `unsupported`：Agent 未达到 verified 能力门槛，或 streaming/permission/apply 尚未实现。
- `canceled`：context 被取消或超时。
- `unavailable`：Controller/runtime 拒绝、恢复要求或其他安全失败；不要据此重试创建/执行。

## 当前缺口

本包未提供 UI transport、事件推送、增量 stdout、权限弹窗、artifact apply 或自动 Agent 探测。真实运行仍须由主机注入已验证的 Controller/runtime/planner/verifier；测试使用 synthetic backend，不运行真实 Agent、不读取真实凭据、不接公网。
