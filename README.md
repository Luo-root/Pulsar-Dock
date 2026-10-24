# Pulsar-Dock

Pulsar-Dock 是面向人和 Agent 的桌面安全协作工作台。Agent 在受控环境中工作，用户通过 Dock 授权、观察、审阅和应用成果；Agent 可以通过 Dock 创建受限协作者、查询任务和交付固定版本成果。

当前仓库处于比赛 P0 原型阶段。历史探针验证过 Claude Code guest 经 Magpie 和 sbx MCP gateway 调用 Dock 的内存态 MCP 工具，但没有创建真实子 Agent 环境，不能作为当前完整产品闭环的证据。

截至 2026-10-24，主线已合并 SbxBackend、SQLite 任务存储、原子环境绑定、可信 Controller 创建服务和 MCP capability 校验。Controller 创建只推进到 Preparing；Start、CopyOut 和 Controller StopTask 仍明确不支持。默认 `dock-gateway` 尚未接入可信 authenticator，所有业务工具调用会拒绝。持久 MCP/Controller 接缝及 schema v2 持久撤权仍在 PR #25 审查中，尚未进入主线。真实 Agent 启动、成果交接、生产通道认证和桌面包仍未完成。

## 文档

- [产品定位](docs/01-product.md)
- [功能与交互](docs/02-features.md)
- [产品与技术架构](docs/03-architecture.md)
- [安全与兼容](docs/04-security.md)
- [三天交付与验收](docs/05-delivery.md)
- [技术选型与验证门槛](docs/06-technology-selection.md)
- [Windows 沙箱底座验证](docs/07-sandbox-validation.md)
- [Claude-in-sbx 真实竖向验证](docs/08-claude-in-sbx-validation.md)

## 本地检查

```powershell
go test ./...
go vet ./...
go build ./cmd/dock-gateway
```

Go 版本要求：1.26.10。CI 分别在 Linux、Windows、macOS 执行 Go 核心测试、vet 和网关构建；Linux 另执行竞态测试。这不代表各平台已完成 sbx、桌面 UI、安装包或签名验证。桌面打包最低目标为 Windows 与 macOS，当前尚未产出正式桌面包。

真实 Agent 测试需要本机准备 Docker Sandboxes、Magpie 和对应的 Agent 配置；测试使用的真实凭据不能进入仓库。

## 开发状态

当前工作围绕 GitHub Issue #1–#6 推进。每次代码变化通过 Pull Request 提交并关联 Issue；`main` 只接收评审后的 PR。
