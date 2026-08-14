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

## V0.2 Knowledge Base — 下一里程碑

- [ ] PostgreSQL + pgvector Store
- [ ] 文档上传、哈希去重和版本控制
- [ ] TXT、Markdown、PDF 基础解析
- [ ] 递归/语义切块策略
- [ ] Embedding Provider 抽象
- [ ] pgvector 向量检索 + PostgreSQL FTS
- [ ] RRF 混合召回
- [ ] 回答引用与证据查看
- [ ] 文档级权限过滤
- [ ] 固定问答集：Recall@K、MRR、答案忠实度

验收条件：每个知识库答案能够定位到原文；能用固定数据证明混合召回优于单一路径。

## V0.3 Long-term Memory

- [ ] 短期历史压缩与摘要
- [ ] Semantic / Episodic Memory Schema
- [ ] 记忆候选提取和 Consolidation
- [ ] 去重、更新、冲突和过期策略
- [ ] 相关性 + 时效性 + 重要性召回
- [ ] 用户查看、编辑、删除记忆
- [ ] 有/无记忆 A/B 评估

验收条件：长期记忆不是历史消息向量库；每条记忆可解释来源并可由用户控制。

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

- [ ] README 的能力矩阵、配置和使用方法与代码一致
- [ ] 项目分析文档同步业务模型、数据模型、亮点和风险
- [ ] 项目技术文档同步流程、API、事件与设计方案
- [ ] 新增配置写入 `.env.example`
- [ ] 新增或修改 API 时提供请求、响应和错误示例
- [ ] 规划能力与已实现能力明确区分
- [ ] 验收指标和实际验证结果写回对应里程碑
