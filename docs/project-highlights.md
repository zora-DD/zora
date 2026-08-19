# Zora 项目亮点全面分析

本文面向项目作者和准备 Agent 开发岗位面试的开发者。它不只罗列功能，而是回答每个亮点背后的五个问题：代码在哪里、业务场景是什么、原始问题是什么、如何实现、为什么选择这种实现。

建议先阅读 [README](../README.md) 跑通系统，再结合本文逐项阅读源码。更完整的数据模型、架构和面试材料见[项目分析文档](project-analysis.md)，接口级细节见[项目技术文档](technical-design.md)。

## 亮点总览

| 编号 | 亮点 | 主要面试价值 |
|---|---|---|
| 1 | Go 原生业务内核 + Eino 框架隔离 | 框架选型、边界设计、可替换性 |
| 2 | Mock 仍经过真实 ReAct 与工具链 | 可测试性、开发效率、契约一致性 |
| 3 | Message、Run、RunEvent 三层执行模型 | 领域建模、审计、可观察性 |
| 4 | SSE 流式事件、取消传播与会话并发控制 | Go 并发、Context、长连接 |
| 5 | 多模型安全注册与请求级路由 | 配置安全、运行时装配、审计 |
| 6 | 文档摄取、版本、ACL 与可定位引用 | RAG 数据治理、权限、可解释性 |
| 7 | 向量 + BM25/FTS + RRF 混合检索 | 检索算法、双存储取舍 |
| 8 | RAG 不是“感觉有效”，而是可回归评测 | Eval、质量门禁、工程闭环 |
| 9 | 长期记忆是独立领域模型 | Memory 生命周期、冲突合并、用户控制 |
| 10 | 增量摘要 + 最近原文窗口 | 上下文压缩、成本、信息损失控制 |
| 11 | 真正的 Supervisor/AgentTool 多 Agent | 职责隔离、串并行、成本治理 |
| 12 | 持久化 Human-in-the-loop 审批 | 高风险操作、安全恢复、状态机 |
| 13 | MCP 工具白名单、只读校验与凭据隔离 | 供应链边界、最小权限、Prompt Injection |
| 14 | 草稿、确认、Operation、执行器四层隔离 | 外部副作用、幂等、租约和恢复 |
| 15 | 指标从持久化执行事实派生 | Token、TTFT、耗时与指标可信度 |
| 16 | SQLite/PostgreSQL 双实现与渐进式部署 | 接口抽象、MVP 到生产演进 |
| 17 | 固定数据集的 Control/Treatment 评测 | 实验设计、质量/成本权衡 |
| 18 | 单二进制 Web 与安全 Markdown 流式渲染 | Go Embed、前端性能、安全输出 |
| 19 | OTel Trace 与 Prometheus 低基数指标 | 跨层排障、SLO、可观察性边界 |
| 20 | Memory Capture 事务 Outbox 与租约 Worker | 原子提交、异步任务、退避重试、崩溃恢复 |

## 1. Go 原生业务内核，Eino 只负责 Agent Runtime

### 关键代码位置

- [`cmd/zora/main.go`](../cmd/zora/main.go)：依赖装配与启动顺序；
- [`internal/agentruntime/runtime.go`](../internal/agentruntime/runtime.go)：Eino ChatModelAgent、模型和事件适配；
- [`internal/chat/service.go`](../internal/chat/service.go)：会话、Run、Memory、Summary 与审计编排；
- [`internal/store/store.go`](../internal/store/store.go)：持久化抽象。

### 业务场景

一个 Agent 产品既需要复用成熟框架的 ReAct 与 Tool Calling，又需要掌握自己的会话、权限、审计、记忆和外部副作用逻辑。

### 问题分析

如果全部手写 Agent 循环，需要处理工具 Schema、流式 ToolCall 拼接、模型兼容和迭代终止；如果把所有逻辑都塞进框架，又会让业务状态与框架类型强耦合，后续替换模型或框架成本很高。

### 技术实现

`agentruntime.Runtime` 把 Eino 的 Agent 事件转换为 Zora 自己的 `Event`；`chat.Service` 只依赖 Runtime 暴露的执行接口和业务 Store。数据库、审批、记忆、摘要、Office 状态机均位于框架之外。入口按“配置 → Store → Tool/Runtime → Service → HTTP”组装依赖。

### 为什么这样实现

这是“框架负责通用机制，业务拥有核心状态”的折中。它比完全自研更快，比框架侵入全项目更可维护，也让面试中能够清楚说明 Eino 替换边界。

## 2. Mock 不绕过 Agent，仍执行真实 ReAct 链路

### 关键代码位置

- [`internal/agentruntime/mock_model.go`](../internal/agentruntime/mock_model.go)：实现 Eino 模型接口并产生 ToolCall；
- [`internal/agentruntime/runtime_test.go`](../internal/agentruntime/runtime_test.go)：验证 Mock → Agent → Tool → Answer；
- [`internal/httpapi/server_test.go`](../internal/httpapi/server_test.go)：端到端 SSE、草稿、RAG 与 Memory 测试。

### 业务场景

开发者没有模型额度、CI 不允许访问公网，或需要稳定复现工具调用与异常场景时，仍然要验证完整产品链路。

### 问题分析

常见 Mock 直接返回最终字符串，这只能测试页面，无法覆盖 Eino ToolNode、工具参数、RunEvent、SSE 和持久化。真实模型又存在费用、速率限制和非确定性。

### 技术实现

Mock 实现与真实模型相同的 ChatModel 接口，根据意图生成结构化 ToolCall；收到 ToolResult 后再生成最终答案。测试因此经过相同 Runtime、Chat Service、Store 和 HTTP 层。

### 为什么这样实现

Mock 的价值不是假装模型聪明，而是提供确定性的协议替身。它降低本地和 CI 成本，同时避免维护一套“测试专用假链路”。

## 3. Message、AgentRun、RunEvent 三层执行模型

### 关键代码位置

- [`internal/domain/types.go`](../internal/domain/types.go)：`Message`、`AgentRun`、`AgentTaskRun`、`RunEvent`；
- [`internal/chat/service.go`](../internal/chat/service.go)：Run 生命周期和事件写入；
- [`internal/store/sqlite/sqlite.go`](../internal/store/sqlite/sqlite.go) 与 [`internal/store/postgres/schema.go`](../internal/store/postgres/schema.go)：表结构。

### 业务场景

用户需要干净的聊天历史，开发者又需要查看某次回答调用了哪些工具、为什么失败、用了哪个模型和多少 Token。

### 问题分析

把 ToolCall 和内部 Prompt 全写入 Message 会污染下一轮上下文；只保存最终回答又无法调试。更新同一条 JSON 日志也难以重建顺序和耗时。

### 技术实现

- `Message`：只保存用户可见的 user/assistant/tool 消息；
- `AgentRun`：记录一次请求的模型、开始时间和 completed/failed/cancelled/rejected 终态；
- `RunEvent`：追加写入 tool、handoff、model usage、first token 等事实；
- `AgentTaskRun`：保存专业 Agent 子任务的当前状态与输出摘要。

### 为什么这样实现

三类对象分别满足产品历史、执行状态和审计时间线，避免一个表承担冲突职责。追加式事件还能从时间差重建指标，不依赖易失的内存计时器。

## 4. SSE 流式输出、取消传播与会话级并发控制

### 关键代码位置

- [`internal/httpapi/server.go`](../internal/httpapi/server.go)：SSE Handler 和事件输出；
- [`internal/httpapi/context.go`](../internal/httpapi/context.go)：请求超时 Context；
- [`internal/chat/service.go`](../internal/chat/service.go)：`SendWithModel`、会话锁、失败终态；
- [`internal/agentruntime/runtime.go`](../internal/agentruntime/runtime.go)：Runtime 流式事件；
- [`internal/httpapi/web/app.js`](../internal/httpapi/web/app.js)：SSE 消费、停止生成、帧合并渲染。

### 业务场景

真实模型可能持续几十秒，用户需要尽快看到首字、观察工具调用，并且可以停止生成。

### 问题分析

长连接要同时处理客户端断开、服务端超时、模型流错误和工具错误。同一会话并发写入还会导致历史顺序错乱。前端若每个 token 重建全部 DOM，会明显闪烁。

### 技术实现

HTTP 请求 Context 贯穿 Chat、Runtime、Tool 和 Store；客户端 Abort 后取消信号向下传播。`chat.Service` 对 conversation ID 加细粒度锁，同一会话串行、不同会话并行。SSE 使用业务事件契约。前端用 `requestAnimationFrame` 合并密集 token，并只更新当前草稿节点。

### 为什么这样实现

Go Context 是跨层取消的标准机制，会话级锁比全局锁并发度更高。SSE 比 WebSocket 更适合单向增量响应，部署和调试成本也更低。

## 5. 多模型安全注册与请求级路由

### 关键代码位置

- [`internal/config/config.go`](../internal/config/config.go)：`ModelProfile` 解析、校验和 Key 引用；
- [`cmd/zora/main.go`](../cmd/zora/main.go)：为每个 Profile 装配独立 Runtime；
- [`internal/chat/service.go`](../internal/chat/service.go)：`WithRuntimeProfiles`、`selectRuntime`；
- [`internal/httpapi/web/app.js`](../internal/httpapi/web/app.js)：模型选择器与 `model_id` 请求参数。

### 业务场景

用户希望在低成本模型、推理模型、本地 Mock 之间自由切换，并比较效果、延迟和费用。

### 问题分析

允许浏览器提交任意 `base_url`、模型名或 Key 会造成 SSRF、密钥泄漏和越权；运行中临时修改全局模型又会引发并发污染。

### 技术实现

服务启动时从 `ZORA_MODELS_JSON` 构建最多 20 个可信 Profile。JSON 只允许引用 `api_key_env`，禁止内嵌 Key。每个 Profile 拥有独立 Runtime，请求只能提交白名单内的 `model_id`。实际模型写入 AgentRun，后台记忆提取和摘要固定使用默认模型。

### 为什么这样实现

模型选择是服务端注册表路由，而不是客户端动态配置。这样兼顾自由切换、密钥安全、并发隔离与结果审计。

## 6. 文档摄取、版本、ACL 与可定位引用

### 关键代码位置

- [`internal/knowledge/service.go`](../internal/knowledge/service.go)：文档摄取、去重、版本和检索；
- [`internal/knowledge/chunker.go`](../internal/knowledge/chunker.go)：Unicode 重叠分块；
- [`internal/knowledge/types.go`](../internal/knowledge/types.go)：Document、Chunk、SearchResult；
- [`internal/knowledge/tool.go`](../internal/knowledge/tool.go)：Agent 知识检索工具；
- [`internal/store/postgres/knowledge.go`](../internal/store/postgres/knowledge.go)：数据库候选召回。

### 业务场景

项目制度、发布方案和接口文档会更新。用户希望回答基于最新版资料，同时能指出信息来自哪个文档和原文位置。

### 问题分析

只保存向量会失去原文定位；同名文档重复上传会混入旧内容；ACL 只在列表层过滤会导致检索越权；中文按字节分块会切坏 UTF-8。

### 技术实现

内容使用 SHA-256 去重；同 owner + 文档名形成版本组并维护 `is_latest`。Document 保存 owner、private/public、模型与维度；Chunk 保存原文、序号和 Unicode 字符区间。检索候选从 Store 层就带可信主体过滤，并默认只查最新版。SearchResult 把文档名、分块序号和区间返回给 Agent。

### 为什么这样实现

RAG 不只是“查到文本”，还需要版本治理、权限和引用证据。把 ACL 下推候选层可以避免先越权读取再过滤的风险。

## 7. 双后端混合检索：向量 + BM25/FTS + RRF

### 关键代码位置

- [`internal/knowledge/service.go`](../internal/knowledge/service.go)：相似度、BM25、RRF 和结果排序；
- [`internal/knowledge/tokenizer.go`](../internal/knowledge/tokenizer.go)：中英文轻量分词；
- [`internal/store/postgres/knowledge.go`](../internal/store/postgres/knowledge.go)：pgvector 与 FTS 候选；
- [`internal/store/postgres/schema.go`](../internal/store/postgres/schema.go)：HNSW 和 GIN 索引；
- [`internal/knowledge/embedder.go`](../internal/knowledge/embedder.go)：Hash/OpenAI-compatible Embedding。

### 业务场景

“项目发布日期”适合语义检索，“P0/P1”“错误码 2508”更依赖精确词项。单一路径很难同时覆盖自然语言与专有名词。

### 问题分析

向量召回容易漏掉代码、日期和缩写；关键词检索不理解同义表达。直接加权两种原始分数又会受量纲影响。

### 技术实现

SQLite 模式在进程内精确扫描，计算余弦与 BM25；PostgreSQL 使用 pgvector HNSW 和 `tsvector` GIN 分别取候选。Service 使用 Reciprocal Rank Fusion 按名次融合，并保留各路原始分数用于解释和评测。

### 为什么这样实现

RRF 不要求校准不同分数范围，简单稳定，适合作为 MVP 融合算法。双后端共用 Service 契约，让本地易用性与生产性能可以渐进演进。

## 8. RAG 可回归评测，而不是只看演示答案

### 关键代码位置

- [`internal/rageval/evaluator.go`](../internal/rageval/evaluator.go)：Recall@K、MRR 等检索指标；
- [`internal/rageval/answer.go`](../internal/rageval/answer.go)：事实覆盖、引用覆盖与忠实度；
- [`cmd/zora-eval/main.go`](../cmd/zora-eval/main.go)：隔离评测入口；
- `evals/knowledge.json`：固定语料和问题。

### 业务场景

修改分块、Embedding 或融合参数后，需要知道新版本是否真的更好，而不是用两三个问题凭感觉判断。

### 问题分析

端到端答案会受模型随机性影响，只看检索又无法发现“检索到了但答案没有引用”。若评测复用开发数据库，历史数据还会污染结果。

### 技术实现

评测命令在隔离数据库中重建固定语料，分别运行 vector、keyword、hybrid；同时通过真实 Chat 链路评价答案事实覆盖、有效引用覆盖和引用忠实度。指标未达阈值时 CLI 返回非零状态。

### 为什么这样实现

它把 RAG 优化从 Prompt 调试升级为可重复实验，也能说明检索质量与生成质量需要分层度量。

## 9. 长期记忆不是聊天记录的另一份向量副本

### 关键代码位置

- [`internal/memory/types.go`](../internal/memory/types.go)：Semantic/Episodic、MemoryKey、来源和过期；
- [`internal/memory/extractor.go`](../internal/memory/extractor.go)：规则/模型候选提取；
- [`internal/memory/service.go`](../internal/memory/service.go)：Capture、同 Key 合并和人工编辑保护；
- [`internal/memory/retriever.go`](../internal/memory/retriever.go)：召回评分；
- [`internal/chat/service.go`](../internal/chat/service.go)：回答前注入、成功后提取。

### 业务场景

用户希望 Agent 跨会话记住语言偏好、技术背景和项目经历，但不希望每句话都永久保存或旧事实覆盖人工修正。

### 问题分析

直接把全部聊天向量化会保存噪声和敏感信息；相同事实会产生重复或冲突；模型自动写入可能覆盖用户显式修改。

### 技术实现

Memory 具有 kind、稳定 `memory_key`、importance、source、expiry 和 `user_edited`。回答成功后才提取候选；同 Key 新值执行 Consolidation；人工修改项禁止自动覆盖。召回按主题相关性、重要性和时效性联合评分，并设置最低相关性门槛。注入 Prompt 时明确标记为数据，不能改变系统规则。

### 为什么这样实现

长期记忆是经过治理的用户事实，不是无限历史。独立模型让记忆可以解释、编辑、删除和过期，符合产品控制权和隐私要求。

## 10. 增量摘要与最近原文窗口

### 关键代码位置

- [`internal/summary/service.go`](../internal/summary/service.go)：触发阈值、覆盖序号和增量更新；
- [`internal/summary/summarizer.go`](../internal/summary/summarizer.go)：规则/模型摘要器与敏感信息检查；
- [`internal/summary/types.go`](../internal/summary/types.go)：Summary 模型；
- [`internal/chat/service.go`](../internal/chat/service.go)：摘要 + 最近消息组装。

### 业务场景

长对话需要保留早期约束，又不能无限增加 Token、延迟和费用。

### 问题分析

只截取最近 N 条会丢失早期目标；每轮全量总结成本高且容易累积错误；若摘要失败阻断正常回答，系统可用性会变差。

### 技术实现

当未摘要消息达到阈值时，只把旧窗口合并到已有摘要，并记录 `through_sequence`；最近若干消息始终保留原文。完整 Message 不删除。摘要读取或生成失败时降级为最近消息，回答主链不失败。

### 为什么这样实现

“摘要 + 最近原文”在上下文长度、信息完整性和成本之间取得平衡。覆盖序号避免同一批消息重复总结，也便于审计。

## 11. 多 Agent 是真实 AgentTool，不是多个角色 Prompt 拼接

### 关键代码位置

- [`internal/agentruntime/multi_agent.go`](../internal/agentruntime/multi_agent.go)：Supervisor 与三个 Specialist；
- [`internal/agentruntime/execution_control.go`](../internal/agentruntime/execution_control.go)：交接预算、并行度、超时和重试；
- [`internal/agentruntime/runtime.go`](../internal/agentruntime/runtime.go)：handoff 事件；
- [`internal/chat/service.go`](../internal/chat/service.go)：父子 Run 持久化；
- [`internal/agentseval/evaluator.go`](../internal/agentseval/evaluator.go)：单/多 Agent 对照。

### 业务场景

复合任务可能需要先从文档取证，再起草通知；独立计算和写作任务又可以并行。

### 问题分析

一个全能 Agent 拿到所有工具会扩大权限并增加选错工具概率；多 Agent 若没有预算和隔离，容易递归交接、成本失控；把专家输出直接拼成最终答案还会造成重复和风格不一致。

### 技术实现

Supervisor 只持有三个 AgentTool。Research、Document、Writer 各自拥有最小工具集。依赖任务串行，独立任务受信号量限制并行。每个根 Run 有交接次数、最大并行、专家超时和有限重试；专业 Agent 保存 `AgentTaskRun`，输出只作为 Supervisor 证据，不直接拼接给用户。

### 为什么这样实现

按职责切分既是能力路由，也是权限边界。只有当专业化带来的质量收益超过模型调用与延迟成本时，多 Agent 才值得开启，所以项目默认关闭并提供评测。

## 12. 持久化 Human-in-the-loop 审批

### 关键代码位置

- [`internal/approval/service.go`](../internal/approval/service.go)：风险识别、请求、等待、决定与超时；
- [`internal/chat/service.go`](../internal/chat/service.go)：审批前暂停和终态处理；
- [`internal/httpapi/server.go`](../internal/httpapi/server.go)：审批 API 和 SSE；
- [`internal/store/sqlite/approvals.go`](../internal/store/sqlite/approvals.go) 与 [`internal/store/postgres/approvals.go`](../internal/store/postgres/approvals.go)：持久化 CAS。

### 业务场景

“发送给客户”“发布到生产”“删除数据”等任务不能因为模型判断错误直接继续。

### 问题分析

只在前端弹窗无法防止绕过 API；只用内存 channel 会在进程重启后丢失审批；重复点击可能造成并发状态冲突。

### 技术实现

审批请求先写数据库，再通过 SSE 发出 `approval_required`。Service 等待决定或超时；API 使用状态条件更新，只允许 pending 原子迁移到 approved/rejected/expired。批准后原 Context 内的 Run 恢复，拒绝进入明确 `rejected` 终态。

### 为什么这样实现

审批必须是后端执行闸门，而不是 UI 提示。持久化状态和 CAS 让操作可审计、可抗重复提交，并为未来跨实例恢复预留边界。

## 13. MCP 不是无限插件：白名单、只读校验与凭据隔离

### 关键代码位置

- [`internal/mcpbridge/client.go`](../internal/mcpbridge/client.go)：Server 启动、工具发现、名称空间、白名单和输出限制；
- [`internal/mcpfiles/server.go`](../internal/mcpfiles/server.go)：目录沙箱；
- [`internal/mcpmicrosoft/server.go`](../internal/mcpmicrosoft/server.go)：Graph 邮件/日历工具；
- [`internal/mcpmicrosoft/auth.go`](../internal/mcpmicrosoft/auth.go)：OAuth、Secret 文件和 401 重试；
- [`cmd/zora/main.go`](../cmd/zora/main.go)：`pass_env` 最小透传。

### 业务场景

Agent 需要读取本地资料、邮件和日历，但第三方 MCP Server 不应默认获得所有环境变量、文件和写权限。

### 问题分析

直接注册 Server 返回的全部工具会把供应方能力当成可信；子进程继承父进程环境会暴露模型 Key 和数据库 DSN；文件工具容易发生 `..`、绝对路径或符号链接逃逸；外部正文还可能包含 Prompt Injection。

### 技术实现

工具必须同时命中本地 `allowed_tools` 并声明 `readOnlyHint`；公开名称增加 Server 前缀。子进程只收到 `pass_env`。文件 Server 限定授权根目录并拒绝隐藏路径、越界和符号链接逃逸。Graph 结果只返回摘要和元数据，并附加“不可信外部内容”语义边界。

### 为什么这样实现

MCP 是能力协议，不是信任协议。配置白名单与远端声明双重校验、最小环境继承和目录沙箱共同实现纵深防御。

## 14. 外部副作用四层隔离：草稿 → 确认 → Operation → Executor

### 关键代码位置

- [`internal/office/tool.go`](../internal/office/tool.go)：模型可调用的草稿预览工具；
- [`internal/office/service.go`](../internal/office/service.go)：草稿校验、内容哈希和状态迁移；
- [`internal/office/operation_service.go`](../internal/office/operation_service.go)：Operation、租约、重试和恢复；
- [`internal/office/types.go`](../internal/office/types.go)：Draft、Operation、Executor 契约；
- [`internal/mcpbridge/office_executor.go`](../internal/mcpbridge/office_executor.go)：Microsoft Graph 专用写执行器。

### 业务场景

Agent 可以帮助起草邮件或会议，但真正发送/创建属于高风险、不可轻易撤销的外部副作用。

### 问题分析

模型可能重复 ToolCall、用户可能重复点击、网络可能在远端成功后本地超时、进程可能在执行中退出。简单的“批准后直接调用 Graph”会产生重复邮件或错误成功状态。

### 技术实现

1. Agent 工具只创建不可执行 Draft；
2. 用户对 Draft 做一次性确认；
3. approved Draft 幂等创建唯一 Operation 和稳定 idempotency key；
4. 专用 Executor 领取带租约的任务，返回可核验远端引用后才能 completed。

邮件先创建远端草稿并 Checkpoint 保存不可变 ID，再发送；重试复用该 ID。日程从幂等键派生固定 transaction ID。启动时将租约过期的 executing 恢复为可重试 failed。

### 为什么这样实现

生成内容与产生副作用必须分离。Operation 把短生命周期 HTTP/Agent Run 转换为可恢复业务任务，稳定幂等键和检查点解决“远端是否已经成功”的经典分布式不确定性。

## 15. 可观察性指标来自持久化执行事实

### 关键代码位置

- [`internal/agentruntime/runtime.go`](../internal/agentruntime/runtime.go)：模型调用、首字和 Token Usage 事件；
- [`internal/chat/service.go`](../internal/chat/service.go)：RunEvent 持久化；
- [`internal/observability/metrics.go`](../internal/observability/metrics.go)：指标重建；
- [`internal/httpapi/server.go`](../internal/httpapi/server.go)：Run/Metrics API；
- [`internal/httpapi/web/app.js`](../internal/httpapi/web/app.js)：运行监控面板。

### 业务场景

用户反馈慢、费用异常或工具失败时，需要定位是模型首字慢、工具慢、多 Agent 交接多，还是 Provider 没返回 Usage。

### 问题分析

只打印日志难以按 Run 聚合；只在内存计时无法跨进程查询；根据字符数估算 Token 会把估计值误当账单事实。

### 技术实现

Runtime 记录 model_call_completed、first_token、tool/handoff 事件。`observability.Aggregate` 从 Run 与事件重建总耗时、TTFT、模型/工具/交接次数及耗时。Token 只接受 Provider Usage；部分调用缺失时明确标记 `usage_complete=false`，不静默估算。

### 为什么这样实现

指标必须可解释、可复算。事件事实比散落日志更适合审计和产品展示；诚实标注缺失比伪精确估算更可靠。

## 16. SQLite 与 PostgreSQL 双实现，支持渐进式部署

### 关键代码位置

- [`internal/store/store.go`](../internal/store/store.go)：对话与 Run 契约；
- [`internal/knowledge/types.go`](../internal/knowledge/types.go)、[`internal/memory/types.go`](../internal/memory/types.go)、[`internal/office/types.go`](../internal/office/types.go)：领域 Store 契约；
- [`internal/store/sqlite`](../internal/store/sqlite)：本地实现；
- [`internal/store/postgres`](../internal/store/postgres)：生产实现；
- [`cmd/zora/main.go`](../cmd/zora/main.go)：配置选择。

### 业务场景

个人项目需要开箱即用，生产部署又需要连接池、多实例共享数据和数据库索引。

### 问题分析

一开始强制 PostgreSQL 会增加体验门槛；只实现 SQLite 又无法证明向量索引、FTS 和并发数据库能力。把 SQL 直接写进业务 Service 会导致存储替换困难。

### 技术实现

上层依赖小而专一的 Store 接口。SQLite 使用纯 Go 驱动、事务和精确扫描；PostgreSQL 使用 pgx 连接池、幂等迁移、pgvector HNSW 与 FTS GIN，并在启动时用 advisory lock 保护多实例 DDL。

### 为什么这样实现

这体现了 MVP 与生产演进的现实取舍：本地默认零运维，同时保留可证明的生产数据路径，而不是提前引入所有基础设施。

## 17. Control/Treatment 评测覆盖 Memory 和 Multi-Agent 收益

### 关键代码位置

- [`internal/memoryeval/evaluator.go`](../internal/memoryeval/evaluator.go) 与 [`cmd/zora-memory-eval`](../cmd/zora-memory-eval)：记忆 A/B；
- [`internal/agentseval/evaluator.go`](../internal/agentseval/evaluator.go) 与 [`cmd/zora-agent-eval`](../cmd/zora-agent-eval)：单/多 Agent 对照；
- `evals/`：版本化样本与阈值。

### 业务场景

Memory 和 Multi-Agent 都会增加复杂度、延迟和模型费用，需要证明它们在具体数据集上带来质量提升。

### 问题分析

只测召回函数无法发现上下文注入污染；只看多 Agent 路由准确率无法判断最终答案是否更好；两组请求若走不同业务链路，实验结论不可信。

### 技术实现

同一问题分别通过关闭/开启能力的完整 Chat 链路。Memory 测量预期召回、错误注入和答案事实覆盖；Multi-Agent 测量路由、最终质量、模型调用代理和延迟比例。固定数据中包含硬负例，并通过阈值门禁返回进程状态。

### 为什么这样实现

Agent 架构不是越复杂越高级。Control/Treatment 迫使开发者用收益、成本和错误率证明设计，而不是只展示功能存在。

## 18. 单二进制 Web 与安全的流式 Markdown 渲染

### 关键代码位置

- [`internal/httpapi/server.go`](../internal/httpapi/server.go)：嵌入静态资源与 SPA；
- [`internal/httpapi/web/index.html`](../internal/httpapi/web/index.html)：页面结构；
- [`internal/httpapi/web/app.js`](../internal/httpapi/web/app.js)：SSE、Markdown、帧合并与交互；
- [`internal/httpapi/web/styles.css`](../internal/httpapi/web/styles.css)：响应式布局和消息样式。

### 业务场景

项目需要可演示的 UI，但不希望为了个人 Agent 项目再维护 Node 构建链和独立前端部署。

### 问题分析

完全使用 `textContent` 无法展示模型 Markdown；直接把模型 HTML 写入 `innerHTML` 会产生 XSS。真实模型 token 密集时，整页重绘还会导致闪烁和滚动抖动。

### 技术实现

静态资源嵌入 Go 二进制。Markdown 渲染器先 HTML 转义，再有限支持标题、列表、引用、强调和代码块。流式内容按动画帧合并，只刷新当前回答，生成阶段关闭重复平滑滚动。

### 为什么这样实现

单二进制降低部署复杂度；有限 Markdown 比允许原始 HTML 更安全。该方案适合当前 MVP，但如果未来支持复杂表格、链接策略和插件，应切换到经过安全审计的 Markdown 库。

## 19. HTTP、Run、模型、工具与 Embedding 的同链路观测

### 关键代码位置

- [`internal/observability/telemetry.go`](../internal/observability/telemetry.go)：Tracer/Meter Provider、OTLP Exporter、Prometheus Registry 和指标定义；
- [`internal/observability/spans.go`](../internal/observability/spans.go)：HTTP、Run、模型、工具和 Embedding Span/Metric 生命周期；
- [`internal/observability/model.go`](../internal/observability/model.go)：Generate/Stream 模型装饰器与真实 Usage；
- [`internal/observability/tool.go`](../internal/observability/tool.go) 与 [`embedding.go`](../internal/observability/embedding.go)：工具和向量模型装饰器；
- [`internal/chat/service.go`](../internal/chat/service.go)：业务 Run 与 Trace ID 关联；
- [`internal/httpapi/server.go`](../internal/httpapi/server.go)：W3C 上下文提取、HTTP Server Span 和 `/metrics`；
- [`internal/observability/telemetry_test.go`](../internal/observability/telemetry_test.go)：跨五层父子关系与指标导出测试。

### 业务场景

一次 RAG Agent 回答可能慢在 HTTP 排队、模型首轮决策、知识库工具、Embedding 查询或第二轮生成。只知道整轮耗时无法定位是哪一层，也无法按模型、工具或错误类型建立趋势和告警。

### 问题分析

如果各模块各自创建根 Span，Jaeger 里会出现互不关联的碎片；如果把 Prompt、工具参数或 Run ID 放进 Metric Label，会造成敏感数据泄露或高基数时序爆炸；如果只保留 Trace，又会因为采样和 TTL 丢失产品审计事实。流式模型还有额外陷阱：拿到 Reader 不代表调用结束，过早结束 Span 会得到错误耗时。

### 技术实现

入口从 HTTP Header 提取 TraceContext，Chat 在创建持久化 Run 后创建子 Span，模型、Tool 和 Embedder 都通过装饰器复用 Context。模型 Stream 的代理 Reader 在 EOF、错误或消费者关闭时才结束 Span。SSE `start` 和 `run_started` 保存 Trace/Span ID，实现业务 Run 到 Jaeger 的定位。Prometheus 使用独立 Registry，指标只按有限 method、route、status、provider、model、tool 等维度聚合，`/metrics` 不进入业务中间件。RunEvent 继续作为不可采样的业务审计，OTel 只负责基础设施时序。

### 为什么这样实现

装饰器让可观察性与 Eino/业务逻辑解耦，也覆盖内置、MCP、办公和专业 Agent Tool。产品审计与基础设施 Trace 分层后，既能保证用户侧事实完整，又能按采样和保留成本运维 Trace。低基数 Metric 能稳定用于 SLO；敏感正文不出业务边界。当前开发环境可直连 Jaeger，生产再增加 Collector、尾采样和多后端，不需要修改业务调用链。

## 20. Memory Capture 使用事务 Outbox，而不是请求尾部 goroutine

### 关键代码位置

- [`internal/memory/capture_queue.go`](../internal/memory/capture_queue.go)：入队、幂等返回和 Worker Wake；
- [`internal/memory/capture_worker.go`](../internal/memory/capture_worker.go)：任务超时、租约、指数退避、恢复与优雅停止；
- [`internal/memory/types.go`](../internal/memory/types.go)：CaptureJob 状态与 Store 契约；
- [`internal/store/sqlite/memory_capture_jobs.go`](../internal/store/sqlite/memory_capture_jobs.go)：本地事务实现；
- [`internal/store/postgres/memory_capture_jobs.go`](../internal/store/postgres/memory_capture_jobs.go)：`FOR UPDATE SKIP LOCKED` 多实例领取；
- [`internal/chat/service.go`](../internal/chat/service.go)：回答保存、任务入队与 SSE `memory_job`；
- [`internal/observability/spans.go`](../internal/observability/spans.go)：异步 TraceContext 续接和 Worker 指标。

### 业务场景

模型完成回答后，系统还要提取用户长期偏好。如果继续在 HTTP 请求中调用第二次模型，用户必须等待额外尾延迟；如果简单启动 goroutine，发布重启或进程异常会静默丢任务。

### 问题分析

异步化不是把代码放进 goroutine 就结束。必须回答：回答已经落库但任务没创建怎么办、客户端重试会不会产生重复任务、两个实例会不会同时处理、模型限流如何退避、Worker 崩溃后谁回收、后台调用如何关联原 Trace，以及是否为了排队又复制一份用户正文。

### 技术实现

Store 在同一个事务中保存 assistant Message 和 pending CaptureJob；`run_id` 唯一吸收入队重试。Job 只引用用户/助手消息 ID，Worker 领取后再读取正文。领取会原子写入 worker ID、lease deadline 和 attempt；PostgreSQL 用 `SKIP LOCKED` 隔离并发消费者。失败且仍有预算时回到 pending，`available_at` 使用指数退避；租约过期可再次领取，最大尝试耗尽进入 failed。SSE 先返回 pending Job，REST、RunEvent、Prometheus 和 `memory.capture` Span 提供后续状态。进程内 Channel 只是低延迟通知，数据库轮询负责可靠性。

### 为什么这样实现

对当前个人项目，数据库 Outbox 比引入 Kafka/RabbitMQ 更容易部署，却能展示事务一致性、幂等、租约和恢复这些真正的后端能力。消息正文不复制可降低隐私面和存储膨胀；Job Store 契约又保留了未来拆独立 Worker 或接消息中间件的空间。当前明确保留的边界是：不同 Job 在多实例下同时更新相同 `memory_key` 仍需唯一约束与冲突重试。

## 如何向面试官总结这些亮点

不要一次背完 20 项。建议根据岗位选择三条主线：

### Agent 后端岗位

1. Eino 框架边界与 ReAct 事件适配；
2. Message/Run/Event 领域模型与 SSE/Context；
3. 多 Agent 权限隔离、预算和 Control/Treatment 评测。

### RAG/知识库岗位

1. 文档版本、ACL、Unicode 分块和引用；
2. pgvector + FTS + RRF；
3. 检索指标与答案引用忠实度分层评测。

### Agent 平台/可靠性岗位

1. RunEvent 产品审计 + OTel/Prometheus 基础设施观测；
2. Memory Capture Outbox 的事务、租约、退避和恢复；
3. Human-in-the-loop 与 Office Operation 的幂等、检查点和副作用边界。

### 两年 Go 经验最适合强调的部分

- Context 取消传播与会话级并发控制；
- Store 接口和 SQLite/PostgreSQL 双实现；
- 状态机、CAS、事务、幂等键与租约；
- SSE、HTTP API、结构化错误和优雅关闭；
- 使用固定测试和评测门禁控制 Agent 的非确定性。

这些内容能把项目从“调用过大模型 API”提升为“用 Go 实现过可运行、可治理的 Agent 系统”。
