# Pulsar-Dock

Pulsar-Dock 是面向人和 Agent 的桌面安全协作工作台。Agent 在受控环境中工作，用户通过 Dock 授权、观察、审阅和应用成果；Agent 可以通过 Dock 创建受限协作者、查询任务和交付固定版本成果。

当前仓库处于比赛 P0 原型阶段。第一条真实竖向链路已经验证：Claude Code 在 Docker Sandboxes guest 中，经 Magpie 模型入口和 sbx MCP gateway 调用 Dock 的 MCP 工具。当前 MCP 网关的任务记录仍是内存态，尚未创建真实子 Agent 环境。

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

Go 版本要求：1.26.10。真实 Agent 测试需要本机准备 Docker Sandboxes、Magpie 和对应的 Agent 配置；测试使用的真实凭据不能进入仓库。

## 开发状态

当前工作围绕 GitHub Issue #1–#6 推进。每次代码变化通过 Pull Request 提交并关联 Issue；`main` 只接收评审后的 PR。
