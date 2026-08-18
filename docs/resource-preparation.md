# Zora 调试与上线资源清单

> 更新日期：2026-08-18。模型名称、价格和免费额度会变化，使用前以供应商控制台为准。

## 1. 当前本地调试的最低资源

Zora 默认使用 SQLite、Hash Embedding 和本地 Mock，因此只需要 Go 环境即可启动。要验证真实 Agent 生成质量，最低只需一项外部资源：一个支持 OpenAI-compatible Tool Calling 的聊天模型 API Key。

DeepSeek 当前提供 `deepseek-v4-flash` 和 `deepseek-v4-pro`，OpenAI 格式 BaseURL 为 `https://api.deepseek.com`，两者均支持 Tool Calls。官方价格页按百万 Token 计费，实际费用从赠送余额或充值余额中扣除：[DeepSeek Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing)。

DeepSeek V4 默认开启思考模式。思考模式发生工具调用时，后续请求必须回传 `reasoning_content`；当前 Zora 配置示例用 `extra_fields.thinking.type=disabled` 关闭思考，先保证 Eino ReAct 链路稳定：[DeepSeek 思考模式](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode)。

## 2. 资源缺口与是否必须

| 资源 | 当前是否必须 | 用途 | 获取方式 | 费用判断 |
|---|---|---|---|---|
| DeepSeek 新 API Key 与余额 | 真实模型调试必须 | 对话、工具选择、记忆提取、摘要、多 Agent | 删除已暴露 Key，在 DeepSeek 控制台创建新 Key，并查询 `/models` 确认账号可用模型 | 按 Token 计费；赠送余额可先使用 |
| 真实 Embedding | 真实语义 RAG 建议 | 文档和查询向量化 | 开通阿里云百炼，创建 API Key，使用 `text-embedding-v4` 的 OpenAI 兼容接口 | 华北 2 原价约 0.5 元/百万输入 Token；免费额度和有效期以控制台为准 |
| PostgreSQL + pgvector | 非必须 | 大数据量、HNSW/FTS、生产多实例 | 本机 Docker Compose，或购买托管 PostgreSQL | 本机开源免费；云数据库按实例收费 |
| Reranker | 当前不需要 | 对召回候选进行二次语义排序 | 后续按评测结果接入 API 或本地模型 | 取决于供应商；没有指标前不建议先购买 |
| Microsoft 365 测试租户 | 当前延期 | 邮件/日历真实读写和最小权限验收 | 后续准备 Entra 应用、测试邮箱和管理员授权 | 可能需要试用或许可证；不影响当前版本调试 |
| Redis/消息队列 | 当前不需要 | 分布式锁、异步任务 | 真正进入多实例或异步 Ingestion 时再引入 | 本地开源免费；托管服务收费 |
| 对象存储 | 当前不需要 | 大文件原文、附件存储 | 当前文档直接进入 SQLite/PostgreSQL；规模化后再接 OSS/S3 | 本地不需要；云存储按量收费 |
| 外部 Trace 平台 | 当前不需要 | 跨服务链路、聚合告警 | 当前先使用 RunEvent/RunMetrics；后续可接 OpenTelemetry/Langfuse | 自托管可免费；云版通常有套餐 |

DeepSeek 官方 API 参考当前列出了 Chat Completion、模型列表等接口，没有可供 Zora 直接使用的 Embedding 模型，因此不要假设同一把 DeepSeek Key 能生成向量。阿里云 `text-embedding-v4` 支持 64–2048 维，Zora 示例使用 1024 维：[模型说明](https://help.aliyun.com/zh/model-studio/text-embedding-v4)、[OpenAI 兼容 Embedding](https://help.aliyun.com/zh/model-studio/embedding-interfaces-compatible-with-openai)。

## 3. 推荐获取顺序

1. 立即轮换已经暴露的 DeepSeek Key，并只在本机环境变量中保存新 Key。
2. 先使用 DeepSeek + Hash Embedding 调通真实工具调用、多模型、记忆和摘要，控制变量并节约成本。
3. 再开通百炼 Embedding，用新的 `ZORA_DATA_DIR` 重新上传测试文档，对比 Hash 与真实向量召回。
4. 数据量和并发明显增加后再启动 PostgreSQL；本地功能调试不需要先购买云数据库。
5. Microsoft 365、Reranker、队列和外部 Trace 都等待对应验收目标出现后再投入。

## 4. 安全要求

- API Key 不写入 `ZORA_MODELS_JSON`，只填写 `api_key_env`；
- 不把真实 Key 写入 `.env.example`、README、测试文档、聊天截图或 Git；
- 本地可通过 IDE 环境变量或当前 Shell 注入；生产使用 Secret 文件或部署平台 Secret；
- 一旦 Key 出现在聊天、日志或截图中，立即删除并重新创建，不再继续使用。
