# 沙箱探针与本地证据

这里保留可复用的测试源码和校验脚本。原始事件、运行结果、安装记录、临时配置和构建产物写入被 Git 忽略的根目录 `validation/`，不随源码提交。精简测试结论和未覆盖范围见 [底座验证](../../docs/07-sandbox-validation.md) 与 [Claude MCP 验证](../../docs/08-claude-in-sbx-validation.md)。

## 不依赖历史数据的检查

在仓库根目录运行：

```powershell
go test ./...
go vet ./...
go build ./cmd/dock-gateway
```

这些检查不需要历史实测记录、sbx、真实模型账户或私人配置。

只读采集当前 sbx CLI 状态，可显式指定输出目录：

```powershell
./tests/sandbox/check-cli.ps1 -SbxPath sbx -EvidenceRoot ./validation/current
```

## 实机探针准备

Go 探针是独立程序，通过显式源码路径分别构建；不要把两个 `main` 一起编译。

```powershell
New-Item -ItemType Directory -Force ./validation/current | Out-Null
go build -o ./validation/current/gateway-fixture.exe ./tests/sandbox/gateway-fixture.go
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -o ./validation/current/guest-probe ./tests/sandbox/guest-probe.go
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

`gateway-fixture` 是只监听宿主回环地址的合成 HTTP 服务，不连接真实模型、不使用真实凭据，最长运行 30 分钟。用独立终端启动，并从输出的 `gateway-routes.json` 获取 A/B 的实际端口：

```powershell
./validation/current/gateway-fixture.exe -out ./validation/current
```

运行实机检查前须自行准备：

1. 两套独立、无宿主项目挂载、共享 Skill 关闭的 sbx 测试环境；固定模板和版本，确认没有无关环境受影响。
2. 将 `guest-probe` 复制到各 guest 的 `/tmp/guest-probe` 并设置执行权限。只运行合成探针，不导入宿主私人目录或真实凭据。
3. 仅为 A 开放网关 A 的宿主端口，仅为 B 开放网关 B 的宿主端口，保留默认拒绝策略；先验证允许路径可用。
4. 在宿主创建合成 canary 文件但不复制进 guest，把预期不可见的 guest 路径传给脚本。

`run-probes.ps1` 的必填参数是 `EvidenceRoot`、`SandboxA`、`SandboxB`、`GatewayPortA`、`GatewayPortB` 和 `HostCanaryGuestPath`。端口必须取自本次 fixture，沙箱名必须指向本次合成环境。该脚本只采集指定探针及网络日志，不自动创建环境、授权出口或清理资源。guest 请求失败只是观察值，要结合正向对照和策略拒绝记录判断。

## 校验已采集的本地记录

```powershell
./tests/sandbox/check-results.ps1 -EvidenceRoot ./validation/current -SandboxA probe-a -SandboxB probe-b
```

该校验器还要求同一轮的完整文件集：`sandbox-probe-results.json`、`final-network-log-a.json`、`<SandboxA>-inventory.json`、`<SandboxB>-inventory.json`、`concurrency-results.json`、`lifecycle-results.json`、`b-server-before-stop.json`、`revocation-results.json` 和 `cleanup-results.json`。并发、停止、撤权和最终清理需由实测操作分别采集；仅执行 `run-probes.ps1` 不会生成全套记录。缺少文件时校验器会失败，不回退到历史数据。

Claude 的原始 `stream-json` 工具事件可用显式路径提取和断言：

```powershell
./tests/sandbox/check-claude-trace.ps1 -TracePath ./validation/current/claude-trace.jsonl -OutputPath ./validation/current/claude-tool-evidence.json
```

当前 Claude 校验器对应固定合成用例：goal 为 `trace-check-20261024`，创建记录后查询返回的 `task-0001`。它不是任意会话评估器；两次成功工具返回也不证明子 Agent 已启动。

复测可能下载模板或使用付费模型，不能在普通单元测试里自动执行。测试结束后只清理本轮创建的环境、端口和 MCP 注册，保留结果在本地；不要通过放开网络或宿主直接执行来让测试通过。
