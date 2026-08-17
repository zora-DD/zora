# Zora

用 Go 和 Eino 构建的可观察 Agent 项目。

Zora 的目标不是只提供一个聊天页面，而是逐步实现 Agent 产品的核心工程能力：工具调用、执行审计、向量知识库、长期记忆、多 Agent、人工审批和办公连接器。

当前版本：**V0.4 Multi-Agent**。V0.1 Agent Core 已完成，V0.2 已形成 SQLite/PostgreSQL 双后端 RAG 主链路；V0.3 已交付可追溯、可由用户控制、可通过 A/B 数据验证收益的长期记忆；V0.4 已完成 Supervisor、专业 Agent、并行与执行保险丝、父子 Run、人工审批和单/多 Agent 对照评测。

## 当前能力

| 能力 | 状态 | 说明 |
|---|---|---|
| 流式对话 | 已完成 | SSE 增量回复、停止生成、超时取消 |
| ReAct Agent | 已完成 | Eino ChatModelAgent、工具循环、最大迭代 |
| 模型接入 | 已完成 | 本地 Mock、OpenAI-compatible、通义千问 |
| 工具系统 | 已完成 | 时间、计算器、项目状态、知识检索四个只读工具 |
| 对话管理 | 已完成 | 创建、列表、自动标题、重命名、删除 |
| 持久化 | 已完成 | SQLite 或 PostgreSQL 保存 Conversation、Message、AgentRun 和 RunEvent |
| 执行审计 | 已完成 | ToolCall、ToolResult、完成、失败和取消事件 |
| Web UI | 已完成 | 内嵌响应式页面，不需要 Node.js 部署 |
| 知识库 MVP | 已完成 | TXT/Markdown、哈希去重、重叠分块、Embedding 抽象、向量 + BM25/RRF、引用 |
| RAG 检索与答案评测 | 已完成 | 对比三路召回，并通过真实 Agent 链路评估事实覆盖、有效引用覆盖和引用忠实度 |
| PostgreSQL 向量库 | 已实现 | pgx 连接池、幂等迁移、pgvector HNSW、PostgreSQL FTS、RRF 候选融合 |
| 生产知识库剩余项 | V0.2 进行中 | 文档权限、版本、PDF、更强语义评测和真实数据验收 |
| 长期记忆底座 | 已完成 | Semantic/Episodic Schema、重要性、来源、过期时间、SQLite/PostgreSQL 和用户 CRUD |
| 自动记忆写入 | 已完成 | 真实模型结构化提取、本地规则提取、Memory Key 去重/冲突合并、人工修正保护和 Run 审计 |
| 记忆召回与注入 | 已完成 | 词项相关性 + 重要性 + 时效性联合评分、Top-K 安全上下文和 Run 审计 |
| 会话摘要与上下文压缩 | 已完成 | 阈值触发、增量摘要、最近消息窗口、安全上下文注入、双数据库持久化和审计 |
| 记忆 A/B 评估 | 已完成 | 隔离数据库、完整 Chat 链路 Control/Treatment、召回率、错误注入、事实覆盖、延迟和质量门禁 |
| 多 Agent 路由与协作 | 已完成 | 可选 Supervisor、Research/Document/Writer Agent、上下文隔离、串行依赖与并行独立任务 |
| 多 Agent 生产治理 | 已完成 | 按根 Run 隔离的交接/并行预算、专家超时与有限重试、取消传播、父子 Run 和人工审批 |
| 单/多 Agent 对照评测 | 已完成 | 7 题真实 Chat/RunEvent 链路，对比答案质量、调用次数代理和延迟比例 |
| 办公助手 | V0.5 | MCP、文件/邮件/日历、审批和审计 |

规划中的能力不会以空接口冒充“已完成”。详细进度见 [Roadmap](docs/roadmap.md)。

## 项目特点

- **Go 原生 Agent Runtime**：核心服务、并发、流式传输和持久化均使用 Go。
- **框架复用、业务自研**：Eino 负责 ReAct、Tool 和模型事件；会话、Run、审计及后续 RAG/Memory 机制由项目控制。
- **Mock 不绕过 Agent**：无密钥模式仍经过 Eino ChatModelAgent 和 ToolNode，可稳定测试完整链路。
- **RAG 召回可解释**：同时保留向量相似度、BM25 得分和 RRF 融合结果，每条证据可追溯到文档和字符区间。
- **检索效果可回归**：固定评测集在隔离数据库中重建语料，分别测量 vector、keyword 和 hybrid，避免算法升级只凭主观体验。
- **双存储后端**：SQLite 保留零依赖精确扫描；PostgreSQL 将向量和全文候选召回下推数据库，HTTP 与 Agent Tool 契约保持不变。
- **Embedding 可替换**：默认 Hash Embedding 零密钥运行；生产可切换 OpenAI-compatible Embedding。
- **用户历史与内部轨迹分离**：Message 用于对话上下文，RunEvent 用于调试和审计。
- **长期记忆不是消息向量库**：Memory 拥有独立类型、稳定 Key、来源、重要性和过期时间；候选只在回答成功后提取，同 Key 冲突执行合并，人工修正不会被自动覆盖。
- **召回可解释、可关闭**：轻量词项相关性与重要性、时效性联合评分，并用最低主题相关性阻止弱词面重合被重要性抬高；RunEvent 只记录 ID 和分数组件。
- **记忆收益可回归**：固定数据集让同一问题分别通过关闭/开启召回的完整 Chat 链路，门禁预期召回、错误注入、事实覆盖增益和答案污染，而不是只评估检索函数。
- **长对话不会只靠截断**：较早消息增量压缩进 `conversation_summaries`，最近窗口保留原文；摘要读取或生成失败时自动退化为最近消息，不推翻正常回答。
- **多 Agent 不是角色 Prompt 展示**：Supervisor 通过 Eino AgentTool 调用研究、文档和写作专家；专家只收到结构化 request，底层工具按职责隔离，协作开始/输出/完成均进入 RunEvent。
- **专家路由与收益可回归**：固定 7 题覆盖单专家、串行依赖、并行任务、直接回答和硬负例；同题运行单 Agent Control 与多 Agent Treatment，门禁质量增益、调用次数代理和延迟比例。
- **多 Agent 有执行保险丝**：交接次数、并行度、专家超时和重试都按根 Run 隔离；专业 Agent 另存子 Run，失败和取消有明确终态。
- **高影响请求先审批**：审批记录持久化，SSE 在 `approval_required` 后等待 Web 决策，通过后恢复原 Run，拒绝或超时进入明确终态。
- **明确的终态语义**：根 Run 最终进入 completed、failed、cancelled 或 rejected。
- **工具安全优先**：显式 allowlist；计算器不使用 eval、Shell 或代码执行。
- **单二进制运行**：SQLite 和前端资源均包含在本地部署方案中。
- **为面试深度设计**：可以讨论框架隔离、流式 ToolCall、并发、取消、RAG 评估、Memory 生命周期和 Multi-Agent 收益。

## 快速开始

### 环境要求

- Go 1.26 或更高版本
- 可选：Docker
- 接真实模型时需要相应 API Key

### 使用本地 Mock 模型

```bash
make run
```

打开 [http://localhost:8088](http://localhost:8088)。默认不需要 API Key。

侧边栏“长期记忆”可维护 `semantic`（稳定事实/偏好）和 `episodic`（经历/事件）记忆，并设置重要性及可选过期时间。默认开启自动候选提取：本地 Mock 只识别明确的“记住”、个人资料和稳定偏好；真实模型使用严格中文 JSON Prompt 提取。自动记忆会关联来源会话/消息，同一 `memory_key` 的新值会合并，用户手动修改后自动流程不再覆盖。

可以用本地 Mock 验证：

```text
我的主要编程语言是 Go。
我的主要编程语言是 Java。
我以后希望你用中文回答。
```

前两句话最终只保留一个“主要编程语言”记忆，值更新为 Java。继续询问“我的主要编程语言是什么？”，系统会按相关性、重要性和时效性召回该记忆并注入模型上下文，本地 Mock 会确定性回答 Java。

默认每当未摘要历史达到 20 条消息时，Zora 会把较早部分增量合并为会话摘要，并保留最近 12 条原始消息。完整消息不会从数据库删除；可通过 `GET /api/conversations/{id}/summary` 查看当前摘要覆盖范围。本地 Mock 使用确定性规则便于测试，真实 Provider 使用同一 Chat Model 通过独立中文结构化 Prompt 生成摘要。

运行长期记忆 A/B 基准：

```bash
make eval-memory
```

命令在临时 SQLite 中写入 `evals/memory.json` 的固定记忆，同一问题交替运行关闭召回的 Control 和开启召回的 Treatment，并从真实 RunEvent 读取实际注入的 Memory ID。默认 5 题包含个人资料、交互偏好、项目经历，以及“Go 并发模型”这种相似主题硬负例；门禁未达标时命令返回非零状态。

### 启用多 Agent

多 Agent 会增加模型调用次数，因此默认关闭。通过环境变量显式启用：

```bash
ZORA_MULTI_AGENT_ENABLED=true make run
```

启用后，Supervisor 会把任务交给研究、文档或写作专家。复合办公任务可以先由文档专家检索证据，再交给写作专家整理：

```text
帮我计算 (128 + 72) * 3.5
帮我写一封会议延期通知
根据我上传的文档，写一份项目发布通知
```

Web 对话中会显示“协作：研究专家/文档专家/写作专家”轨迹。运行固定路由基准：

```bash
make eval-agents
```

独立任务可以并行交接，例如“同时计算 6*7，并写一条结果通知”；文档取证再写作仍保持串行。默认 `risky` 审批模式会在“发送给、发布到、删除、部署到”等高影响请求前显示审批卡片，批准后原 SSE 继续执行。

评测命令使用隔离 SQLite 和真实 `chat.Send → Eino AgentTool → RunEvent` 链路。默认 7 题基线路由准确率 1、意外专家调用率 0、答案完成率 1；单 Agent Control 质量 0.785714，多 Agent Treatment 质量 1，质量增益 0.214286，调用次数代理比 2。耗时比例随机器波动，报告会输出并按宽松上限门禁；真实模型仍应使用业务样本和真实 Token Usage 重新验证。

可以尝试：

```text
现在上海几点？
帮我计算 (128 + 72) * 3.5
介绍一下这个项目现在有哪些功能
```

点击侧边栏的“知识库”可上传 UTF-8 编码的 `.txt` / `.md` / `.markdown` 文件（单文件最大 5 MiB）。上传后可以询问：

```text
根据我上传的文档，项目的发布日期和上线要求是什么？
```

本地 Mock 会展示确定性的证据摘要和 `[README.md#0]` 形式引用，工具 Trace 中可查看完整检索结果；配置真实 Chat Model 后，模型会根据证据组织回答并标注文档名与分块编号。

运行内置 RAG 检索基准：

```bash
make eval-rag
```

命令会在临时 SQLite 数据库中摄取 `evals/knowledge.json`，不会修改在线知识库。报告先给出三种检索模式的 Recall@K、MRR、命中率、平均延迟和逐题排名，再通过与线上相同的 Eino Runtime 和 `knowledge_search` 生成答案，计算事实覆盖率、有效引用覆盖率和引用忠实度；任一门禁未达标都会返回非零退出码。

默认 Hash Embedding + Mock 基线的 4 个问题达到 Recall@3=1、MRR=1，答案三项指标均为 1。答案评测采用数据集中显式标注的事实/证据锚点，是零密钥、可重复的工程基线；它不能替代真实模型语义评测或人工抽检。当前检索小样本下三种模式仍然打平，因此尚不能据此声称混合召回优于单路。

数据默认保存到：

```text
./data/zora.db
```

### 使用 PostgreSQL + pgvector

直接启动 PostgreSQL 和 Zora：

```bash
docker compose up --build
```

也可以只启动数据库，再从本机运行 Go 服务：

```bash
make postgres-up
make run-postgres
```

PostgreSQL 模式使用 HNSW 余弦向量索引和 GIN 全文索引生成两路候选，`knowledge.Service` 继续执行 RRF，保证与 SQLite 使用相同的融合规则。首次启动会执行幂等建表；多实例迁移通过 advisory lock 串行化。

修改 `ZORA_EMBEDDING_DIMENSIONS` 后，现有 `vector(N)` 列不会被静默改写。服务会在启动时拒绝维度不一致的数据库，需要先迁移或重建知识索引。

## 接入通义千问

Zora 使用 OpenAI-compatible 模型协议：

```bash
ZORA_MODEL_PROVIDER=openai \
ZORA_MODEL=qwen-plus \
ZORA_API_KEY=你的_DashScope_API_Key \
ZORA_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1 \
make run
```

也可以替换 `ZORA_BASE_URL` 和 `ZORA_MODEL`，连接其他兼容 Tool Calling 的模型。

注意：

- 不要将 API Key 写入源码或提交到 Git；
- 所选模型需要支持 Tool Calling；
- 自定义 System Prompt 中如果包含 Eino 模板占位符语法，需要正确转义花括号。

如果需要真实语义检索，再增加：

```bash
ZORA_EMBEDDING_PROVIDER=openai \
ZORA_EMBEDDING_MODEL=text-embedding-v4 \
ZORA_EMBEDDING_DIMENSIONS=1024 \
make run
```

Embedding 配置默认复用上面的 DashScope Key 和 BaseURL，也可通过 `ZORA_EMBEDDING_API_KEY` / `ZORA_EMBEDDING_BASE_URL` 单独指定。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `ZORA_ADDR` | `:8088` | HTTP 监听地址 |
| `ZORA_DATA_DIR` | `./data` | SQLite 数据目录 |
| `ZORA_STORE_PROVIDER` | `sqlite` | `sqlite` 或 `postgres` |
| `ZORA_POSTGRES_DSN` | 空 | postgres 模式必填的连接串 |
| `ZORA_POSTGRES_MAX_CONNS` | `10` | PostgreSQL 连接池上限，范围 1–100 |
| `ZORA_MODEL_PROVIDER` | `mock` | `mock` 或 `openai` |
| `ZORA_MODEL` | `qwen-plus` | 真实模型名称 |
| `ZORA_API_KEY` | 空 | openai 模式必填 |
| `ZORA_BASE_URL` | 空 | OpenAI-compatible API 地址 |
| `ZORA_SYSTEM_PROMPT` | 内置中文指令 | Agent 系统指令 |
| `ZORA_REQUEST_TIMEOUT` | `90s` | 单次 Agent 请求超时 |
| `ZORA_MAX_ITERATIONS` | `8` | ReAct 最大迭代，范围 1–50 |
| `ZORA_MULTI_AGENT_ENABLED` | `false` | 是否启用 Supervisor 与三个专业 Agent；默认关闭以控制成本 |
| `ZORA_MULTI_AGENT_MAX_HANDOFFS` | `6` | 单轮最多专业 Agent 交接次数，范围 1–20 |
| `ZORA_MULTI_AGENT_MAX_PARALLEL` | `3` | 单轮专业 Agent 最大并行数，范围 1–10 |
| `ZORA_MULTI_AGENT_SPECIALIST_TIMEOUT` | `30s` | 每次专业 Agent 调用的独立超时 |
| `ZORA_MULTI_AGENT_RETRY_COUNT` | `1` | 专业 Agent 失败后的重试次数，范围 0–3 |
| `ZORA_MULTI_AGENT_APPROVAL_MODE` | `risky` | `off`、`risky` 或 `all`；仅启用多 Agent 时接入审批闸门 |
| `ZORA_MULTI_AGENT_APPROVAL_TIMEOUT` | `60s` | 等待人工审批的最长时间 |
| `ZORA_EMBEDDING_PROVIDER` | `hash` | `hash` 或 `openai` |
| `ZORA_EMBEDDING_MODEL` | `text-embedding-v4` | 真实 Embedding 模型名 |
| `ZORA_EMBEDDING_API_KEY` | 复用 `ZORA_API_KEY` | Embedding 独立密钥 |
| `ZORA_EMBEDDING_BASE_URL` | 复用 `ZORA_BASE_URL` | Embedding API 的 v1 根地址 |
| `ZORA_EMBEDDING_DIMENSIONS` | hash: `384`；openai: `1024` | 向量维度，变更后需重建旧索引 |
| `ZORA_KNOWLEDGE_CHUNK_SIZE` | `800` | 每个分块的 Unicode 字符上限 |
| `ZORA_KNOWLEDGE_CHUNK_OVERLAP` | `120` | 相邻分块重叠字符数 |
| `ZORA_MEMORY_AUTO_CAPTURE` | `true` | 成功回答后是否自动提取并合并长期记忆 |
| `ZORA_MEMORY_MAX_CANDIDATES` | `3` | 单轮最多候选数，范围 1–10 |
| `ZORA_MEMORY_RECALL_ENABLED` | `true` | 是否在回答前召回并注入相关记忆，可独立关闭做 A/B |
| `ZORA_MEMORY_RECALL_LIMIT` | `5` | 单轮最多注入的记忆数，范围 1–20 |
| `ZORA_MEMORY_RECALL_MIN_SCORE` | `0.25` | 相关性、重要性、时效性联合分数门槛 |
| `ZORA_SUMMARY_ENABLED` | `true` | 是否启用增量会话摘要和上下文压缩 |
| `ZORA_SUMMARY_TRIGGER_MESSAGES` | `20` | 未摘要消息触发阈值，范围 4–500 |
| `ZORA_SUMMARY_KEEP_RECENT` | `12` | 始终保留原文的最近消息数，至少 2 且小于触发阈值 |
| `ZORA_SUMMARY_MAX_RUNES` | `4000` | 单份摘要最大 Unicode 字符数，范围 500–20000 |

配置模板见 [.env.example](.env.example)。项目不会自动读取 `.env`；生产环境应通过容器、Secret 或部署平台注入环境变量。

## Docker

推荐的 PostgreSQL + pgvector 方式：

```bash
docker compose up --build
```

Compose 使用 `pgvector/pgvector:0.8.6-pg16-bookworm`，数据库从宿主机映射到 `54328`，Zora 仍监听 `8088`。

构建并运行本地 Mock 模式：

```bash
docker build -t zora:dev .
docker run --rm \
  -p 8088:8088 \
  -v zora-data:/app/data \
  zora:dev
```

运行通义千问：

```bash
docker run --rm \
  -p 8088:8088 \
  -v zora-data:/app/data \
  -e ZORA_MODEL_PROVIDER=openai \
  -e ZORA_MODEL=qwen-plus \
  -e ZORA_API_KEY=你的_DashScope_API_Key \
  -e ZORA_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1 \
  zora:dev
```

## 核心执行链

```mermaid
sequenceDiagram
    participant UI as Web UI
    participant API as "Go HTTP/SSE"
    participant Service as Chat Service
    participant DB as SQLite
    participant ADK as Eino Runtime
    participant Supervisor as Supervisor
    participant Specialist as Specialist Agent
    participant LLM as Model
    participant Tool as ToolNode

    UI->>API: POST message
    API->>Service: Send
    Service->>DB: 保存 user Message 和 running Run
    Service->>DB: 读取会话摘要和最近原始消息
    Service->>DB: 查询有效长期记忆
    Service->>Service: 相关性 + 重要性 + 时效性联合排序
    Service->>ADK: 会话摘要 + 安全记忆上下文 + 最近原始消息
    ADK->>Supervisor: 执行根 Agent
    Supervisor->>LLM: 消息 + Tool/Agent Schema
    alt 已启用多 Agent 且需要专业能力
        LLM-->>Supervisor: AgentTool Call
        Supervisor->>Specialist: AgentTool(request)
        Specialist->>Tool: 仅调用职责内工具
        Tool-->>Specialist: ToolResult
        Specialist-->>Supervisor: 专家交付物
        ADK-->>UI: agent_handoff_started / agent_output / agent_handoff_completed
    end
    alt 需要工具
        LLM-->>ADK: ToolCall
        ADK->>Tool: 执行参数
        Tool-->>ADK: ToolResult
        ADK->>LLM: 注入工具结果
        ADK-->>UI: tool_call / tool_result
    end
    LLM-->>ADK: 流式回答
    ADK-->>UI: delta
    Service->>DB: 保存回答
    Service->>LLM: 提取长期记忆候选（真实模型）
    Service->>DB: 按 Memory Key 去重/冲突合并
    Service->>LLM: 达到阈值时增量生成会话摘要
    Service->>DB: 保存摘要覆盖序号和审计事件
    Service->>DB: 保存记忆审计和完成事件
    Service-->>UI: done（含记忆处理计数）
```

## API 概览

| Method | Path | 作用 |
|---|---|---|
| `GET` | `/api/health` | 健康检查 |
| `GET` | `/api/info` | 版本、模型和能力 |
| `GET` | `/api/conversations` | 对话列表 |
| `POST` | `/api/conversations` | 创建对话 |
| `PATCH` | `/api/conversations/{id}` | 重命名对话 |
| `DELETE` | `/api/conversations/{id}` | 删除对话及关联数据 |
| `GET` | `/api/conversations/{id}/messages` | 查询消息历史 |
| `GET` | `/api/conversations/{id}/summary` | 查询当前增量会话摘要及覆盖范围 |
| `POST` | `/api/conversations/{id}/messages` | 发送消息并接收 SSE |
| `GET` | `/api/runs/{id}/events` | 查询持久执行事件 |
| `GET` | `/api/runs/{id}/children` | 查询根 Run 下的专业 Agent 子 Run、状态和输出摘要 |
| `GET` | `/api/approvals` | 审批开启时查询记录，可通过 `status` 和 `limit` 筛选 |
| `POST` | `/api/approvals/{id}/decision` | 审批开启时提交 `approved` 或 `rejected` 决定并恢复等待中的 Run |
| `GET` | `/api/knowledge/documents` | 查询已索引文档 |
| `POST` | `/api/knowledge/documents` | multipart 上传 TXT/Markdown 并同步索引 |
| `DELETE` | `/api/knowledge/documents/{id}` | 删除文档及其分块 |
| `POST` | `/api/knowledge/search` | 执行向量 + BM25/RRF 混合检索 |
| `GET` | `/api/memories` | 查询长期记忆，可按类型筛选并选择是否包含已过期项 |
| `POST` | `/api/memories` | 手动创建 Semantic/Episodic 记忆 |
| `POST` | `/api/memories/recall` | 调试记忆联合召回，返回总分及分数组件 |
| `GET` | `/api/memories/{id}` | 查询单条长期记忆及来源 |
| `PUT` | `/api/memories/{id}` | 完整更新内容、类型、重要性和过期时间 |
| `DELETE` | `/api/memories/{id}` | 用户删除长期记忆 |

SSE 事件：`start`、`approval_required`、`approval_approved/rejected/expired`、`tool_call`、`tool_result`、`agent_handoff_started`、`agent_output`、`agent_handoff_completed`、`delta`、`done`、`error`。交接事件包含 `child_run_id`；`agent_output` 只显示在协作 Trace，不拼入最终回答。开启自动记忆时，`done.memory` 返回候选、新增、更新和跳过数量；`done.memory_recalled` 返回实际注入数量；本轮触发摘要时，`done.summary` 返回覆盖序号、消息数和字符数。候选、召回及摘要正文都不会复制进 SSE 或 RunEvent。

完整请求、响应和事件契约见 [项目技术文档](docs/technical-design.md)。

## 项目结构

```text
cmd/zora/                  服务入口、依赖组装和优雅关闭
cmd/zora-eval/             隔离运行固定 RAG 检索与答案评测
cmd/zora-memory-eval/      隔离运行长期记忆有/无 A/B 评测
cmd/zora-agent-eval/       隔离运行多 Agent 路由与协作评测
evals/                     可版本化的 RAG/Memory/Multi-Agent 数据、锚点与阈值
internal/config/           环境配置与启动校验
internal/domain/           Conversation、Message、Run、Event
internal/id/               随机业务 ID
internal/agentruntime/     Eino Runtime、模型适配和事件转换
internal/agentseval/       专家路由与单/多 Agent 质量、成本代理、耗时对照门禁
internal/agenttools/       只读工具和安全计算器
internal/approval/         人工审批策略、等待/恢复和持久化契约
internal/knowledge/        文档分块、Embedding、混合检索和 Agent Tool
internal/rageval/          检索指标、答案引用/忠实度指标和门禁
internal/memory/           Semantic/Episodic 模型、提取、Consolidation、联合召回和用户 CRUD
internal/memoryeval/       长期记忆 Control/Treatment 指标、报告和质量门禁
internal/summary/          增量摘要策略、Model/Rule 摘要器和持久化契约
internal/chat/             会话用例、并发控制和 Run 生命周期
internal/store/            可替换的持久化接口
internal/store/sqlite/     对话与知识库的 SQLite 实现
internal/store/postgres/   pgx、pgvector HNSW、PostgreSQL FTS 和迁移
internal/httpapi/          REST、SSE 和内嵌 Web UI
docs/                      分析、技术设计、架构和 Roadmap
```

## 开发与验证

```bash
# 单元和集成测试
make test

# 静态分析
make vet

# 固定 RAG 检索与答案评测
make eval-rag

# 长期记忆有/无 A/B 评测
make eval-memory

# 多 Agent 路由与协作评测
make eval-agents

# 启动 pgvector 并执行真实数据库集成测试
make postgres-up
make test-postgres

# 格式化、测试和静态分析
make check

# 并发竞态检测
go test -race ./internal/...

# 验证无 CGO 构建
CGO_ENABLED=0 go build ./cmd/zora
```

当前测试覆盖：

- 计算器优先级、括号、一元运算、非法表达式和除零；
- Mock 模型通过 Eino 完成 ToolCall → ToolResult → Answer；
- SQLite Conversation/Message 生命周期和级联删除；
- PostgreSQL Store 契约、向量维度、FTS 词项和可选真实数据库生命周期测试；
- HTTP 创建对话和 SSE 工具调用链；
- 根页面、静态资源和 SPA 路由回退；
- Unicode 分块边界、重叠与原文字符偏移；
- Hash Embedding 可复现性和 OpenAI-compatible Embedding 批处理；
- 文档入库、哈希去重、混合检索、引用和级联删除；
- 向量、关键词、混合三种检索模式及固定集 Recall@K/MRR 计算；
- HTTP multipart 上传、知识检索与删除。
- 增量摘要阈值、最近窗口、序号间隔、结构化模型输出、敏感信息过滤和安全上下文注入；
- SQLite 摘要 Upsert/级联删除、PostgreSQL Schema，以及 HTTP 摘要查询和 Mock 端到端回忆。
- 长期记忆 A/B 数据集校验、Control/Treatment 指标、错误召回与答案污染反例、RunEvent 召回 ID 解析和完整 CLI 基线。
- Supervisor/AgentTool 串行与并行交接、执行预算/超时/重试/取消、子 Run、人工审批等待与恢复，以及 7 题单/多 Agent 对照 CLI 基线。

## 文档导航

- [项目分析文档](docs/project-analysis.md)：业务目标、业务模型、数据模型、技术架构、项目亮点和风险。
- [项目技术文档](docs/technical-design.md)：核心流程、包设计、配置、接口、SSE、并发、安全、测试和扩展方案。
- [架构说明](docs/architecture.md)：当前边界及 RAG、Memory、Multi-Agent 接入点的简版说明。
- [Roadmap](docs/roadmap.md)：各版本任务、状态和验收条件。

## 常见问题

### 为什么默认使用 Mock？

为了让项目在没有 API Key 时仍能运行和测试。Mock 实现的是 Eino 模型接口，工具调用仍经过真实 Agent 链路。

### 为什么默认仍使用 SQLite？

它提供零运维体验，适合演示和本地开发。需要更大的数据规模或多连接服务时，可将 `ZORA_STORE_PROVIDER` 改为 `postgres`；向量 HNSW 和全文候选召回会下推 PostgreSQL，而上层 Service、Tool 和 HTTP API 不变。

### 为什么多 Agent 默认关闭？

V0.4 已实现 Supervisor、专业 Agent、执行治理和 Control/Treatment 对照。当前 Mock 小样本中质量从 0.785714 提升到 1，但调用次数代理增加到 2 倍，这不足以证明真实模型场景普遍划算。默认关闭可以避免意外成本；使用 `ZORA_MULTI_AGENT_ENABLED=true` 显式开启，并通过 `make eval-agents` 在自己的业务集上复验。

### 为什么工具结果没有全部写进下一轮历史？

工具内部轨迹保存在 RunEvent，主 Message 只保存用户可见历史。长对话会把较早消息增量合并为摘要，同时保留最近原文；相关长期记忆按当前问题单独召回，因此不需要把全部工具轨迹反复发送给模型。

### 如何清空本地数据？

停止服务后删除 `ZORA_DATA_DIR` 中的 `zora.db`。该操作不可恢复，请先备份需要保留的对话。

## Roadmap

- V0.1：Agent Core——已完成
- V0.2：向量知识库与 RAG——主链路已实现，生产增强项继续迭代
- V0.3：长期记忆——Schema、双存储、用户 CRUD、自动写入、Consolidation、召回注入、会话摘要和 A/B 门禁已完成
- V0.4：多 Agent——Supervisor、专业 Agent、并行/执行治理、父子 Run、人工审批和单/多 Agent 对照已完成
- V0.5：MCP 办公助手

详见 [docs/roadmap.md](docs/roadmap.md)。

## License

本项目暂未指定开源许可证。公开发布前应根据使用和分发目标选择许可证。
