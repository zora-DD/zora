# 第一阶段：真实 Embedding 与 RAG 基线

本阶段目标是把 Hash Embedding 替换为真实语义向量，并使用固定领域数据记录 vector、keyword、hybrid 三路检索指标。聊天模型效果与向量检索效果分开评估，避免一次实验同时改变太多变量。

## 1. 配置基线

本地 `.env.local` 应包含：

```bash
ZORA_EMBEDDING_PROVIDER=openai
ZORA_EMBEDDING_MODEL=text-embedding-v4
ZORA_EMBEDDING_API_KEY='仅保存在本机的百炼 Key'
ZORA_EMBEDDING_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
ZORA_EMBEDDING_DIMENSIONS=1024
```

真实 Key 不得写入本文、`.env.example`、评测集、日志或 Git。

## 2. 索引隔离

Hash 384 维与 `text-embedding-v4` 1024 维不能混用。建议使用新的 SQLite 目录：

```bash
ZORA_DATA_DIR=./data/embedding-v4
```

如果使用 PostgreSQL，需要新建 1024 维数据库或迁移并重建 `knowledge_chunks`。服务会拒绝把不同向量维度静默写入同一索引。

## 3. 手工无历史污染测试

同一会话中的第二个问题可能直接使用上一轮答案，不一定再次检索。验证真实向量时应：

1. 新建对话；
2. 不在问题中复用文档原词；
3. 确认回答出现 `knowledge_search` 工具轨迹；
4. 确认回答引用正确文档。

推荐问题：

```text
根据我上传的资料，系统计划在什么时候开始正式向使用者提供服务？
```

若界面显示“工具 0”，该答案可能来自模型常识或对话历史，不能作为向量召回通过证据。

## 4. 64 题检索评测

评测集位于 [`evals/knowledge-domain.json`](../evals/knowledge-domain.json)，包含 10 份隔离文档和 64 个问题：

| ID 前缀 | 数量 | 主题 |
|---|---:|---|
| overview | 6 | 项目范围、代号、职责 |
| baseline | 10 | 日期、窗口、区域和四项门禁 |
| capacity | 8 | QPS、延迟、预算和压测 |
| rollback | 8 | 灰度、回滚阈值、RTO/RPO |
| security | 8 | 日志、隐私和数据库变更 |
| timeline | 6 | 发布日时间线 |
| notice | 5 | 通知内容和表达约束 |
| scope | 5 | 非本次范围和缺失信息 |
| conflict | 4 | 版本权威性与历史冲突 |
| injection | 4 | Prompt Injection 不可信内容 |

运行：

```bash
make eval-rag-real
```

该命令会：

- 自动加载 `.env.local`；
- 在临时 SQLite 中重新摄取固定文档；
- 使用真实 Embedding 分别运行 vector、keyword、hybrid；
- 不调用 DeepSeek 等聊天模型；
- 输出 Recall@3、MRR、Hit Rate 和平均检索延迟摘要；
- 评测结束后自动删除临时数据库。

需要排查具体失败题目时，执行明细模式：

```bash
set -a
source .env.local
set +a
go run ./cmd/zora-eval -dataset ./evals/knowledge-domain.json -answers=false -details=true
```

如果希望保存报告：

```bash
mkdir -p ./data/evals
make eval-rag-real > ./data/evals/rag-real-latest.json
```

`data/` 已被 Git 忽略，报告不会误提交。

## 5. 指标定义

| 指标 | 含义 | 第一阶段门槛 |
|---|---|---:|
| Recall@3 | 前 3 个结果覆盖了多少相关文档 | ≥ 0.90 |
| MRR | 第一个相关文档排名的倒数平均值 | ≥ 0.80 |
| Hit Rate | 至少命中一个相关文档的问题比例 | 观察值 |
| Average Latency | 单次检索平均耗时 | 记录基线，不先设硬门槛 |

本阶段重点比较：

- `vector` 是否能处理语义改写；
- `keyword` 是否擅长日期、P0/P1、QPS、RTO 等精确词；
- `hybrid` 是否在总体 Recall 和 MRR 上保持稳定；
- 哪类问题失败，而不只看总平均值。

## 6. 真实基线结果（2026-08-18）

| 项目 | 值 |
|---|---|
| 日期 | 2026-08-18 |
| Embedding Provider | openai |
| Embedding Model | text-embedding-v4 |
| Dimensions | 1024 |
| Chunk Size / Overlap | 800 / 120 |
| Vector Recall@3 / MRR / Hit Rate | 0.911458 / 0.828125 / 0.937500 |
| Keyword Recall@3 / MRR / Hit Rate | 0.963542 / 0.945313 / 0.984375 |
| Hybrid Recall@3 / MRR / Hit Rate | 0.934896 / 0.914063 / 0.953125 |
| Vector / Keyword / Hybrid 平均延迟 | 199.878734 / 1.850875 / 197.109656 ms |
| Hybrid 相对 Vector | Recall +0.023438，MRR +0.085938 |
| Hybrid 相对 Keyword | Recall -0.028646，MRR -0.031250 |
| 评测结论 | `passed=true`，Hybrid 达到 Recall@3 ≥ 0.90、MRR ≥ 0.80 的阶段门槛 |

本次结果证明真实语义向量链路已经接通，而且 Hybrid 能明显改善纯 Vector 的首个相关文档排序；但不能据此宣称 Hybrid 已优于所有单路检索。固定语料包含较多日期、代号、QPS、P0/P1、RTO/RPO 等精确词，因此 Keyword 在当前数据集上最好，Hybrid 的融合权重或候选池仍有优化空间。

紧凑模式未输出 Case 明细。由 Hit Rate 可推算：Vector、Keyword、Hybrid 分别有 4、1、3 道题完全未命中相关文档；具体 Case ID 和失败原因仍需通过 `-details=true` 报告确认，不能只根据聚合指标猜测。

生成并保存明细报告：

```bash
mkdir -p ./data/evals
set -a
source .env.local
set +a
go run ./cmd/zora-eval \
  -dataset ./evals/knowledge-domain.json \
  -answers=false \
  -details=true > ./data/evals/rag-real-details.json
```

保存明细后，可以用 `jq` 只查看未完全召回的题目：

```bash
jq '.modes[] | {mode, failed_cases: [.cases[] | select(.recall_at_k < 1)]}' \
  ./data/evals/rag-real-details.json
```

## 7. 小规模答案级评测

检索基线通过后，再运行原有小数据集：

```bash
set -a
source .env.local
set +a
go run ./cmd/zora-eval -dataset ./evals/knowledge.json -answers=true
```

这一步会调用聊天模型，评估事实覆盖、引用覆盖和引用忠实度。不要直接对 64 题全部运行答案评测，否则会产生更多模型调用和费用。

## 8. 完成标准

- 服务日志显示 `向量提供方=openai`、`向量模型=text-embedding-v4`；
- 使用全新索引重新上传文档；
- 新对话语义改写问题触发 `knowledge_search` 并回答正确；
- 64 题真实 Embedding 检索报告成功生成；
- Hybrid Recall@3 ≥ 0.90、MRR ≥ 0.80，或已对未达标 Case 给出具体原因；
- 保存一份不含 API Key 的指标记录。
