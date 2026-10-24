# 06 · 技术选型与验证门槛

版本：v0.2 选型建议｜2026-10-23｜Go 已升级，sbx 本机基础实测见 [07](07-sandbox-validation.md)；Dock 应用与真实 Agent/MCP/Skill 尚未联调

## 1. 选型结论

建议以 **Wails v2 + React/TypeScript + Go + SQLite + 官方 MCP Go SDK** 构建 Dock，**Docker Sandboxes 独立 sbx v0.49.2 的本机基础测试已支持进入 P0 原型实现阶段**。采用限定在 07 的配置与证据范围；生产凭据、真实模型/MCP、完整 Agent 与 Skill 接入仍待验证。

这份文档把前面的职责设计落实到技术，不代表依赖已安装或组合已跑通。区分三个状态：**建议采用**＝技术方向明确；**有条件采用**＝存在必须先过的集成门槛；**未验证**＝没有执行实测。没有将建议改成用户已批准的决定。

| 部位 | 建议选型 | 为什么 | 状态 |
|---|---|---|---|
| 桌面壳 | Wails v2 稳定分支；候选 v2.17.0 | Go 业务直接接桌面，生成 TS 绑定；首版单窗口足够 | 建议采用，未构建 |
| UI | React + TypeScript + Vite；npm 锁定依赖 | 任务树、审批、差异、连接中心用同一套组件；未来可复用展示层 | 建议采用，补丁版本待构建锁定 |
| 控制服务 | Go，模块化单体 | 进程、网络代理、并发任务、策略和跨平台适配集中维护 | 建议采用 |
| 任务执行 | Go 状态机、context 取消、限额调度；SQLite 事务持久化 | 先实现本地父子任务、消息和成果，不引入分布式服务 | 建议采用 |
| 状态存储 | SQLite + database/sql + modernc.org/sqlite | 无需外部数据库，纯 Go 驱动降低 Windows CGO 配置成本 | 候选 v1.61.0，未构建 |
| 成果存储 | 本地文件快照 + SHA-256 清单；SQLite 存索引 | 二进制/大文件不塞数据库；独立输入与输出便于比较 | 建议采用 |
| Agent 调 Dock | 官方 modelcontextprotocol/go-sdk；stdio 适配桥接到受控网关，HTTP 按 Agent 能力选用 | 创建子任务、通信和交付可成为普通工具，不依赖 Harness 原生多 Agent | 候选 v1.8.0，未联调 |
| 模型与 MCP 网关 | Go net/http，协议匹配转发；Magpie 可选做模型转换 | 策略留在 Dock，复用 Magpie 的协议能力；不重做模型路由平台 | 建议采用，Magpie 接口未实测 |
| 上游凭据 | Windows Credential Manager；数据库仅保存句柄 | 使用系统凭据库，不把密钥写进任务文件或普通配置 | 建议采用，Go 封装与权限实测待做 |
| CLI 展示 | xterm.js + Go 终端会话管理 | 保留原生 CLI 体验；xterm 只负责输入显示，不承担 PTY 或隔离 | 有条件采用，PTY 通路待验证 |
| 隔离运行时 | sbx v0.49.2 本地 microVM，Linux Agent 环境 | 每环境独立内核、分环境出口策略；实测范围见 07 | 基础实测支持 P0 原型；完整接入待测 |
| 测试 | Go tests/race + 前端单测 + 浏览器 E2E + Windows 实机集成 | 策略、交互、进程和隔离分别测试 | 建议采用；网页测试不等于桌面壳测试 |

首版仍是一个桌面产品，不为上述每一项拆仓库或启动一个服务。Wails UI、Controller、策略、网关和存储可以在同一个 Go 程序内按模块分开；权限端点必须区分。隔离运行时是独立进程，接口留在 `RuntimeBackend` 后，后续可替换。

## 1.1 RuntimeBackend 已落地的职责边界

Dock 的 Controller 只依赖 `RuntimeBackend`，不直接调用 sbx、Docker、WSL 或 Windows API。Backend 必须统一提供：

- `Check`：检查底座、模板、虚拟化和本地依赖；
- `Create/Start/Stop/Remove`：管理环境生命周期；
- `Exec/CopyIn/CopyOut`：执行命令和成果快照交接；
- `SetNetworkPolicy/Revoke`：按环境收紧出口和撤销能力；
- `Inspect/Capabilities`：返回真实状态与能力差异。

本接口已经写入架构契约，首版实现为 `SbxBackend`：固定 sbx v0.49.2、固定 linux/amd64 模板摘要、无 workspace、`--skills off`、每环境 scoped allow。Docker 登录、镜像拉取和 sbx 审批只作为 Backend 的准备状态，不出现在 Dock 的业务模型中。

未来的 `LocalBackend` 只能在通过同一组文件/进程/网络/凭据/生命周期反例后接入；没有安全证据时只允许受信任务，不能在安全模式下自动替代 `SbxBackend`。

当前首个真实 Agent 路径已通过：Claude guest → Magpie 模型入口 → sbx MCP gateway → Dock stdio gateway。下一步实现 Controller/SQLite 任务记录和 SbxBackend 的 `Create/Start/Exec/CopyOut/Stop/Remove`，把目前内存态工具结果接到真实子环境与成果快照。

只读命令实际返回：

| 检查项 | 结果 | 对选型的影响 |
|---|---|---|
| 系统 | Windows 11 家庭版 中文版，build 26200，x64 | 不能把 Windows Sandbox 当现成可用组件 |
| 内存 | 约 31.7 GiB | 可以尝试两个环境，但并发容量尚未测量 |
| 虚拟化 | HypervisorPresent=True；VirtualMachinePlatform 已启用 | 不等于 `sbx` 所需 Windows Hypervisor Platform 已启用 |
| Windows Hypervisor Platform | 本轮由 Disabled 启用为 Enabled，操作返回 RestartNeeded=true | 未主动重启即完成两 microVM 实测；仍保留系统重启提示，不用 Enabled 单项代替能力检查 |
| WSL | Ubuntu 运行中，版本 2 | 可开发 Linux 工具，但不把用户日常发行版当安全环境 |
| Docker | 客户端 28.3.0；desktop-linux 引擎管道不存在 | 引擎当前不可用，未尝试启动；不影响选择独立 `sbx` 的资料判断 |
| Go | 1.26.10（本轮由 1.24.0 升级） | 版本检查、最小编译运行通过；完整依赖构建待测 |
| Node | 22.16.0 | 构建组合仍要锁版本并检查 engines |
| Wails CLI | 2.10.2 | 不能视为候选库 v2.17.0 的已配套 CLI |
| sbx | v0.49.2 已安装并由用户完成登录 | CLI、守护进程与两环境实测见 07 |
| rustc、cargo | 初始检查 PATH 未发现，本轮未安装 | 当前 Wails + Go 路线不需新增 Rust 工具链 |
| 项目 | 仅设计文档，未发现 go.mod/package.json | 当前没有可复用的项目依赖锁 |

已读取版本元数据：Wails v2.17.0 要求 Go ≥1.25.0；MCP Go SDK v1.8.0 要求 Go ≥1.25.0；modernc.org/sqlite v1.61.0 要求 Go ≥1.26.0。[S1–S3]

**开发工具链采用 Go 1.26 系列，当前已升级为 Go 1.26.10。** 用户已明确选择 1.26；2026-10-23 使用官方 Windows x64 MSI、校验 SHA-256 和 Google LLC 签名后，在原目录 `D:\Backend\go` 完成升级，并实际编译运行 HTTP/JSON/哈希最小程序。完整依赖图与桌面构建仍未测试。Wails CLI 与库版本保持一致；前端 package-lock、Go go.sum 和环境镜像摘要进入版本管理，比赛期间不跟随 `latest`。

P0 已通过 `SbxBackend` 的底座基础实测，但这不等于真实 Agent 接入完成。桌面层应显示 Backend 状态：未准备好、需要登录、模板下载中、策略拒绝、运行中、已停止、清理失败；不把 Docker 账号状态显示为 Agent 身份。

## 2. 首个真实 Agent 竖向接入

首个接入对象选择本机已安装的 Claude Code。临时 MCP 配置使用 `--mcp-config` + `--strict-mcp-config`，不修改用户全局配置。Dock 网关首版用 stdio 暴露 `task.create_child`、`task.status`、`artifact.publish`，后续由 SbxBackend 把同一入口装配到 guest。

2026-10-24 已完成真实 Agent 竖向路径：Claude Code 2.1.300 在 sbx v0.49.2 无 workspace、`--skills off` guest 内运行；通过 sbx MCP gateway 加载 `pulsar-dock-local`，再由 gateway 连接 Dock stdio 网关，成功调用 `task.create_child`，返回 `task-0001 / created`。模型请求经 `host.docker.internal:3425` 到 Magpie，guest 只注入 `ANTHROPIC_BASE_URL` 和 `magpie` sentinel，不注入真实上游 key。

此前宿主 Claude 临时会话第一次失败是模型代理返回 HTML；切换 Magpie 模型路由后宿主和 guest 均成功。Guest 首次失败是未登录 Claude；通过 Magpie sentinel 路径后成功。该结果证明一条真实 Claude → Magpie → sbx MCP gateway → Dock stdio gateway 路径，不代表其他 Agent、真实 MCP 凭据、上游密钥注入或会话恢复已经完成。证据见 `validation/2026-10-24-claude-in-sbx-mcp.json`。

## 3. 为什么选 Wails v2

Wails 官方把 v2 标记为 Stable、v3 标记为 Beta。[S5] 主作者熟悉 Go，首版需要单窗口工作台、对话框、任务状态和终端，Wails v2 与这些条件匹配。

| 候选 | 优点 | 本项目的取舍 |
|---|---|---|
| Wails v2 + Go | Go 业务直接接入，使用系统 WebView，减少语言和进程桥接 | 首选；跨平台 WebView、PTY、打包仍需实测 |
| Electron + Go sidecar | Chromium 行为较统一，适合复杂网页宿主和多视图 | 备选；增加 Node/Chromium 分发更新、IPC 与子进程管理工作 |
| Tauri + Go sidecar | 系统 WebView，可建立细粒度命令桥 | 不作首选；当前还需 Rust 工具链与额外 Go sidecar 接线 |
| Wails v3 | 更广的桌面 API 方向 | 当前 Beta，不把首版关键路径押在预发布版本上 |

这不是性能跑分结果。Wails 不自动比其他框架更安全；安全取决于原生桥暴露面、内容来源和 Go 服务校验。

实现约束：只在可信主窗口加载打包 UI；不把 DSH Web、项目预览页或任意 Agent HTML 加载进带管理绑定的主 WebView。首版可通过仅本机可访问的受控端口在系统浏览器打开，Dock 提供入口、状态与停止。浏览器入口也需鉴权与 Origin/CSRF 校验，不能把“外部打开”当安全措施的全部。若以后要求所有不可信网页都内嵌，再专门验证隔离 WebView 能力，必要时重评桌面壳。

终端流采用有界队列、批量发送与背压；Ctrl+C、窗口尺寸、交互输入和断开恢复必须测试。Windows 侧 `sbx` CLI 若需要伪终端，应接 ConPTY；若有可靠的 guest 终端协议，可直接连接。两条路径尚未实测，不提前选定第三方 PTY 库。普通 stdout 管道能展示日志，不代表交互终端已完成。

## 4. 为什么优先验证 sbx

Docker 当前官方资料说明：独立 `sbx` 无需 Docker Desktop 或 Docker Engine；本地 Windows 支持要求 Windows 11、x64 Intel/AMD 和 Windows Hypervisor Platform。每个本地沙箱有自己的 Linux 内核，网络经过宿主策略代理，沙箱不能直接互连。[S6–S8]

这更接近 Dock 的外部边界需求，能让开发集中在授权、Agent 适配、任务协作与成果，而非三天内自制完整虚拟化平台。

| 备选 | 判断 |
|---|---|
| Windows Sandbox | 微软文档注明不支持 Home，当前不能同时运行多个实例；与本机和多 Agent 并行目标不匹配 [S9] |
| 普通 Docker Desktop Linux 容器 | 可用作受限开发验证，但容器间共享内核，不等价于每 Agent 独立 microVM；Docker socket、网络、挂载等需另做边界 |
| 用户日常 WSL 发行版 | 有宿主盘/进程互操作等集成，不能直接视为沙箱；不要让 Agent 接管现有 Ubuntu |
| 自管 VM / 自研 Windows 进程沙箱 | 可作为长期替代，但增加网络、镜像、权限与生命周期维护成本，不是三天默认路线 |
| Codex 自带沙箱 | 可研究其权限与网络机制；其命令沙箱不覆盖所有模型、MCP、浏览器等服务路径，不能直接替代整个多 Harness 运行环境 [S10] |

### 必须改变的默认配置

`sbx` 的官方默认行为与 Dock 需求有差距，不能套上 UI 就宣称满足设计：

1. **项目用无宿主挂载环境。** 用 `sbx create` 不传 workspace，再经受控传输导入筛选快照；不能裸用 `sbx run`，它默认挂载当前目录。其 clone 模式仍会只读暴露原目录的未跟踪文件和 `.env`，还会修改原仓库 remote，不符合 Dock 默认不暴露原目录的要求。[S7–S8]
2. **关闭共享 Skill。** 创建时 `--skills=off`，只注入用户选定、固定版本的副本；默认共享库会扩大跨环境影响范围。[S11]
3. **只开放 Dock 能力入口。** 明确核对 global、sandbox、kit 三种规则。Locked Down 只是无基线允许项，kit 仍可能追加规则；已有 global allow 也不能忽略。必须证明实际出口仅为计划内目标，不能只看 preset 名称。[S12]
4. **关闭 SSH Agent 转发与不需要的宿主集成。** 官方说明 SSH Agent 默认转发；私钥未复制也可被用来签名/认证。剪贴板等入口也需按 Dock 策略限制并测试。[S8]
5. **不直接注册任意宿主 stdio MCP。** 官方 MCP gateway 的本地 stdio 服务运行在宿主，不在 Agent VM 内；需要真实凭据的第三方本地 MCP 仍须有受控连接环境。[S7–S8]
6. **不修改用户所有 sbx 环境的全局策略。** 优先用受管命名环境及 scoped 规则；若无法把继承的宽权限收紧到 Dock 需要的集合，又无法安全隔离配置，就判定该集成方案不满足首版要求。不能擅自 reset 全局配置。[S12]

本地 `sbx` 使用需要 Docker 登录；官方称 CLI 与本地算力可免费商用，组织治理另收费，CLI 默认有使用统计并提供关闭设置。[S13] 免费使用不等于允许嵌入发行、再分发或源码可审计；分发条款未核查，首版设计按独立外部依赖连接，不直接打包冒充自研底座。登录/地区网络可用性也是采用门槛。

## 5. 网关、身份与凭据怎么接

建议路径：Agent → 沙箱内 Dock 工具/协议适配 → 宿主 Dock 能力网关 → 选定 Magpie/模型/MCP。

- **给 Agent 的协作工具**：官方 MCP Go SDK 实现 `task.create_child`、消息、成果和权限申请。stdio 只作为兼容桥；它在沙箱内，不能自己执行宿主动作。需要时桥接到宿主网关，所有判断在宿主完成。Streamable HTTP 能直接使用时可省略桥，不能假定每个 Harness 均支持同一传输。
- **模型连接**：受控协议兼容代理；Go 负责认证、允许模型、超时、用量与撤权，Magpie 可选负责协议转换。真实密钥留在已连接的可信服务或 OS 凭据库；不为了接入再导出 Magpie 全部凭据。
- **宿主入口**：`sbx` 文档提供 `host.docker.internal` 与指定 localhost 端口允许规则。[S14] 只开放能力端口，不开放 UI 管理入口、Magpie 管理端口或整个 loopback。
- **环境身份**：每环境随机短期凭证，服务端查主体和授权；伪造 body/header 中的 task_id 无效，停止立即撤销。有条件使用每环境专属连接或可验证的运行时来源绑定。`sbx` 代理可能让连接表现为宿主来源，不能靠 remote IP 识别环境。
- **环境入口实测**：A/B 各自使用独立宿主端口，sbx scoped allow 只开放所属端口。即使把 B 的合成令牌提供给 A，请求 B 端口仍返回 403。它支持“每环境入口隔离 + 应用认证”的候选实现，不能等同于凭证不可复制或通用密码学来源绑定；不在受管 scope 下放宽出口是采用前提。请求重放号、期限、生产撤权和公共入口安全仍需产品实现与实测。
- **凭据库门槛**：Windows Credential Manager 保护本地保存，不保证阻止所有同用户恶意宿主进程。输入 UI 短暂接触密钥，网关使用时也可能有明文内存；不记录、不回显、不进普通备份，错误和转储策略要测试。读取失败时不回退到明文 JSON。

Dock 的三种审批模式实现于同一 Go Policy Service；模式影响询问频率，不取消运行时边界。Harness 自己的审批机制独立存在，不能仅切换 Dock 模式就宣称所有内部命令也受同样逐次审批。运行时的独立网络批准也必须纳入或限制，不能自动点掉第二套提示。

## 6. 数据、Skill、任务与实现组织

建议 SQLite 使用本地盘、WAL、事务与 schema 迁移，任务状态/授权变更/事件索引在一致事务内提交；并发写入有界，避免把数据库文件放共享网络盘。成果数据在独立本地目录，以文件清单、版本摘要和来源关联。密钥不进入 SQLite。首版不用 Redis、外部消息队列、Kubernetes 或分布式工作流服务。

父子任务、幂等创建、消息队列、额度预留与撤权先用 Go 领域服务实现；若已有可复用 Go 模块能满足接口，可在源码审查后引入，不将“未内置业务目录”判为基础库缺陷。没有 checkpoint 的 Harness 依旧只重跑任务，不伪造内部上下文恢复。

Skill 按各 Harness 适配目录和调用方式注入，不依赖 `sbx` 共享库；自定义 Linux 模板预装所需 CLI/依赖，运行时尽量不临时开放包管理网络。Skill 测试覆盖加载、相对引用、脚本、MCP 和越权拒绝。模板以版本和摘要固定，不读取工作区中的任意宿主启动钩子作为可信配置。

建议一个仓库内保持清晰目录即可：`frontend/` 放 UI，`internal/` 放 Go 业务与适配器，`templates/` 放隔离模板，`tests/` 放按现有方案组织的测试，`docs/` 放设计。本轮已创建 `tests/sandbox/` 合成探针与 `validation/2026-10-23/` 精选证据；应用实现目录尚未创建。

### 两个需要在原型中明确的能力上限

- **只读审查**：`sbx` guest 有 sudo，不能靠 guest 内 `chmod` 证明 Agent 无法改自己的输入。宿主权威成果保持不可变，每次审查记录输入摘要；若要求强制阻止 guest 修改输入，必须另有运行时级只读机制或收紧 guest 能力，没验证前只称“基于固定快照审查”。
- **成果传播限制**：Agent 已经读到的内容可以改写进入模型请求，标签/正则过滤不保证防泄漏。对于“不允许发往某服务”的成果，应拒绝交给能调用该服务的环境，或在交付前整体收紧其出口并检查历史状态；已进入外部模型的上下文不能撤回。不能在选型中承诺任意文本的完美信息流追踪。

## 7. 从建议到正式选型的四个验证步骤

每一步都记录版本与实际输出。T2 的指定路径、并发、网络、SSH Agent、测试服务停止与清理已实测；T3 仅完成合成 HTTP 网关正反对照、分环境端口约束和虚拟令牌撤销。T1 桌面、T3 真实模型/流式/MCP/凭据、T4 产品协作仍未执行；详细结果见 07。

| 步骤 | 验证内容 | 通过后能锁定什么 |
|---|---|---|
| T1 桌面构建 | 在选定 Go/Wails/Node 组合构建 release；目录选择、事件推送、长输出终端、输入与 Ctrl+C、拒绝不可信导航 | 桌面壳、前端、Go 与终端路径 |
| T2 隔离 | 授权安装/启用后，启动两套无宿主挂载环境；测试跨环境、宿主文件、SSH、Skill、网络、额度与清理 | `sbx` 是否真正适合作为首版底座 |
| T3 能力通路 | 指定端口访问 Dock；模拟上游凭据与模型流式请求；MCP 调用；跨环境重放拒绝；停止后旧凭证失效 | 网关、身份、凭据和协议方案 |
| T4 协作与恢复 | 一主一子创建、固定快照交接、重复请求、Controller 崩溃、孤儿环境处理；不改原目录/全局配置 | 本地任务调度与成果方案 |

建议将 T2/T3 作为实施第一天的首要验证，不用等漂亮界面。若 `sbx` 安装、登录、策略隔离、身份绑定或终端集成无法通过，继续保留 RuntimeBackend 接口，明确报告阻塞，再评估自管 VM/其他方案；不自动降级为裸宿主执行，也不把普通容器重新命名成已满足的 microVM。

## 8. 证据与来源

资料核查日期：2026-10-23。以下是本轮实际读取的官方资料；主要依据供应商/项目自身文档，未完成独立攻击测试或源码安全审计。动态文档可能变化，实施时需按固定版本复核。

- [S1：Wails v2.17.0 go.mod](https://proxy.golang.org/github.com/wailsapp/wails/v2/@v/v2.17.0.mod)
- [S2：MCP Go SDK v1.8.0 go.mod](https://proxy.golang.org/github.com/modelcontextprotocol/go-sdk/@v/v1.8.0.mod)
- [S3：modernc.org/sqlite v1.61.0 go.mod](https://proxy.golang.org/modernc.org/sqlite/@v/v1.61.0.mod)
- [S4：Go 官方下载版本](https://go.dev/dl/?mode=json)
- [S5：Wails 官方 README](https://raw.githubusercontent.com/wailsapp/wails/master/README.md)
- [S6：sbx 安装与平台前提](https://docs.docker.com/ai/sandboxes/install.md)
- [S7：sbx 架构](https://docs.docker.com/ai/sandboxes/architecture.md)
- [S8：sbx 隔离边界](https://docs.docker.com/ai/sandboxes/security/isolation.md)
- [S9：Microsoft Windows Sandbox](https://learn.microsoft.com/windows/security/application-security/application-isolation/windows-sandbox/)（本轮搜索返回正文；未单独打开页面）
- [S10：Codex 安全与网络控制](https://developers.openai.com/codex/sandbox)（本轮搜索返回正文；未单独打开页面）
- [S11：sbx 默认行为](https://docs.docker.com/ai/sandboxes/security/defaults.md)
- [S12：sbx 本地策略与继承](https://docs.docker.com/ai/sandboxes/governance/access-controls/local.md)
- [S13：sbx 登录、费用与统计 FAQ](https://docs.docker.com/ai/sandboxes/faq.md)
- [S14：sbx 宿主服务连接](https://docs.docker.com/ai/sandboxes/workflows/development.md)
- [S15：官方 MCP Go SDK 传输说明](https://raw.githubusercontent.com/modelcontextprotocol/go-sdk/v1.8.0/docs/protocol.md)（独立检查任务读取）

本轮另执行了 Go 实际编译、双 microVM 合成探针与生命周期测试，结果见 07。尚未构建完整 Dock，也没有证实主流 Agent/MCP/Skill 已兼容。
