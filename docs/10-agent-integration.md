# Agent 接口与沙箱验证

本页是 UI 后续对接的当前能力边界，不是三 Agent 全部验收通过的声明。关联 Issue #57。

## 2026-10-25 Windows 实测

环境：Windows x64，sbx v0.49.2，RuntimeBackend 固定 OCI digest（见 `internal/runtime/sbx.go`），2 CPU / 2g，无 workspace mount，skills off，网络默认拒绝。不复制宿主凭据，不调用模型。

| Agent | 真实 guest 结果 | 当前适配状态 |
| --- | --- | --- |
| Claude Code | shell guest 中 `--version` 为 2.1.306；`--help` 包含现有计划器的受限参数 | 将精确版本 pin 从 2.1.300 更新到 2.1.306；仅 CLI 契约，不代表模型/MCP/ACP 已通 |
| Codex | shell guest 无二进制；codex 类型 guest 创建时因 `policy: denied: unexpected allow rule` 被拒绝，随后 `sbx ls --json` 为 `[]` | 保留 ErrUnsupported，不放宽网络规则，不回退宿主 |
| DSH | shell guest 无二进制；本地 DSH 插件状态 requires_install | 暂跳过；需要独立 guest provisioning 与固定依赖；没有运行验证 |

扩展常见 CLI 入口清单后，真实 shell inventory PASS 97.38 秒：`claude` 为 2.1.306；`codex`、`copilot`、`cursor-agent`、`devin`、`docker-agent`、`droid`、`gemini`、`kiro-cli`、`opencode`、`dsh` 均未在 agent 用户 PATH 找到。这是入口清单探针，不是全文件系统安装审计；不证明各自默认 Agent 专用镜像不可用。sbx create 的受支持类型不等于此固定镜像预装清单。

首轮真实 shell inventory PASS 77.09 秒。测试 PASS 表示清单探针执行完毕，ABSENT 明确表示缺失，不表示该 Agent 支持。Codex 专用 guest 探针 FAIL 41.63 秒；保留该失败，不以全量 Go 单测替代。

sbx 的只读状态查询仍出现 `docker hub refresh lock held by another process` 警告，准备可靠性未解决（#32）。

## 可重复检查

```powershell
$env:SBX_NO_TELEMETRY='1'
$env:PULSAR_REAL_AGENT_CONTRACTS='1'
$env:PULSAR_AGENT_CONTRACT_CODEX='0'
go test -tags sandboxsmoke -count=1 -v ./tests/sandbox -run '^TestAgentGuestContracts$'
```

只有显式 opt-in 才创建真实环境。使用 `PULSAR_AGENT_CONTRACT_CODEX=1` 检查 codex 类型 guest；不安装缺失 CLI，不自动改变网络。不读取配置、环境变量或认证状态；只记录版本、固定 flag 存在性。`--help` 存在 flag 不证明组合执行或语义兼容。

## UI 应接的边界

当前 UI owner 工作树为 `validation/worktrees/chat-desktop-redesign/frontend`。其 `HostBridge` 只有 `getSnapshot` 与 `subscribe`，Agent/model/approval/运行模式选择和发送消息仍为本地预览。不得把预览配置当作已授权执行。

后续可信桌面 facade 应分别提供（下列为待实现需求，不是现存 API）：

- Agent 能力检测：返回 guest/template/version、检查层级与 unsupported/blocked 原因。不要只有一个 verified 布尔值。
- 创建与执行：UI 只提交受限目标和可信配置引用；宿主选择固定 caller、policy、planner 与 guest verifier。禁止前端提交任意 argv、环境变量、凭据和 RuntimeBackend 实例。
- 状态与事件：区分 command completed、任务语义验收、模型错误与基础设施错误；stdout 是不可信数据，不是 artifact。
- 停止与撤权：通过 Controller StopTask 的持久整树撤销及 owned runtime 清理；不能只取消页面订阅。
- 权限与成果：权限请求绑定 task/environment/session generation；成果发布不等于应用，应用仍需独立人工操作与 baseline check。

现有 `Controller.ExecuteTask` 要求 Preparing、拥有的环境、Inspect 一致性、GuestVerifier、Planner、执行 lease 和撤权复核；本次没有绕过这些约束。尚无真实平台 GuestVerifier（可信 PATH、binary provenance、MCP config 内容/所有权、身份通道）。

## ACP 与 DSH

PR #55 只有 transport-neutral Client/Fake，未合入本分支，不是 SDK/真实 transport。RuntimeBackend.Exec 只返回最终 string（64 KiB 上限），无 stdin 或持续双向流；不能将其宣称为 ACP streaming。真实 ACP 还需要现成 client SDK、受控 guest 双向 transport、任务与环境身份绑定、generation 防旧事件、cancel/permission/session lifecycle 与 focused security tests。

DSH 官方当前 master CLI 文档声明 `dsh --profile acp` 为 ACP stdio，`dsh --profile headless "job"` 为一次性任务；此为源码研究，不是 rc.8 安装运行结果，也不说明该固定包自带相同 profile。npm rc.8 元数据确认 bin 为 `dsh -> lib/bin.js`，有大量版本范围依赖；只 pin 顶层包不足以固定完整供应链。当前未安装、未启动，不将 Web 插件进程当 guest Agent。

来源：
- https://raw.githubusercontent.com/deepseek-ai/deepseek-harness/master/apps/cli/README.md
- https://raw.githubusercontent.com/deepseek-ai/deepseek-harness/master/apps/cli/src/args.ts
- https://registry.npmjs.org/@deepseek-ai/dsh/0.1.0-rc.8

## 尚未验收

真实模型调用、认证代理的最小授权通道、guest MCP 授权工具往返、成果交接、ACP 双向 prompt/cancel/permission、Codex/DSH guest provisioning、Windows/macOS 桌面桥和安装包均未通过本次验证。CLI inventory 不能作为这些项的完成证据。
