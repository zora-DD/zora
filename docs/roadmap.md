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

## V0.3 Long-term Memory

- [ ] 短期历史压缩与摘要
- [x] Semantic / Episodic Memory Schema（来源、重要性、可选过期时间）
- [x] 记忆候选提取和 Consolidation（真实模型结构化提取 + 本地确定性规则）
- [x] Memory Key 去重、冲突更新、人工修正保护和过期过滤
- [x] 相关性 + 时效性 + 重要性召回与安全上下文注入
- [x] 用户创建、查看、编辑、删除记忆（REST API + Web 面板）
- [ ] 有/无记忆 A/B 评估

验收条件：长期记忆不是历史消息向量库；每条记忆可解释来源并可由用户控制。

当前验证结果：已建立独立 `memories` 表和 `memory.Service`，区分 semantic/episodic，保存稳定 `memory_key`、来源会话/消息、重要性、人工修正标记和可选过期时间；SQLite 与 PostgreSQL 保持相同 Store 契约。回答成功后，真实模型使用抗指令注入的中文结构化 Prompt 提取候选，本地 Mock 使用保守规则；Service 按 Kind + Memory Key 创建、跳过重复或更新冲突，并拒绝覆盖人工修正。新请求执行前以词项相关性 65% + 重要性 20% + 90 天半衰期时效性 15% 联合排序，过门槛的 Top-K 记忆以不可信 JSON 数据注入独立 System Message，总正文上限 6,000 字符；本轮输入冲突时优先本轮。提取和召回失败均不影响正常回答，RunEvent 只记录 ID 和分数组件。尚未完成短期摘要和有/无记忆 A/B 质量门禁，因此 V0.3 仍在进行中。

## V0.4 Multi-Agent

- [ ] Supervisor + Research / Document / Writer Agents
- [ ] 子 Agent 上下文隔离和结构化交接
- [ ] 并行任务、预算、超时、重试和取消
- [ ] 父子 Run 链路与可视化
- [ ] Human-in-the-loop 审批节点
- [ ] 单 Agent / 多 Agent 的质量、成本和耗时对比

验收条件：至少一个基准任务能证明多 Agent 带来可量化收益，否则保持单 Agent。

## V0.5 Office Agent

- [ ] 官方 MCP Go SDK
- [ ] 文件、邮件、日历只读连接器
- [ ] 草稿预览
- [ ] 写操作人工确认
- [ ] 凭据隔离、最小权限和完整审计

## 每个版本的文档完成标准

- [x] README 的能力矩阵、配置和使用方法与代码一致
- [x] 项目分析文档同步业务模型、数据模型、亮点和风险
- [x] 项目技术文档同步流程、API、事件与设计方案
- [x] 新增配置写入 `.env.example`
- [x] 新增或修改 API 时提供请求、响应和错误示例
- [x] 规划能力与已实现能力明确区分
- [x] 验收指标和实际验证结果写回对应里程碑
