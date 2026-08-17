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

## V0.2 Knowledge Base — 进行中

- [x] PostgreSQL + pgvector Store（完整会话/Run/知识库持久化）
- [x] 文档上传和 SHA-256 内容去重
- [ ] 文档版本控制
- [x] TXT、Markdown 基础解析
- [ ] PDF 基础解析
- [x] Unicode 边界感知的重叠分块
- [ ] 递归/语义切块策略
- [x] Embedding Provider 抽象（本地 Hash + OpenAI-compatible）
- [x] SQLite 精确向量扫描 + BM25（最多 10,000 chunks）
- [x] pgvector HNSW 向量检索 + PostgreSQL FTS/GIN 候选召回
- [x] RRF 混合召回
- [x] 结构化引用坐标与 Agent Tool 证据查看
- [ ] 文档级权限过滤
- [x] 固定检索评测集：Recall@K、MRR、命中率和单路/混合对比
- [x] 确定性答案事实覆盖、有效引用覆盖与引用忠实度评估

验收条件：每个知识库答案能够定位到原文；能用固定数据证明混合召回优于单一路径。

当前验证结果：上传 → 分块 → Embedding → 向量/关键词 → RRF → `knowledge_search` → 对话 SSE 的纵向链路已打通。SQLite 采用进程内精确扫描；PostgreSQL 实现完整 Store、pgvector HNSW、`tsvector`/GIN、维度校验、迁移锁和数据库候选下推。自动化测试已覆盖 Store 契约、候选融合和评测指标；真实 PostgreSQL 生命周期测试可通过 `make test-postgres` 执行，但本次开发环境没有 Docker，容器验收尚未实际运行。`zora-rag-smoke-v1` 在默认 Hash Embedding 下得到 Recall@3=1、MRR=1，事实覆盖率/有效引用覆盖率/引用忠实度均为 1；但三种检索模式仍然打平，确定性锚点评测也不能替代真实模型语义评审，因此仍需真实语义样本和 ACL，V0.2 暂不标记完成。

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

## V0.5 Office Agent — 第一阶段已完成

- [x] 官方 MCP Go SDK
- [x] 文件只读连接器
- [ ] 邮件只读连接器
- [ ] 日历只读连接器
- [ ] 草稿预览
- [ ] 写操作人工确认
- [ ] 凭据隔离、最小权限和完整审计

当前验证结果：已固定官方 `github.com/modelcontextprotocol/go-sdk v1.7.0`，Zora 通过 stdio 启动 MCP 子进程、完成协议握手和分页工具发现，再把 JSON Schema 转为 Eino Tool。只有同时进入本地 `allowed_tools` 且声明 `readOnlyHint` 的工具会被注册，公开名称增加 `mcp_{server}_` 前缀；每次调用受独立超时与 12,000 字符默认输出上限约束，工具调用/结果沿用 RunEvent 审计。内置 `zora-mcp-files` 仅支持文件列表和 UTF-8 文本读取，授权根目录在子进程内强制校验，拒绝绝对路径、`..`、隐藏路径、符号链接逃逸、二进制及超过 2 MiB 的文件。子进程使用非继承环境，只透传配置中的 `pass_env`，并拒绝主模型 Key、Embedding Key 和数据库 DSN。in-memory MCP 端到端测试与真实 stdio 冒烟均通过。邮件/日历、专用 OAuth 凭据、草稿和写操作状态机尚未实现，因此 V0.5 仍在进行中。

## 每个版本的文档完成标准

- [x] README 的能力矩阵、配置和使用方法与代码一致
- [x] 项目分析文档同步业务模型、数据模型、亮点和风险
- [x] 项目技术文档同步流程、API、事件与设计方案
- [x] 新增配置写入 `.env.example`
- [x] 新增或修改 API 时提供请求、响应和错误示例
- [x] 规划能力与已实现能力明确区分
- [x] 验收指标和实际验证结果写回对应里程碑
