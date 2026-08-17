# Zora 架构说明

> 本文是便于快速阅读的架构摘要。完整业务分析见 [项目分析文档](project-analysis.md)，完整流程、接口和设计细节见 [项目技术文档](technical-design.md)。

## 1. 边界

Zora 将系统划分为七个边界：

1. `httpapi`：HTTP、JSON、SSE 和静态界面，不包含 Agent 规则。
2. `chat`：用例编排、事务顺序、并发保护和执行审计。
3. `agentruntime`：Eino ADK 适配，输出与传输协议无关的事件。
4. `agenttools`：工具 Schema、输入校验和执行代码。
5. `knowledge`：文档摄取、Embedding、混合检索、引用和 `knowledge_search` Tool。
6. `store`：对话与知识库的持久化边界，由 SQLite 或 PostgreSQL 实现。
7. `rageval`：固定数据集校验、检索指标、答案引用/忠实度和联合门禁。

依赖方向始终从传输层指向应用层和抽象层，Eino 类型不会进入 HTTP API 的公开数据模型。

## 2. 为什么选择 Eino，而不是完全手写循环

V0.1 直接使用 `ChatModelAgent + Runner`，以获得：

- 标准工具 Schema 与调用循环
- 模型/工具事件
- 最大迭代次数
- 流式输出
- 后续 Middleware、Checkpoint 和 Multi-Agent 升级路径

项目保留自己的 `agentruntime.Event`，避免上层被某个 Eino 版本的数据结构锁死。Eino 当前固定在稳定版本，升级必须先通过现有集成测试。

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

采用 append-only 审计：`run_started`、`tool_call`、`tool_result`、`model_output`、`run_completed/failed/cancelled`。流式 token 只发往客户端，不逐 token 落库。

## 4. 并发与取消

- 同一 Conversation 同时只允许一个 Run 修改历史，避免两个请求读取相同旧上下文后交错落库。
- 不同 Conversation 可以并发运行。
- 浏览器 Abort、HTTP Context 取消和服务端 Deadline 会传入 Eino 与模型请求。
- 模型客户端和整个消息请求都有超时。

PostgreSQL Store 已对 schema migration 使用 advisory transaction lock；业务对话锁仍是进程内 Mutex，多实例部署前还应升级为数据库 advisory lock 或带租约的分布式锁。

## 5. 安全基线

- 工具采用显式 allowlist。
- 计算器使用递归下降解析器，不执行表达式代码，也不调用 Shell。
- 请求体限制为 1 MiB，消息限制为 20,000 字符。
- Web UI 对模型输出做 HTML 转义。
- 默认 Content Security Policy 只允许同源资源。
- API Key 只从环境变量读取。
- 当前没有任何写入外部系统的工具。
- 知识文档限制为 UTF-8 TXT/Markdown 且最大 5 MiB，文档删除需要用户确认。

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

## 7. 长期记忆接入点

长期记忆使用异步 Consolidation，而不是把所有聊天记录向量化：

```text
对话结束 → 候选事实提取 → 置信度判断 → 去重/合并 → 持久化
新请求   → 相关性 + 时效性 + 重要性召回 → 注入 Agent 上下文
```

每条 Memory 必须包含来源消息、类型、置信度、创建/更新时间和可选过期时间，并提供用户查看和删除接口。

## 8. 多 Agent 接入点

单 Agent 的工具链与评估稳定后，增加 Supervisor：

```text
Supervisor
├── Research Agent
├── Document Agent
└── Writer Agent
```

子 Agent 通过 Agent-as-Tool 返回结构化结果。共享的只有任务输入和明确交接物，不默认共享全部历史。Run Event 将增加父子 Run ID、预算、重试和审批事件。
