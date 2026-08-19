# Roadmap

## V0.1 Agent Core — 已完成

- [x] Go HTTP 服务与内嵌 Web UI
- [x] Eino ChatModelAgent / ReAct Loop
- [x] OpenAI-compatible 模型与本地 Mock 模型
- [x] SSE 流式对话与取消
- [x] SQLite 会话、消息和 Run 持久化
- [x] 三个只读工具
- [x] Agent Run 审计事件
- [x] 自动化测试与 Docker 构建文件
- [x] 项目分析、技术设计、README 和文档维护约定

## V0.2 Knowledge Base — 真实语义基线已完成，融合排序待优化

- [x] PostgreSQL + pgvector Store（完整会话/Run/知识库持久化）
- [x] 文档上传和 SHA-256 内容去重
- [x] 文档版本控制
- [x] TXT、Markdown 基础解析
- [x] PDF 基础解析（文本层，不含 OCR）
- [x] Unicode 边界感知的重叠分块
- [x] 递归字符切块策略（Markdown 标题 → 段落 → 换行 → 句末 → 空格 → 硬切）
- [x] Embedding Provider 抽象（本地 Hash + OpenAI-compatible）
- [x] SQLite 精确向量扫描 + BM25（最多 10,000 chunks）
- [x] pgvector HNSW 向量检索 + PostgreSQL FTS/GIN 候选召回
- [x] RRF 混合召回
- [x] 结构化引用坐标与 Agent Tool 证据查看
- [x] 文档级权限过滤（服务端可信主体 + private/public）
- [x] 固定检索评测集：Recall@K、MRR、命中率和单路/混合对比
- [x] 确定性答案事实覆盖、有效引用覆盖与引用忠实度评估
- [x] 10 份文档、64 题的真实 Embedding 领域检索集和纯检索评测入口
- [x] 使用 `text-embedding-v4` 记录 vector/keyword/hybrid 的 64 题真实指标
- [ ] 输出失败 Case 明细，按语义、关键词、分块和多文档问题归因并调整 Hybrid 融合策略

验收条件：每个知识库答案能够定位到原文；能用固定数据证明混合召回优于单一路径。

当前验证结果：上传 → 文本/PDF 解析 → 递归字符分块 → Embedding → 向量/关键词 → RRF → `knowledge_search` → 对话 SSE 的纵向链路已打通。相同主体和文档名形成单调版本链，默认列表与检索只使用最新版，删除最新版会恢复上一版；服务端可信主体控制 owner，private 仅 owner 可见、public 可跨主体读取，非 owner 不得删除。SQLite 旧表可迁移，PostgreSQL 在版本组上使用事务级 advisory lock。自动化测试覆盖版本回退、旧版检索隔离、ACL、PDF 文本层解析、递归边界和 HTTP 元数据。小型答案集用于事实与引用冒烟；`starship-domain-retrieval-v1` 使用 10 份文档、64 题评估真实向量。2026-08-18 的 `text-embedding-v4` 基线中，Vector Recall@3/MRR 为 0.911458/0.828125，Keyword 为 0.963542/0.945313，Hybrid 为 0.934896/0.914063，阶段阈值通过。Hybrid 相对 Vector 的 Recall 与 MRR 分别提升 0.023438 和 0.085938，但仍低于 Keyword 0.028646 和 0.031250；因此真实语义链路已验收，融合排序优势尚未成立，需结合失败 Case 明细继续调权和归因。

## V0.3 Long-term Memory — 主链路已完成

- [x] 短期历史压缩与增量摘要（最近消息窗口、双存储、安全注入和审计）
- [x] Semantic / Episodic Memory Schema（来源、重要性、可选过期时间）
- [x] 记忆候选提取和 Consolidation（真实模型结构化提取 + 本地确定性规则）
- [x] Memory Key 去重、冲突更新、人工修正保护和过期过滤
- [x] 相关性 + 时效性 + 重要性召回与安全上下文注入
- [x] 用户创建、查看、编辑、删除记忆（REST API + Web 面板）
- [x] 有/无记忆 A/B 评估（完整 Chat 链路、硬负例、事实覆盖与污染门禁）

验收条件：长期记忆不是历史消息向量库；每条记忆可解释来源并可由用户控制。

当前验证结果：已建立独立 `memories` 表和 `memory.Service`，区分 semantic/episodic，保存稳定 `memory_key`、来源会话/消息、重要性、人工修正标记和可选过期时间；SQLite 与 PostgreSQL 保持相同 Store 契约。回答成功后，真实模型使用抗指令注入的中文结构化 Prompt 提取候选，本地 Mock 使用保守规则；Service 按 Kind + Memory Key 创建、跳过重复或更新冲突，并拒绝覆盖人工修正。新请求执行前以词项相关性 65% + 重要性 20% + 90 天半衰期时效性 15% 联合排序，非总览问题还需达到 0.20 最低主题相关性；过门槛的 Top-K 记忆以不可信 JSON 数据注入独立 System Message，总正文上限 6,000 字符。长对话按实际未摘要消息数触发增量摘要，`conversation_summaries` 保存覆盖序号，最近窗口继续保留原文；原始消息不会删除。`make eval-memory` 在隔离数据库中让同一问题通过 Control/Treatment 完整 Chat 链路，并从 RunEvent 核对实际注入 ID。默认 5 题基线达到预期召回率 1、错误召回率 0、Treatment 事实覆盖率 1、Control 事实覆盖率 0、覆盖增益 1、答案污染率 0。基线曾发现“Go 并发模型”被个人语言记忆污染，新增最低主题相关性后通过，证明门禁能够驱动实现修正。V0.3 主链路已完成。

## V0.4 Multi-Agent — 已完成

- [x] Supervisor + Research / Document / Writer Agents
- [x] 子 Agent 上下文隔离和结构化交接
- [x] 专家工具权限隔离、协作 SSE/RunEvent 与 Web Trace
- [x] 固定路由评测：准确率、意外专家调用、答案完成率和硬负例
- [x] 并行任务、预算、超时、重试和取消
- [x] 父子 Run 链路与可视化
- [x] Human-in-the-loop 审批节点
- [x] 单 Agent / 多 Agent 的质量、成本和耗时对比

验收条件：至少一个基准任务能证明多 Agent 带来可量化收益，否则保持单 Agent。

当前验证结果：`ZORA_MULTI_AGENT_ENABLED` 默认关闭，显式开启后由 `zora_supervisor` 通过 Eino AgentTool 调用研究、文档和写作专家。研究专家仅持有时间、计算器和项目状态工具，文档专家持有 `knowledge_search`（V0.5 启用时再追加 MCP 文件只读工具），写作专家无底层工具；AgentTool 默认只传递 Supervisor 构造的 `request`，不共享主会话完整历史。Runtime 对每个根 Run 注入独立的交接次数、并行度、专家超时和重试预算，Context 取消继续下传；独立子任务由 Eino ToolNode 并行执行，证据依赖任务保持串行。每次交接同步创建 `agent_task_runs` 子 Run，SSE/Web Trace 暴露 `child_run_id`。`risky/all/off` 审批策略把高影响请求持久化为 `approval_requests`，Web 可批准或拒绝，批准后恢复原 SSE，拒绝/超时进入明确终态。`make eval-agents` 使用隔离 SQLite 和完整 Chat/RunEvent 链路，默认 7 题路由准确率 1、意外专家调用率 0、答案完成率 1；同题单 Agent Control 质量 0.785714，多 Agent Treatment 质量 1，质量增益 0.214286，调用次数代理比 2，延迟比例随环境输出并受宽松上限门禁。该结论只适用于确定性 Mock 小样本，真实 Provider 仍需扩充业务集和 Token Usage。V0.4 主链路已完成。

## V0.5 Office Agent — 上线准备代码已完成，真实租户验收延期

- [x] 官方 MCP Go SDK
- [x] 文件只读连接器
- [x] 邮件只读连接器
- [x] 日历只读连接器
- [x] 草稿预览
- [x] 写操作人工确认
- [x] 幂等执行任务、租约与失败恢复
- [x] Microsoft Graph 邮件/日程写执行器（默认关闭）
- [x] 邮件远端草稿检查点、失败重试与已发送恢复
- [x] 日程固定 transactionId 幂等创建
- [x] Microsoft client credentials OAuth、令牌缓存/提前刷新与 401 单次重试
- [x] Secret/Token 文件、reader/writer 双身份、写工具双门禁和最小权限 Runbook
- [ ] 真实 Microsoft 租户邮件/日历读写与范围外拒绝验收（外部资源延期，不阻塞 V0.6）

当前验证结果：已固定官方 `github.com/modelcontextprotocol/go-sdk v1.7.0`，Zora 通过 stdio 启动 MCP 子进程、完成协议握手和分页工具发现，再把 JSON Schema 转为 Eino Tool。只有同时进入本地 `allowed_tools` 且声明 `readOnlyHint` 的工具会被注册，公开名称增加 `mcp_{server}_` 前缀；每次调用受独立超时与 12,000 字符默认输出上限约束，工具调用/结果沿用 RunEvent 审计。内置 `zora-mcp-files` 仅支持文件列表和 UTF-8 文本读取，授权根目录在子进程内强制校验；`zora-mcp-microsoft` 通过 Graph 提供邮件搜索/详情和日历窗口查询/详情四个只读工具，只返回元数据与正文摘要。Graph Token 只透传给独立子进程，外部内容带不可信数据提示。

第三阶段新增 `office_drafts` 业务对象与 `preview_email_draft`、`preview_calendar_draft` 两个内部工具。草稿保存规范化 JSON、内容 SHA-256、Conversation/Run 来源；`UNIQUE(source_run_id, content_hash)` 保证 Agent 重试不重复创建。SQLite/PostgreSQL、REST 查询/删除、Web 草稿箱和单/多 Agent Writer 路由均已接通。

第四阶段新增 Office 专用确认状态机：`draft → pending_confirmation → approved/rejected`。数据库使用带预期状态的 compare-and-swap 更新，状态与 `office_draft_events` 不可变审计记录在同一事务提交，因此并发或重复决定只有一个能够成功。REST/Web 支持提交确认、批准、拒绝和查看迁移记录；approved 明确为“已批准、未执行”。

第五阶段新增独立 `office_operations` 和 `office_operation_events`。每份 approved 草稿最多一个 Operation，`draft_id` 与 SHA-256 幂等键均唯一；执行按 `pending/failed → executing → completed/failed` 迁移，领取时写入租约、执行器名称和 attempt。Operation、草稿状态及两类审计事件在同一 SQLite/PostgreSQL 事务提交；失败重试保持原幂等键，进程启动会回收过期 executing 租约。Executor 接口强制声明 `IdempotencySafe`，且只有返回 `external_effect=true` 和非空远端引用才能完成，默认无执行器时 API 返回 503、任务保持 pending。REST/Web 已支持准备任务、状态/审计查看和条件执行；单元测试覆盖安全门禁、失败重试、稳定幂等键和租约恢复，SQLite 测试覆盖并发准备、原子完成与重启持久化。

第六阶段在 Executor 权限边界后接入专用 Microsoft Graph MCP 写会话。写工具不适配为 Eino Tool，模型仍只能生成 Zora 内部草稿。邮件先通过 `POST /messages` 创建远端草稿并取得不可变 ID，Operation 在发送前原子保存检查点与审计事件；失败重试复用该 ID，并用 `isDraft` 区分“仍待发送”和“远端已发送、本地未落完成状态”。日程把稳定幂等键派生为固定 UUID `transactionId` 后调用 `POST /events`。Graph 错误映射为中文且会隐藏访问令牌，Token 仍只进入最小环境的子进程。自动化测试覆盖请求方法/路径/载荷、Bearer 头、写工具安全声明、检查点先于发送、发送失败重试不重复创建草稿，以及稳定日程事务 ID。

第七阶段新增 client credentials `/.default` TokenSource。client secret 从单行 Secret 文件读取，access token 在 Microsoft 子进程内缓存并最多提前 2 分钟刷新；Graph 401 会失效缓存并只重试一次，OAuth/Graph 错误均脱敏。普通 Agent reader 与审批后 writer 改用两套环境前缀、两套 Entra 应用和两份 Secret；reader 默认只注册四个只读工具，writer 必须同时设置 `ZORA_OFFICE_EXECUTOR=microsoft_graph` 与 `ZORA_OFFICE_MICROSOFT_WRITE_ENABLED=true`。部署 Runbook 给出 Exchange Application RBAC 的 mailbox scope、reader/writer 最小角色和正负验收清单。

本阶段仍没有用 Mock 冒充真实租户成功：当前环境没有 Microsoft 测试租户、专用邮箱和 Entra 管理权限，因此在线发送/建会及范围外拒绝尚未执行。拿到这些资源后只剩真实租户验收，不再缺应用代码主链路。

## V0.6 Agent Reliability & Observability — 已完成

- [x] 每次 Assistant 模型调用形成 `model_call_completed` RunEvent
- [x] 采集 Provider 真实 Prompt/Completion/Total/Cached/Reasoning Token Usage
- [x] 未上报或部分上报 Usage 的完整性标记，不使用字符数伪造 Token
- [x] 首字延迟、工具/Agent 交接耗时与整轮耗时
- [x] SQLite/PostgreSQL 最近 Run 查询与单 Run 查询
- [x] `GET /api/runs`、`GET /api/runs/{id}/metrics` 聚合接口
- [x] `done.metrics` 实时结果和 Web 最近 50 次运行监控
- [x] 聚合单元测试、Runtime Usage 测试、HTTP/SSE 纵向验收
- [x] 多模型安全配置、请求级选择、Web 切换和实际模型 Run 审计

验收条件：指标必须来自真实 Runtime 与持久化 RunEvent；Provider 不返回 Usage 时不得估算成“真实 Token”；同一 Run 的实时 `done.metrics`、聚合 API 和事件明细可以互相核对。

当前验证结果：Runtime 在每条 Assistant 输出合并完成后记录模型调用，兼容工具调用轮次、最终回答和专业 Agent 输出。真实 Provider 返回的 `ResponseMeta.Usage` 被转换为与 SDK 解耦的结构，RunEvent 保存调用级 Token 与 `usage_reported`；聚合器统计 Usage 完整性。Chat 在第一个用户可见 `delta` 时记录 `first_token`，并用 ToolCall ID 分别关联普通工具和 Agent 交接的开始、完成与耗时。最近 Run API 从 SQLite/PostgreSQL 的 `agent_runs` 读取终态，再使用追加式事件重建指标；Web 监控按需读取最近 50 次。Mock 端到端实测为 2 次模型调用、1 次工具调用、Usage 未上报，页面/API 会如实显示“Provider 未上报”。浏览器自动化环境无法连接本机回环地址，因此本轮完成了真实 HTTP/SSE、静态资源、JavaScript 语法和 API 测试，但不把截图级视觉检查描述为已通过。

## V0.7 OpenTelemetry & Prometheus — 已完成

- [x] OTLP/HTTP Trace Exporter、W3C TraceContext/Baggage 与 ParentBased 比例采样
- [x] HTTP Server Span 与路由、状态码、耗时指标
- [x] 持久化 Run 与 `agent.run` Span 关联，SSE/RunEvent 返回 Trace ID
- [x] 模型 Generate/Stream Span、耗时与 Provider 真实 Token 指标
- [x] 内置、知识库、办公、MCP 与专业 Agent Tool 的统一 Span/指标包装
- [x] Embedding 写入/查询 Span、输入数量与耗时指标
- [x] 独立 Prometheus Registry 和 `GET /metrics`，自抓取不进入业务 HTTP 指标
- [x] Jaeger/Prometheus Compose、Makefile 命令和阶段使用文档
- [x] Trace 父子关系与 Prometheus 导出的自动化测试

验收条件：同一次知识库 Agent 请求中，HTTP、Run、模型、知识库工具和 Embedding 必须拥有相同 Trace ID，且父子关系正确；指标不得把 Prompt、回答、文档或 Tool 参数正文作为 Attribute/Label。

当前验证结果：自动化测试构造完整调用链并验证 `HTTP → Run → model/tool → embedding` 的父子 Span 与同一 Trace ID，Prometheus Handler 可导出 Run Counter 和延迟 Histogram。业务 Run 的 `trace_id`、`span_id` 同时进入 SSE `start` 和持久化 `run_started`，可从产品审计跳转到 Jaeger。当前是本地直接导出到 Jaeger 的开发方案，生产仍需 Collector、Grafana Dashboard 与告警规则。

## V0.8 Memory Capture Outbox & Worker — 已完成

- [x] assistant Message 与 Capture Job 同事务提交，失败整体回滚
- [x] `run_id` 幂等入队，Outbox 只保存消息 ID、不复制正文
- [x] SQLite/PostgreSQL 的 pending/executing/completed/failed 状态机
- [x] PostgreSQL `FOR UPDATE SKIP LOCKED`、租约所有者和过期恢复
- [x] 单任务超时、指数退避、最大尝试次数和终态失败
- [x] SSE `done.memory_job` 与 Job 列表/详情 REST API
- [x] queued/retry/completed/failed RunEvent 审计
- [x] `memory.capture` Consumer Span、队列延迟/执行耗时/状态指标
- [x] 事务原子性、幂等、重试、租约恢复、HTTP 和 Trace 自动化测试

验收条件：HTTP 返回 `done` 前必须已经持久化助手消息和 pending Job；临时失败可在退避后完成；进程退出遗留租约不会永久卡住；异步 Span 可通过持久化 TraceContext 与原 Run 关联；任何 Trace/Metric 不包含对话正文。

当前验证结果：SQLite 自动化测试覆盖消息与 Job 原子提交及回滚、同 Run 重复入队、available_at、租约和最后尝试恢复；Worker 测试注入一次失败后在第二次完成；HTTP 测试验证 SSE Job 和状态查询；OTel 测试验证后台 `memory.capture` 的父 Span 是原 `agent.run`。PostgreSQL Schema 和领取 SQL 已进入自动化检查，真实 PostgreSQL 并发集成仍应在 Docker 可用环境执行 `make test-postgres`。

## V0.9–V0.11 部署准备能力 — 已完成

- [x] 消息、长期记忆与知识库使用三套物理隔离向量索引，记录模型、维度和索引版本
- [x] 文档摄取与会话摘要迁入可恢复后台任务，按 kind 使用独立 Worker
- [x] 后台任务租约、指数退避、人工重投、Payload 清理和异步 Trace/Metric
- [x] 可信客户端 IP 令牌桶、SQLite/PostgreSQL UTC 每日持久化配额
- [x] 双提交 CSRF、精确 CORS Origin 白名单与可信代理 CIDR
- [x] 模型/Embedding Key 和 PostgreSQL DSN 的 `_FILE` Secret 注入与权限校验

验收条件：三类向量不能混表召回；HTTP 不等待真实 Embedding 摄取和摘要完成；重启后任务与日配额状态不丢；伪造代理 Header、非法 Origin、缺失 CSRF 和超额请求必须被明确拒绝；Secret 不得写入配置 JSON、日志或 API 响应。

V0.11 后停止继续扩展非必要功能，进入部署、真实环境负向验证和故障复盘。未完成事项仅保留多用户认证/租户隔离、多副本全局限流、托管 Secret 自动轮换、独立消息/记忆召回评测，以及 Microsoft 365 外部资源验收。

## 后续技术待办

- [ ] 为不同 Job 并发更新同一 `kind + memory_key` 增加数据库唯一约束、冲突重读与合并重试。
- [x] 文档摄取改为持久化 `knowledge_ingestion` Job + 独立 Worker，并提供状态查询与 failed 人工重投。
- [x] 会话摘要迁入 `conversation_summary` Job；按 Run 幂等，保持 sequence 边界和失败隔离。
- [x] 普通对话消息向量化与跨会话用户原话语义召回。
- [x] 长期记忆向量化，并与词项相关性、重要性和时效性联合召回。
- [x] 使用 `message_embeddings`、`memory_embeddings`、`knowledge_chunks` 物理隔离三类向量，并记录模型、维度与索引版本。
- [ ] 为消息和长期记忆向量召回补独立离线评测，持续观察上下文污染与额外 Embedding 成本。

## 每个版本的文档完成标准

- [x] README 的能力矩阵、配置和使用方法与代码一致
- [x] 项目分析文档同步业务模型、数据模型、亮点和风险
- [x] 项目技术文档同步流程、API、事件与设计方案
- [x] 新增配置写入 `.env.example`
- [x] 新增或修改 API 时提供请求、响应和错误示例
- [x] 规划能力与已实现能力明确区分
- [x] 验收指标和实际验证结果写回对应里程碑
