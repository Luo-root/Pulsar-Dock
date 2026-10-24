# 03 · 产品与技术架构

版本：v0.1 讨论稿｜2026-10-23｜架构契约；具体技术建议与尚未满足的运行时契约见 [06](06-technology-selection.md)。06 的候选不代表下述安全要求已实现。

## 1. 总体形态

Pulsar Dock 是一个独立桌面应用，分为可信控制面、受控执行面和外部能力面。它可以调用不同 Agent，但不强迫它们共用内部实现。

```text
┌──────────────────────────── 用户桌面 ────────────────────────────┐
│  Dock UI / 可信设置 / 审批 / 任务与成果查看                       │
│           │ 本地受控 API                                         │
│     Dock Controller（策略、任务、环境、事件、成果）               │
└───────────┼──────────────────────────────────────────────────────┘
            │ 仅暴露按任务授权的接口
   ┌────────▼─────────┐       ┌────────────────┐
   │ Agent Environment │ ...   │ Agent Environment│
   │ Agent + workspace │       │ Agent + workspace│
   │ adapter/terminal  │       │ adapter/terminal │
   └────────┬─────────┘       └────────┬─────────┘
            │受控模型/MCP/消息            │受控成果/消息
   ┌────────▼───────────────────────────▼────────┐
   │ Capability Gateway / Broker                   │
   │ model proxy · MCP proxy · task/message API    │
   └───────────────┬──────────────────────────────┘
                   │
   ┌───────────────▼───────────────┐
   │ Credential & Connector Zone    │
   │ OS vault · Magpie · MCP bridge │
   └───────────────┬───────────────┘
                   │
        Magpie / compatible model service / approved MCP
```

这个图表达的是信任关系，不是已经选定的进程和容器技术。隔离实现要根据 Windows 实测决定。

## 2. 模块职责

| 模块 | 负责 | 不负责 |
|---|---|---|
| Dock UI | 选择、解释、审批、查看、手动应用 | 执行 Agent 指令、保存明文上游密钥到任务目录 |
| Controller | 身份、策略、任务图、生命周期、额度、撤权 | 直接把任意宿主目录挂给 Agent |
| Environment Manager | 创建/启动/停止/清理环境、进程树、资源约束 | 决定业务任务是否成功 |
| Agent Adapter | 将通用启动契约映射到具体 Agent 的配置、命令和事件 | 获得更高权限、绕过 Controller |
| Capability Gateway | 受控模型/MCP/任务消息/成果 API、环境身份认证与审计 | 把所有管理 API 暴露给沙箱 |
| Credential & Connector Zone | OS 凭据库中的上游认证、受限连接器、带凭据 MCP 的独立连接环境 | 把明文凭据返回给 Agent 或普通日志 |
| Workspace/Artifact Store | 副本、快照、差异、成果引用、敏感性标签、冲突检测 | 自动覆盖用户原项目 |
| Event Store | 生命周期、权限、请求和结果记录 | 记录不必要的密钥、完整敏感内容 |
| Magpie Connector | 能力探测、模型连接、独立配置生成 | 接管 Magpie 全局配置或假设所有 Agent 同构 |
| Connector Environment Manager | 创建/回收带凭据的 MCP 连接环境，绑定服务授权和网络出口 | 在宿主高权限进程中无边界运行第三方 MCP |

安全关键策略应在 Controller/Gateway 中实现，而不是只在 UI 或 Magpie 普通中间件中实现。若代理异常或超时，默认拒绝需要安全保证的请求。

## 3. 核心领域模型

```text
Project
  id, source_ref, import_policy, baseline_digest

Task
  id, parent_id, root_task_id, goal, state, requested_by, limits,
  approval_mode, approval_policy_version, comparison_group_ref

Environment
  id, task_id, agent_kind, agent_version, workspace_ref,
  policy_ref, process_ref, state, created_at, expires_at

CapabilityGrant
  id, subject(environment/task), capability, resource_selector,
  operations, expires_at, revocable, approval_ref

Artifact
  id, task_id, source_environment, snapshot_digest, kind,
  files, provenance, sensitivity, created_at

Message
  id, from_task, to_task, sender_environment, kind, body_ref,
  artifact_refs, created_at

CredentialBinding
  id, connector, subject_environment, allowed_services, allowed_actions,
  expires_at, revocation_epoch, vault_ref

ConnectorEnvironment
  id, connector, process_ref, credential_binding, network_policy,
  state, expires_at

Event
  id, task/environment, actor, type, summary, policy_decision,
  created_at
```

关键不变量：

- 每个环境属于一个任务；子任务不能脱离父任务策略单独扩大范围。
- 每个成果引用不可变快照；展示的差异对应明确基线。
- Message 是平台传递对象，不是任意环境间的 socket 或共享目录；每条消息必须由 Gateway 根据环境专属身份签发 `sender_environment`，不能由 Agent 自填。
- Grant 有主体、对象、动作、期限和撤销状态；缺项按拒绝处理。
- 每个 Agent 环境启动时获得仅能经专用通道使用的短期环境身份（候选为每环境 IPC 通道句柄或短期密钥）；Gateway 校验环境与进程/通道绑定、任务关系、签发代次、期限和单次请求重放号。Controller 重启或撤权递增撤销代次。具体绑定机制待平台探针决定，但“仅凭 task_id 或 loopback 来源”不算认证。
- 上游凭据只由 Credential & Connector Zone 从操作系统凭据库或受支持的 Magpie 接口读取，按 `CredentialBinding` 临时使用；Agent、任务普通存储、事件和成果永不获得明文。
- `Artifact` 的敏感性标签和传播策略在成果发布时确定；跨环境交接必须经过策略校验，必要时要求用户批准或脱敏，不能只凭 `artifact.read` 放行。
- 带真实凭据的 MCP 由 `ConnectorEnvironmentManager` 启动独立连接环境，绑定单一服务/授权/网络出口，并拥有独立进程树、日志与回收生命周期；不能退化为宿主高权限常驻进程。
- 日志引用敏感数据时只存摘要、哈希或受保护句柄，不把原始密钥写入事件。

## 4. 通用 Agent 适配契约

不同 Agent 的配置、模型协议、MCP 形态、启动退出和事件能力不同。适配器不追求伪造同构，而是显式声明能力。

```text
AgentAdapter
  Identify() -> kind, version, install_source
  Check(ctx, connection, runtime) -> diagnostics
  Prepare(ctx, environment, policy) -> launch_spec
  Start(launch_spec) -> process_handle
  Stop(process_handle, mode) -> result
  Observe(process_handle) -> native_events?        // 可选
  Collect(ctx, environment) -> artifact_manifest
  Resume(ctx, environment, checkpoint) -> unsupported | launch_spec
  Capabilities() -> {structured_events, resume, mcp, model_proxy, ...}
```

`Prepare` 只生成沙箱专用配置，并从可信连接获取一次性或受限入口；不复制真实上游密钥。`Collect` 不能直接把整个环境目录当成果，需按策略筛选并记录来源。

首版适配顺序建议：先选择一个真实可跑组合做 P0，再把 Claude Code、Codex、DSH 的检查结果写入兼容矩阵。目标名单不是已完成名单。

Skill 适配包含发现位置、沙箱专用副本、版本摘要、相对引用和依赖检查；能被发现与能正确执行分开记录。创建协作者通过 Dock 公共工具接口提供，不要求 Harness 原生实现多 Agent。

多 Harness 对比以 comparison_group 关联任务：固定输入快照、目标与验收标准，记录各自 Harness/模型/Skill/MCP 版本、权限和预算。每次独立运行保留自己的成果与指标；对抗审查在独立结论锁定后开启受控消息轮次，由 Controller 限制轮数与资源，不开放环境间直接通信。

## 5. 平台能力 API：给 UI 和 Agent 共用，但权限不同

路径和协议只是候选，必须在实现时确定；重要的是语义。

| 能力 | UI 使用 | Agent 使用 | 默认边界 |
|---|---|---|---|
| `task.create_child` | 人工创建 | 主任务请求 | 只能一层、父级授权、额度内 |
| `task.send_message` | 查看/回复 | 父子通信 | 只发给已授权任务，记录来源 |
| `task.await` / `task.status` | 观察 | 等待/查询子任务 | 只能查询任务图可见节点，有限时长 |
| `artifact.publish` | 导出/确认 | 提交成果 | 绑定快照和任务，不能自动应用 |
| `artifact.read` | 查看 | 读取已授权成果 | 只读句柄，不暴露无关项目 |
| `capability.request` | 批准 | 申请新增能力 | 申请不能自批准 |
| `model.invoke` | 连接检查 | 受控模型调用 | 只能使用已授模型与额度 |
| `mcp.call` | 测试连接 | 调用允许工具 | 工具、参数、时限和数据范围策略化；需要真实凭据时经 Connector Environment |
| `credential.use` | 连接检查 | 仅间接使用已绑定服务 | 只能由 Gateway/Connector 使用，永不返回明文 |

管理 API（创建任意环境、修改全局策略、读取宿主配置、批准权限、应用成果、发布外部数据）不应出现在 Agent 的能力集合中。若主 Agent 需要创建子任务，提供的是受限任务 API，而不是 Controller 的管理员 API。

## 6. 关键数据流：主 Agent 创建子 Agent

```text
主 Agent（环境专属通道） -> Dock task.create_child(goal, template, input_artifact, requested_caps)
Gateway -> 校验环境身份（通道绑定/撤销代次/期限/重放号）与调用者任务
Dock Controller -> 校验主体、父任务、额度、策略、成果引用和成果敏感性/传播规则
Dock Controller -> 若超出已授权范围，生成用户审批请求并停止
Dock Environment Manager -> 创建隔离环境与最小 Grant
Dock Adapter -> 生成子 Agent 专用配置并启动
Gateway -> 只向该环境注入环境专属通道身份，不注入上游密钥
子 Agent -> 通过 task/message/artifact 能力工作
子 Agent -> 发布结构化成果与状态
Dock -> 通知主 Agent；写入 Event Store；撤销到期能力
主 Agent -> 汇总成果
用户 -> 查看差异/测试/审查 -> 手动应用或导出
```

消息正文可以落在受保护存储中，平台记录引用、摘要和哈希。来自子 Agent 的消息是低信任输入，不能改变策略优先级。

## 7. RuntimeBackend：把隔离底座从产品职责中抽出来

Dock 不把 Docker、WSL 或某个 Windows API 写死在 Controller 里。Controller 只依赖 `RuntimeBackend`，所有底座都必须实现同一组生命周期和能力契约；底座能力不足时返回明确的 `unsupported`/`denied`，不能由 Controller 静默改成宿主执行。

```text
RuntimeBackend
  Name() -> string
  Capabilities() -> {microvm, network_policy, workspace_modes,
                     process_tree_stop, credential_proxy, port_publish, ...}
  Check(ctx) -> diagnostics
  Create(ctx, EnvironmentSpec) -> RuntimeEnvironment
  Start(ctx, RuntimeEnvironment) -> status
  Exec(ctx, RuntimeEnvironment, command, policy) -> stream
  CopyIn(ctx, RuntimeEnvironment, artifact) -> receipt
  CopyOut(ctx, RuntimeEnvironment, selector) -> artifact
  SetNetworkPolicy(ctx, RuntimeEnvironment, rules) -> result
  Revoke(ctx, RuntimeEnvironment, grant) -> result
  Stop(ctx, RuntimeEnvironment) -> result
  Remove(ctx, RuntimeEnvironment) -> result
  Inspect(ctx, RuntimeEnvironment) -> status
```

`EnvironmentSpec` 至少包含任务、Agent、固定模板/镜像摘要、CPU/内存上限、无宿主挂载或快照输入、Skill 模式、网络策略、能力网关入口和到期时间。Backend 不负责决定业务审批；它只执行 Controller 已签发的环境和网络约束，并返回真实状态与日志引用。

### 首版 SbxBackend

截至 2026-10-24，`SbxBackend` 已通过 PR #12 合入主线（Issue #3）：固定 `sbx v0.49.2` 与镜像摘要，创建无 workspace 挂载、`--skills off` 的环境，并实现身份核对、Exec、有限 CopyIn、scoped 网络策略替换、撤权和 Stop/Remove。Start 因隐式创建风险、CopyOut 因缺少传输期间字节上限而明确返回 unsupported；不能将其描述为完整运行时或成果出口。

SQLite 任务存储、环境 ID 原子绑定和可信 Controller 创建服务已分别通过 PR #11/#17/#18 合入；Controller 只推进至 Preparing，网关/TTL 参数当前拒绝，StopTask 尚未实现。PR #22 已加入进程内 MCP capability 校验，默认命令入口未配置可信 authenticator，拒绝业务调用。持久 MCP/Controller 接缝与 schema v2 撤权仍属待审 PR #25，不是当前主线保证。

既有 sbx 与 Claude 探针是独立手动/脚本实测，未验收当前 Go 适配器驱动的真实多 Agent 闭环。后续仍需验证受控网关、Agent 启动、成果交接和停止树回收；模板摘要、CLI 版本和安全设置写入事件也仍是目标要求。

SbxBackend 不把 Docker 登录、镜像拉取、sbx 审批请求或 Docker 的全局配置暴露成产品概念。桌面层只显示“安全运行环境未准备好/需要授权/模板下载失败”等可解释状态。它也不能把 sbx 的 Docker 登录状态当成 Agent 身份认证。

### 后续 LocalBackend 接缝

未来可以增加不依赖 Docker 登录的 `LocalBackend`，但必须实现同一契约，并独立通过文件、进程、网络、凭据和生命周期反例测试。没有可证明的 Windows 隔离边界时，LocalBackend 只能用于受信任务或开发诊断，不能自动接替 SbxBackend 的安全模式。

Backend 选择由安装检查和任务策略决定：安全模式只允许通过安全门槛的 Backend；不可用时阻止运行并说明原因，不降级到宿主进程。比较不同 Backend 时使用同一任务、快照、策略和验收集，记录能力差异，不把“能启动”当作“安全等价”。

## 8. 运行时与平台候选

- Agent 不能读取未授权的宿主文件；
- Agent 不能从任务目录发现真实凭据；
- Agent 不能把任意出站网络当作未审计通道；
- 子任务不能访问兄弟环境；
- 停止后子进程和临时入口能撤销；
- 沙箱内程序不能调用管理 API 修改以上规则。

## 9. 一致性与恢复

Controller 将任务和授权状态作为事实源；环境进程状态可能短暂落后。状态采用 `created → preparing → running → waiting_approval → stopping → succeeded/failed/stopped/expired`，非法跃迁拒绝并记录。

成果采用不可变快照和显式应用操作。原项目变化后应用必须重新比较基线。恢复能力由 Adapter 声明：没有真实 checkpoint 的 Agent 只能重新启动，不能把“重新运行”说成“上下文恢复”。

首版建议用 SQLite 保存任务、事件与成果索引，文件快照存本地独立目录，选型依据见 [06](06-technology-selection.md)。崩溃后的索引一致性、清理幂等和敏感文件处理仍须实测。
