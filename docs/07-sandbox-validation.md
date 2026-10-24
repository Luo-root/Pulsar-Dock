# 07 · Windows 沙箱底座验证记录

日期：2026-10-23｜对象：Docker Sandboxes sbx v0.49.2｜范围：本机原型底座验证

## 1. 结论

**本轮结果支持继续用 sbx 开发 Pulsar Dock P0 原型。** 已实际启动两套无项目挂载的 Linux microVM，验证指定路径不可见、抽样文件状态隔离、网络默认拒绝与分环境允许、跨环境端口访问拒绝，以及停止后测试服务活动未延续、测试文件保留。

此结论针对下面固定版本和配置，不是安全认证、完整渗透测试或三款 Agent 的兼容声明。模型与凭据通路本轮用自建合成 HTTP 服务验证，**没有验证真实模型、SSE 流式、Magpie、MCP、Skill、系统凭据库或 sbx 上游密钥注入**。

环境专属入口验证的是“各沙箱只能访问自己的网关端口 + 网关检查对应虚拟令牌”的组合。不是不可复制的 token、硬件身份绑定或完整防重放机制；生产实现必须保持每环境入口和策略分离，不能换成所有沙箱可访问的公共 bearer-token 入口后沿用结论。

## 2. 本次实际环境与改动

| 项目 | 状态 |
|---|---|
| 宿主 | Windows 11 家庭版 x64，build 26200，约 31.7GiB RAM |
| Go | 从 1.24.0 升到 1.26.10，原安装目录 `D:\Backend\go` 保持不变 |
| Go 安装证据 | 官方 MSI SHA-256 匹配，Google LLC Authenticode 有效，MSI exit=0；最小 HTTP/JSON/SHA-256 程序实际编译运行通过 |
| sbx | v0.49.2，commit `5d40fd43027b33ed1f1a39c14fb07e2503d6998f`；官方 MSI 哈希与 Docker Inc 签名通过，安装 exit=0 |
| Windows Hypervisor Platform | 从 Disabled 启用为 Enabled；系统当时返回 RestartNeeded=true，本轮未主动重启，但 sbx 虚拟化诊断与实际 microVM 启动成功 |
| 登录 | 用户在官方页面完成 Docker 授权；CLI 登录成功；未获取或导出用户密码、token |
| Docker Desktop | 引擎未运行；本轮 sbx 未依赖它启动 microVM。Docker CLI 的 manifest inspect 仅用于查官方镜像摘要 |
| 实测镜像 | `docker.io/docker/sandbox-templates@sha256:fd0013ca46d457583c9d68382e88b7c73472b4611a37012e7987d45c5ec19e3b`，claude-code-minimal 的 linux/amd64 manifest |
| guest 执行方式 | 使用 `shell` 适配启动，并运行 Go 交叉编译合成探针，未执行 Claude 模型任务 |
| 两套环境 | `pulsar-probe-a-20261023` / `pulsar-probe-b-20261023`，每套 2CPU、2GiB，无 workspace 参数、`--skills off` |

新 sbx 安装在原先无环境基线 `[]` 下初始化默认拒绝网络策略，没有 reset 用户已有沙箱。为验证修改并保留的设置如下：

```text
ssh.agentForwardingEnabled = false
browser.enabled = false
clipboard.imagePaste = false
skills.defaultMode = off
diagnostics.autoUpload = no
mcp.forceLocalGateway = true
```

运行命令使用 `SBX_NO_TELEMETRY=1`，没有上传诊断包。没有修改 Magpie、用户日常 WSL 配置或真实项目内容。安装器与镜像下载产生本地缓存，详见收尾部分。

## 3. 已执行的验证

| 检查 | 预期与实际结果 | 范围判断 |
|---|---|---|
| 双 microVM 并发 | 两个环境同时 `running`，各自 2CPU/2GiB，宿主出现两个 runtime shim；启动标识不同 | 通过本轮两环境并发样本，不代表容量/压力测试 |
| 无项目挂载 | guest mountinfo 未发现项目/共享 Skill；仅见运行时 resolv.conf 挂载；合成宿主 canary、`/mnt/c/Users/...`、`/mnt/d/Backend`、`/run/sandbox/source` 均不存在 | 指定路径与无挂载配置测试通过；不是遍历所有逃逸路径 |
| 文件隔离 | A 写 `/tmp/pulsar-probe/only-a.txt` 成功，B 同路径不存在；成果可导回宿主，SHA-256 `721b5c7dd249ddad41a01cfc21b22a7edc05c7fbe13d7e1a9763606b275ef9cb` | 文件状态隔离与复制通路通过 |
| SSH Agent | `SSH_AUTH_SOCK` 环境变量仍设置，但 socket 不存在；两环境 `ssh-add -l` exit=2、无法连接 | SSH 转发不可用；不能仅凭变量是否存在判断 |
| 允许的宿主入口 | 未授权时 A→localhost:27871 返回 403；添加仅 A 适用规则后返回 200 | 策略改变确实影响真实请求，正反对照通过 |
| 合成模型调用 | A→A 网关 /invoke + A 虚拟令牌返回 200；B→B 网关同样成功；错误令牌返回 401 | 验证基础 HTTP 与认证判断，不是真模型/流式协议 |
| 借用另一环境令牌 | 把 B 的合成令牌提供给 A 后，A→B 端口仍返回 403；B→A 同样拒绝 | 分环境端口策略约束通过；不证明令牌本身不可复制 |
| 默认拒绝公网 | example.com 经 forward proxy 返回 403；绕过环境代理直接 DNS 被拦 | 有明确 proxy policy 日志，不是把联网失败误当安全 |
| 直连 IPv4/IPv6 | 1.1.1.1:443 和 `[2606:4700:4700::1111]:443` 直接请求失败，transparent proxy 日志记录 default deny | 两个具体 IP 出口测试通过；UDP/ICMP等未做完整实测 |
| guest root 直连 | 以 guest root 请求 1.1.1.1:443 仍失败，并在最终日志中记录拒绝 | 说明此项出口约束不依赖普通用户自律，非完整 root 攻击测试 |
| 未授权宿主服务 | guest→host.docker.internal:3425 返回 403，有拒绝日志 | 未开放已有模型网关端口；无真实模型消费 |
| 跨环境服务 | B 合成 HTTP 服务在 B loopback 返回 200；A 请求 B guest IP 失败，请求 B sandbox 名返回 403 | 在当前默认拒绝策略下未能互访；未证明主动放开目标仍不能直连 |
| 虚拟令牌撤销 | 宿主撤销 A 合成令牌后，原本成功的 /invoke 返回 401 | 合成网关撤销机制通过；sbx stop 不会自动代 Dock 撤销业务令牌 |
| 单环境网络撤权 | 删除 A→27871 allow 后，相同入口返回 403 | 动态网络撤权通过，已在允许状态实际成功作过对照 |
| 显式停止 | B 持续 HTTP 父进程 + 心跳子进程运行时 stop 用时约 662ms；CLI 收到 143；宿主转发端口无监听 | 本轮停止与端口回收通过；单样本不是性能承诺 |
| 停止后重启 | 新 boot_id；旧服务端口未监听，心跳文件保留且不再更新 | 磁盘保留、旧进程不续活；不是 Agent 内部上下文恢复 |
| 结束清理 | 导出合成成果后删除本轮 A/B，`sbx ls` 为 `[]`；无 runtime shim；测试监听端口数=0 | 本次环境/进程/端口已清理 |

## 4. 实测发现对实现的影响

1. **应用要控制每环境入口。** 本轮通过每环境独立 localhost 端口与 scoped allow 区分 A/B；共享地址只靠可复制 token 不具有相同隔离效果。生产网关仍需限时、限权、请求去重和撤销状态。
2. **CLI 短命令可能自动停止空闲环境。** `exec`、`cp` 可能自动启动环境，短请求结束后又显示 stopped；不得以一次状态读数推断手动停止已执行，也不得在“停止后检查”时不知不觉把它重新启动。持续服务与宿主端口提供了本轮停止对照。
3. **默认拒绝会产生 sbx 审批请求。** 未匹配出口返回 403 和“Approval required”，不是 Dock 审批。需要在产品中统一呈现/明确拒绝，不自动代用户批准。
4. **模板启动脚本仍尝试联网。** 日志观察到 apt/GitHub CLI/Docker 下载请求被拦。原型模板应预装依赖，不能为消除提示放开通用网络。此轮没有验证 guest 内 Docker build 功能。
5. **镜像分发是实际成本。** 完整 shell 模板先后出现 TCP 中断与 registry auth 错误；固定 amd64 摘要减少多架构拉取，最终 minimal 摘要成功。需固定镜像摘要、复用缓存并提供明确的失败恢复体验，不能承诺新机器秒级启动。
6. **安装与重启状态要按实际能力判断。** Windows 功能启用曾返回需重启，但不重启也完成本次 microVM 测试；安装向导仍应检查实际能力并尊重系统后续重启要求，不能一概说“不需要重启”。

## 5. 没有验证的事项

- Codex、Claude Code、DSH 的模型任务、独立认证目录、真实流式输出、PTY/ConPTY、Ctrl+C/窗口尺寸。
- 真实上游密钥在 Windows Credential Manager、Magpie 或 sbx proxy 中的保存与注入、错误回显、转储与成果包泄漏扫描。
- MCP 各传输、OAuth、远端资源与反向能力；带真实凭据的第三方 MCP 独立连接环境。
- Skill 加载、触发、相对引用、脚本依赖和恶意 Skill 回归。
- 主 Agent 真调用 Dock 创建子 Agent 的端到端产品闭环；重复创建、父子权限继承、预算扣除。
- 宿主 Controller 崩溃时孤儿 VM 自动回收；OS 重启恢复；长期稳定性与最大并发。
- UDP/ICMP/协议走私、恶意 DNS、运行时内核漏洞、所有文件链接/硬链接路径与完整逃逸攻击。
- mTLS/通道硬绑定、token 单次重放防护、不可逆数据流控制、只读输入不可篡改。
- 安装包再分发与集成授权条款；Wails/UI/SQLite/MCP SDK 的完整依赖构建。

## 6. 证据与复测入口

工作区：`D:\Github-Project\pulsar dock`。

- 探针源码：`tests/sandbox/guest-probe.go`、`gateway-fixture.go`。
- 执行记录脚本：`tests/sandbox/check-cli.ps1`、`run-probes.ps1`。
- 具体输入、返回值与策略日志：`validation/2026-10-23/sandbox-probe-results.json`、`final-network-log-a.json`、`final-network-log-b.json`。
- 配置与 guest 清单：`effective-settings.json`、`pulsar-probe-a-20261023-inventory.json`、`pulsar-probe-b-20261023-inventory.json`。
- 并发、生命周期、撤权与清理：`concurrency-results.json`、`lifecycle-results.json`、`revocation-results.json`、`cleanup-results.json`。
- 安装：`hypervisor-setup-result.json`、`elevated-setup-result.json`；安装完整日志留本机，不随精简报告打包。

`run-probes.ps1` 使用本轮固定的 A/B 名称、guest 二进制路径及合成网关端口，不是无需准备即可重跑的生产测试器；下次须先重新创建同配置环境、编译复制探针并启动 fixture，修改端口与 scoped allow 后运行。guest 探针网络错误是观察值，最终判定结合正向对照和策略日志，不能只看脚本 exit=0。独立只读核对确认主要网络与并发证据，并要求收窄挂载、SSH 与进程结论；本报告已按指定路径、SSH Agent 和测试服务活动范围表达，不据此宣称所有协议或全部进程树均已验证。

## 8. 真实 Claude 竖向链路补充

2026-10-24 使用本机 Claude Code 2.1.300 完成一条真实链路：

```text
Claude Code（sbx guest，无 workspace、skills off）
  -> sbx local MCP gateway
  -> pulsar-dock-local（临时注册）
  -> Dock stdio MCP gateway
  -> task.create_child
```

模型请求路径为 `Claude guest -> host.docker.internal:3425 -> Magpie -> deepseek/deepseek-flash[1m]`。guest 只收到模型入口和 `magpie` sentinel，未注入真实上游 key。Claude 成功返回 `task-0001 / created`；宿主和 guest 的 Claude MCP gateway health 均通过。测试后已停止并删除 Claude 沙箱，并删除临时 `pulsar-dock-local` 注册；最终 `sbx ls` 与 `sbx mcp ls` 均为空。

## 9. 清理与下一步

已删除两套合成测试环境及其环境规则/转发资源，也已删除真实 Claude 接入测试沙箱和临时 `pulsar-dock-local` 注册；保持 sbx 已安装与安全设置，合成网关已退出。Go 的安装目录和 GOPATH 保持原位置。

本机保留固定模板镜像缓存，便于后续 Agent 适配，不做全局 prune。工作区安装下载、构建缓存和测试二进制已移入回收站；报告包只保留源码与精选结果，不携带 MSI、二进制或用户真实凭据。

下一步继续做真实 Agent + Dock 网关 + MCP 协作工具的竖向实现：把当前内存态 `task.create_child` 换成 Controller/SQLite 任务记录，接入 SbxBackend 的环境创建，再验证子 Agent 启动、固定快照交接和成果回收。此报告只放行继续原型开发，不放行生产部署。
