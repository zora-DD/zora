# Zora 架构说明

> 本文是便于快速阅读的架构摘要。完整业务分析见 [项目分析文档](project-analysis.md)，完整流程、接口和设计细节见 [项目技术文档](technical-design.md)。

## 1. 边界

Zora 将系统划分为十六个边界：

1. `httpapi`：HTTP、JSON、SSE 和静态界面，不包含 Agent 规则。
2. `chat`：用例编排、事务顺序、并发保护和执行审计。
3. `agentruntime`：Eino ADK 适配，输出与传输协议无关的事件。
4. `agenttools`：工具 Schema、输入校验和执行代码。
5. `knowledge`：文档摄取、Embedding、混合检索、引用和 `knowledge_search` Tool。
6. `memory`：Semantic/Episodic Memory、候选提取、Consolidation、Capture Outbox/Worker、生命周期校验和用户控制。
7. `summary`：会话增量摘要、最近消息窗口、Model/Rule Summarizer 和持久化契约。
8. `memoryeval`：长期记忆 Control/Treatment、召回/事实/污染指标和质量门禁。
9. `agentseval`：多 Agent 路由准确率、意外专家调用、答案完成和质量门禁。
10. `store`：对话、知识库、长期记忆、Capture Job 与摘要的持久化边界，由 SQLite 或 PostgreSQL 实现。
11. `rageval`：固定数据集校验、检索指标、答案引用/忠实度和联合门禁。
12. `mcpbridge`：官方 MCP Client、stdio 生命周期、工具发现/白名单和 Eino 适配。
13. `mcpfiles`：独立文件连接器的授权目录、路径校验和只读工具实现。
14. `mcpmicrosoft`：独立 Microsoft Graph 连接器的 Token 边界、邮件/日历只读查询和外部内容安全标记。
15. `office`：邮件/日程草稿模型、结构化校验、可信 Run 来源、重试幂等、人工确认状态机和内部预览工具。
16. `observability`：从 AgentRun 与 append-only RunEvent 派生 TTFT、真实 Usage、工具/交接耗时等统一运行指标。

依赖方向始终从传输层指向应用层和抽象层，Eino 类型不会进入 HTTP API 的公开数据模型。

## 2. 为什么选择 Eino，而不是完全手写循环

V0.1 直接使用 `ChatModelAgent + Runner`，以获得：

- 标准工具 Schema 与调用循环
- 模型/工具事件
- 最大迭代次数
- 流式输出
- 后续 Middleware、Checkpoint 和 Multi-Agent 升级路径

项目保留自己的 `agentruntime.Event`，避免上层被某个 Eino 版本的数据结构锁死。Eino 当前固定在稳定版本，升级必须先通过现有集成测试。

启动时可以从 `ZORA_MODELS_JSON` 为每个模型配置装配独立 Runtime，Chat 以安全 `model_id` 做请求级路由；未配置时仍保留单 Runtime。API 只公开模型展示元数据，密钥由各配置的 `api_key_env` 从服务端环境读取。自动记忆提取和摘要使用默认模型，避免后台增强链路随页面选择漂移并产生不可预测成本。

## 3. 数据模型

### Conversation / Message

只保存用户可见历史。工具中间消息放在 Run Event 中，防止主对话无限膨胀；下一轮模型获得最终回答而不是完整内部轨迹。

### AgentRun

每个用户请求对应一个 Run，状态只能落在：

- `running`
- `completed`
- `failed`
- `cancelled`

即使浏览器断开，服务也会使用短时独立 Context 将 Run 落为 `cancelled`，避免永久停留在 `running`。

### RunEvent

采用 append-only 审计：`run_started`、`tool_call`、`tool_result`、`agent_handoff_started`、`agent_output`、`agent_handoff_completed`、`model_call_completed`、`first_token`、`model_output`、记忆提取/召回事件、会话摘要加载/更新/失败事件、`run_completed/failed/cancelled`。流式 token 只发往客户端，不逐 token 落库；每次完整模型回复只写一次调用完成事件。记忆和摘要正文也不会复制进事件。专业 Agent 的交付物会进入 `agent_output`，用于核对协作事实，但不会成为下一轮主会话历史。

### RunMetrics

RunMetrics 不是独立事实表，而是 `AgentRun + RunEvent` 的查询时投影。它统一计算总耗时、首字延迟、模型调用与 Usage 完整度、真实 Token、工具调用/耗时和 Agent 交接/耗时。SSE 完成事件、REST API 和 Web 运行监控都复用同一个聚合器；Provider 未返回 Usage 时明确标记不完整，不进行字符数估算。

### Memory

Memory 独立于原始 Message，区分 `semantic` 稳定事实/偏好与 `episodic` 经历/事件。每条记录包含稳定 Key、来源、重要性、人工修正标记、创建/更新时间和可选过期时间。当前支持用户完整 CRUD、回答后的自动提取/Consolidation，以及回答前的联合召回和安全上下文注入。

### MemoryCaptureJob

回答完成时，assistant Message 与 pending CaptureJob 在同一事务提交；Job 只保存 Run/会话/消息 ID、状态、attempt、available_at、租约和计数结果。后台 Worker 按租约领取、超时控制、指数退避和过期恢复执行 Capture，SSE/REST/RunEvent/OTel 可追踪后续状态。PostgreSQL 使用 `FOR UPDATE SKIP LOCKED` 避免同一任务被多个实例领取。

### ConversationSummary

每个 Conversation 最多一份增量摘要，记录正文、已覆盖的 Message sequence、累计消息数、模型和更新时间。达到阈值时合并旧摘要与较早消息，最近窗口继续保留原文；原始 Message 不删除。下一轮上下文由“摘要 + 相关长期记忆 + 最近原始消息”组成。

### OfficeDraft

OfficeDraft 是邮件或日程参数快照。它保存类型、状态、标题、规范化 JSON Payload、内容哈希、来源 Conversation/Run 和时间戳。Service 支持 `draft → pending_confirmation → approved/rejected`，只允许删除 `draft`；执行时由独立 Operation 原子推进为 `executing → completed/failed`。每次迁移追加 OfficeDraftEvent，模型不能直接修改状态。

### OfficeOperation

OfficeOperation 是 approved 草稿对应的唯一持久化执行任务，保存稳定幂等键、执行器名称、attempt、租约、错误和远端引用。任务按 `pending/failed → executing → completed/failed` 迁移，并追加带 attempt 的 OfficeOperationEvent。默认不配置 Executor；只有声明支持幂等重放且返回可核验远端引用的实现才能完成任务。

## 4. 并发与取消

- 同一 Conversation 同时只允许一个 Run 修改历史，避免两个请求读取相同旧上下文后交错落库。
- 不同 Conversation 可以并发运行。
- 浏览器 Abort、HTTP Context 取消和服务端 Deadline 会传入 Eino 与模型请求。
- 模型客户端和整个消息请求都有超时。
- 草稿状态迁移使用数据库 compare-and-swap；状态与审计事件在同一事务提交，重复或并发人工决定只有一个成功。
- Operation 使用数据库唯一约束和租约防止重复创建/领取；任务、草稿和双审计原子迁移，启动时回收过期执行租约。
- Memory Capture 使用消息 + Job 事务 Outbox、Run 唯一幂等键和数据库租约；Worker 停止时取消当前任务并将失败持久化为可重试或终态。

PostgreSQL Store 已对 schema migration 使用 advisory transaction lock；业务对话锁仍是进程内 Mutex，多实例部署前还应升级为数据库 advisory lock 或带租约的分布式锁。

## 5. 安全基线

- 工具采用显式 allowlist。
- 计算器使用递归下降解析器，不执行表达式代码，也不调用 Shell。
- 请求体限制为 1 MiB，消息限制为 20,000 字符。
- Web UI 对模型输出做 HTML 转义。
- 默认 Content Security Policy 只允许同源资源。
- API Key 只从环境变量读取。
- 外部写能力不注册为 Agent Tool；只有显式启用的审批后 OfficeExecutor 可以执行固定 Microsoft Graph 写协议。
- 草稿工具只能从 Chat 注入的可信 Conversation/Run Context 取得来源，并固定返回 `external_effect=false`。
- 草稿提交、批准和拒绝只能通过独立 REST/Web 操作；批准状态也不触发外部写入，避免把“确认”误当成“执行”。
- 知识文档限制为 UTF-8 TXT/Markdown 且最大 5 MiB，文档删除需要用户确认。
- 长期记忆内容最多 2,000 字符，类型/重要性/过期时间在 Service 层校验，来源字段不可由用户伪造，删除需要确认；候选提取 Prompt 隔离不可信聊天数据，Service 二次拒绝明显敏感凭据。
- 会话摘要 Prompt 把旧摘要和消息编码为不可信 JSON，禁止保留密码或 Token；加载时仍按非指令背景数据注入，摘要失败自动退化为最近原始消息。

## 6. V0.2 RAG 当前架构

RAG 没有塞进 HTTP Handler，而是以独立应用层和 Store 边界实现：

```text
internal/knowledge/
├── types.go         Document/Chunk/Store 契约
├── chunker.go       Unicode 边界感知分块
├── tokenizer.go     中文单/双字与西文词项
├── embedder.go      Hash/OpenAI-compatible Embedding
├── service.go       摄取、BM25/向量、RRF 和引用
└── tool.go          knowledge_search

internal/store/sqlite/
└── sqlite.go        Document/Chunk 事务存储

internal/store/postgres/
├── postgres.go      pgxpool、pgvector 注册、迁移锁和维度校验
├── conversations.go Conversation/Message/Run/Event
├── knowledge.go     Document/Chunk、HNSW/FTS 候选召回
└── schema.go        PostgreSQL DDL 与索引

internal/rageval/
├── evaluator.go     Recall@K、MRR、命中率和模式对比
└── answer.go        事实覆盖、有效引用覆盖和引用忠实度

evals/
└── knowledge.json   固定语料、问题、事实/证据锚点与阈值

SQLite 候选召回在 Go 内最多精确扫描 10,000 个 Chunk。PostgreSQL 使用 `CandidateStore` 将 pgvector HNSW 和 `tsvector`/GIN 两路 Top 50 候选下推数据库，再由 `knowledge.Service` 统一执行 RRF。两个后端保持相同的 Service 和 Tool 契约。
```

检索作为 Eino Tool 或 Graph 暴露给 Agent，但召回、权限过滤和评估属于业务层。

线上 `knowledge_search` 固定使用 hybrid；离线评测通过 `SearchWithMode` 分别执行 vector、keyword 和 hybrid，再通过真实 Eino Runtime 生成答案并核对事实、有效引用及原文支持。`cmd/zora-eval` 每次在临时 SQLite 中重建固定语料，因此不会被在线历史数据污染。PostgreSQL 集成测试通过 `ZORA_TEST_POSTGRES_DSN` 显式启用。

## 7. V0.3 长期记忆当前架构与接入点

当前已实现：

```text
internal/memory/
├── types.go          Memory/CaptureJob/Store 契约
├── capture_queue.go  助手消息 + Job 事务入队与状态查询
├── capture_worker.go 租约领取、退避重试和恢复
├── extractor.go      真实模型结构化提取与本地保守规则
├── retriever.go      相关性、重要性、时效性联合评分
└── service.go        CRUD、候选校验、Key Consolidation 和人工修正保护

internal/store/sqlite/memories.go
internal/store/postgres/memories.go
internal/memoryeval/
internal/summary/
internal/store/sqlite/summaries.go
internal/store/postgres/summaries.go

GET/POST /api/memories
GET/PUT/DELETE /api/memories/{memoryID}
GET /api/memory-capture/jobs[/{jobID}]
GET /api/conversations/{conversationID}/summary
```

SQLite 与 PostgreSQL 都保存 kind、memory_key、content、importance、user_edited、source、created/updated/expires_at。默认查询排除过期项；Web 管理面板显式展示全部记录，确保用户仍能清理已过期记忆。

长期记忆与消息向量使用不同物理表，并按两条失败隔离链路运行：

```text
对话结束 → 消息 + Job 原子入队 → Worker 租约领取 → 候选提取 → 校验 → Key 合并 → 持久化
新请求   → 词项/向量相关性 → 相关性 65% + 重要性 20% + 时效性 15% → Top-K → 安全 System 上下文
```

自动提取的 Memory 关联来源用户消息，并按 Kind + Memory Key 跳过重复或更新冲突；用户手动修正会阻止后续自动覆盖。召回默认排除过期/无关记忆，正文按不可信 JSON 数据注入，最多 6,000 字符；RunEvent 只记录 ID 和可解释分数。

长对话按当前会话实际未摘要消息数量触发，保留最近窗口，把较早消息增量写入 `conversation_summaries`。摘要读取/生成失败只记录审计并继续回答。

消息、长期记忆和知识库分别写入 `message_embeddings`、`memory_embeddings` 与 `knowledge_chunks`。前两者额外保存索引版本；模型或维度不匹配的记录不会参与查询。跨会话消息召回排除当前会话且只注入用户原话，助手消息虽然建索引但默认不回灌模型。`POST /api/semantic/reindex` 可从原始业务表幂等重建派生索引。

## 8. V0.4 多 Agent 架构

当前多 Agent 由 `ZORA_MULTI_AGENT_ENABLED=true` 显式开启：

```text
Supervisor
├── Research Agent  → current_time / calculator / project_status
├── Document Agent  → knowledge_search / MCP 文件、邮件、日历只读工具（启用时）
└── Writer Agent    → 无底层工具，只消费任务与证据
```

实现选择 Eino AgentTool，而不是依赖完整上下文共享的 Agent Transfer：Supervisor 只把 JSON `request` 交给专业 Agent，Agent 不默认继承主会话历史；底层工具也按职责分别注入。需要“根据文档写作”时先执行 Document，再把原任务和文档交付物传给 Writer，最后只由 Supervisor 输出一次最终答案。

`agentruntime.Runtime` 开启嵌套事件透传并完成防重复处理：

```text
agent_handoff_started(target, request)
  → 创建 AgentTaskRun(child_run_id)
  → 子 Agent tool_call/tool_result
  → agent_output(agent, deliverable)
  → agent_handoff_completed(target, result)
  → Supervisor 最终 delta
```

每个根 Run 都会创建独立执行状态：最多交接次数、最大并行度、专业 Agent 独立超时和有限重试。Eino ToolNode 会并行执行同一轮的多个独立 AgentTool；Document → Writer 这类依赖链仍串行。Context 取消会停止排队和执行中的子任务，未完成的 `agent_task_runs` 补写 failed/cancelled 终态。

高影响请求可由 `off/risky/all` 策略触发 `approval_requests`。Chat 发出 `approval_required` 后等待 Web 通过独立 HTTP API 提交决定；approved 恢复同一 SSE，rejected/expired 结束根 Run。当前等待通道在进程内，审批记录本身在 SQLite/PostgreSQL 持久化。

Web 将交接事件显示为带 `child_run_id` 的专业 Agent Trace，并显示审批卡片。`make eval-agents` 在隔离 SQLite 中完整经过 Chat、Eino AgentTool 和 RunEvent，当前 7 题得到路由准确率 1、意外专家调用率 0、答案完成率 1；单 Agent Control 质量 0.785714，多 Agent Treatment 质量 1，质量增益 0.214286，调用次数代理比 2。真实模型仍需采集 Token Usage 并扩充业务样本。

## 9. V0.5 MCP 办公连接器架构

第六阶段在只读连接器、内部草稿、人工确认和持久化执行任务内核之上增加显式启用的 Graph 写执行器：

```text
Zora 主进程
├── mcpbridge.Manager（只读 Agent 工具会话）
│   └── CommandTransport（独立最小环境）
        ├── zora-mcp-files 子进程
        │   ├── list_files
        │   └── read_text_file
        └── zora-mcp-microsoft 子进程
            ├── search_emails / get_email
            └── list_calendar_events / get_calendar_event
└── mcpbridge.OfficeExecutor（审批后专用写会话，默认关闭）
    └── zora-mcp-microsoft 子进程
        ├── create_email_draft → checkpoint → send_email_draft
        ├── get_email_delivery_state（故障恢复）
        └── create_calendar_event(transactionId)

Writer Agent / 单 Agent
└── office.Service
    ├── preview_email_draft
    ├── preview_calendar_draft
    ├── draft → pending_confirmation → approved / rejected
    ├── approved → Operation(pending) → executing → completed / failed
    ├── Executor（默认未配置；Graph 执行器必须显式启用）
    └── office_drafts / office_operations + 双事件表（SQLite / PostgreSQL）
```

启动时，`mcpbridge` 按配置逐个启动 stdio Server，执行 MCP 握手和分页工具发现。一个工具必须同时出现在部署者提供的 `allowed_tools` 中，并由 Server 声明 `readOnlyHint=true`；之后才会以 `mcp_{server}_{tool}` 名称进入 Eino。主进程不经过 Shell，子进程也不默认继承环境；模型 Key、Embedding Key 与数据库 DSN 不能透传。

文件连接器在独立进程中固定授权根目录，每次请求重新解析实际路径。绝对路径、父目录逃逸、隐藏路径和逃逸符号链接都会被拒绝；只读取普通 UTF-8 文本。MCP ToolCall 仍通过既有 Runtime，因此无需旁路即可得到 SSE Trace 和持久化 RunEvent。

Microsoft 连接器使用 Graph REST 统一查询邮件和日历。生产模式由子进程从 Secret 文件读取 client secret，通过 client credentials `/.default` 获取、缓存并提前刷新 access token；应用身份必须指定用户 ID，不能使用 `me`。reader 与 writer 使用不同环境前缀、Entra 应用和 Secret，普通 reader 默认不注册写工具。四个只读工具仅返回元数据和正文摘要，不下载邮件附件；默认日历窗口为 7 天、最长 93 天。外部内容始终附带不可信数据提示，系统 Prompt 也要求忽略其中的工具指令、链接和权限请求。

`preview_email_draft` 和 `preview_calendar_draft` 会先校验邮箱、长度、RFC3339 时间窗与 IANA 时区，再保存结构化草稿。`source_run_id + content_hash` 唯一约束吸收 Agent/Writer 重试。REST/Web 可把 `draft` 提交为 `pending_confirmation`，再一次性批准或拒绝；Store 用预期状态条件更新，并把状态和 `office_draft_events` 记录放在同一事务中。只有 `draft` 可以删除，终态记录保留用于审计。

approved 草稿可幂等创建唯一 Operation。领取任务时 Operation 与 Draft 原子进入 executing，并保存执行器、attempt、lease owner/until；成功或失败时两者与 `office_draft_events`、`office_operation_events` 同事务提交。失败和启动时回收的过期租约继续复用原 SHA-256 幂等键。Executor 必须声明幂等安全，且返回真实副作用标记和非空远端引用后才能完成。

Graph 写工具虽然由同一 Microsoft MCP Server 声明，但普通 `mcpbridge.Manager` 仍强制要求 `readOnlyHint=true`，因此写工具不会进入 Eino/Agent。只有 `ZORA_OFFICE_EXECUTOR=microsoft_graph` 时，`OfficeExecutor` 才启动第二条专用 MCP 会话，并只接受固定名称与安全声明匹配的四个协议工具。该子进程仅继承 Graph Token、BaseURL 和 UserID，模型 Key、Embedding Key 与数据库 DSN 不进入其环境。

邮件执行使用两段式恢复协议：Graph 先创建远端邮件草稿并返回不可变 ID，Store 在 Operation 仍持有 executing 租约时原子写入 `external_reference` 和 `executing → executing` 检查点事件，之后才允许发送。发送失败时 failed 状态保留远端引用；重试先查询 `isDraft`，仍为草稿则继续发送，已不是草稿则把上次执行恢复为 completed，不再重复发送。日程以稳定幂等键派生 UUID `transactionId`，Graph 成功返回事件 ID 后同样保存检查点。

默认仍没有 Graph 写 Executor，`approved` 和 `pending` 均明确表示“未执行”；执行 API 返回 503 且不领取任务。OAuth/Secret、写工具双门禁和 Exchange Application RBAC 部署方案已完成，本地协议与故障注入测试通过；真实 Microsoft 租户在线验收因外部资源暂缓，不允许把测试结果描述成真实发送成功。

## 10. V0.6 运行指标架构

```text
agentruntime.Event
      ↓ chat 持久化
AgentRun + RunEvent
      ↓ observability.Aggregate
      ├── SSE done.metrics
      ├── GET /api/runs
      ├── GET /api/runs/{id}/metrics
      └── Web 运行监控
```

Runtime 只负责把 Eino 事件归一化为稳定的模型调用事实，Chat 负责记录第一段用户可见输出和执行事件，Store 负责原始数据，Observability 只做纯聚合。普通工具与 Agent 交接都使用 ToolCall ID 关联开始与完成；未成对事件保留为未完成计数。当前最近 Run 列表最多聚合 100 条，适合单机调试；生产规模再引入异步投影、分位指标和 OpenTelemetry，不替换原始审计链。
