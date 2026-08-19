# 阿里云轻量服务器 staging 部署指南

本文描述成本优先的单机 staging 方案。它用于尽快验证真实域名、HTTPS、GitHub 登录、PostgreSQL 向量检索、Redis 会话和 DeepSeek 调用，不等价于生产多副本架构。

## 1. 每项资源的作用

| 资源 | 作用 | 当前低成本方案 |
|---|---|---|
| 域名 | 给浏览器、HTTPS 证书和 GitHub OAuth 回调提供稳定地址 | `zora-agent.top` |
| 轻量应用服务器 | 运行 Docker 和全部容器 | 一台境外或香港 Linux 实例 |
| ACR | 保存已经构建好的 Zora 镜像，避免在小内存服务器上编译 Go | 中国香港个人版私有仓库 `zora-agent/zora` |
| Caddy | 对外监听 80/443，自动申请证书并反向代理 Zora | Docker 容器 |
| PostgreSQL + pgvector | 保存用户、对话、知识块、向量、记忆、任务和配额 | 同机私有容器，不暴露公网端口 |
| Redis | 保存 GitHub 会话、OAuth state 和分布式限流状态 | 同机私有容器，不暴露公网端口 |
| GitHub OAuth App | 让用户使用 GitHub 身份登录，并生成稳定租户 ID | staging 独立创建一个 App |
| 费用预算 | 接近预算时提醒，避免意外账单 | 月度 50 元告警；不是自动停机 |

## 2. 为什么先使用单机 staging

ACK、ALB、RDS 和 Tair 可以实现真正的跨节点多副本和托管高可用，但固定成本明显高于个人学习项目预算。单机 staging 仍然保留真实多用户数据隔离、PostgreSQL、Redis、HTTPS 和 OAuth 流程，可以完成大部分功能验收；它缺少的是主机故障容灾、滚动升级和托管数据库备份。

## 3. 部署顺序

1. 注册域名并完成实名认证；
2. 购买轻量服务器，开放 22、80、443，SSH 仅允许自己的公网 IP；
3. 安装 Docker Engine 与 Compose 插件，并创建 1–2 GiB Swap；
4. 使用已创建的 ACR 私有仓库 `crpi-bn9tjq61qbvw8e3l.cn-hongkong.personal.cr.aliyuncs.com/zora-agent/zora`，本地构建并推送不可变版本标签；
5. 把 `deploy/aliyun/swas` 上传到服务器的 `/opt/zora`；
6. 复制 `.env.staging.example` 为 `.env.staging`，替换域名、邮箱、镜像和 GitHub Client ID；
7. 把 `ai-config.template.json` 复制到仓库外的临时位置，替换 LLM 与 Embedding 参数，再运行 `sudo ./install-ai-config.sh /临时路径/ai-config.json`；
8. 运行 `prepare-secrets.sh`，在终端静默输入 GitHub、LLM 和 Embedding 三个外部 Secret；
9. 将域名 A 记录指向服务器公网 IPv4；
10. 运行 `sudo ./deploy.sh`，Caddy 自动申请 HTTPS 证书；
11. 创建或核对 GitHub OAuth App 回调，然后执行 staging 验收脚本。

## 4. 为什么服务器至少建议 2 GiB 内存

一台服务器同时运行系统、Docker、Zora、PostgreSQL、Redis 和 Caddy。1 GiB 可以通过严格内存限制和 Swap 勉强启动，但文档摄取、向量索引或多 Agent 并发时容易触发 OOM；2 GiB 是更合理的 staging 下限。部署文件已把各容器内存限制在较低范围，并关闭本机 Jaeger/Prometheus 容器以节省内存。

## 5. Secret 管理

`.env.staging` 只保存域名、镜像和限流等非敏感配置。LLM/Embedding 元数据保存在 `.runtime/ai-config.json`，真实 Key、数据库密码、Redis 密码、GitHub Client Secret 和 metrics token 保存在 `.secrets`。两类文件都以 `0400` 只读挂载给固定 UID 10001，并已加入 Git 和 Docker 构建忽略规则。

`ai-config.json` 只能包含模型元数据和 `api_key_env` 引用，不能包含 `api_key`。修改 LLM 配置后执行 `docker compose ... restart zora` 生效；修改 Embedding 模型或维度前必须先规划知识、消息和长期记忆向量的重建，不能直接覆盖旧索引。

服务器仍应做到：禁用 SSH 密码登录、只用密钥；限制 22 端口来源；定期轮换外部 Key；不要把 Secret 粘贴到聊天、工单、截图或 Shell 历史中。

## 6. 验收与已知限制

部署后先执行：

```bash
BASE_URL=https://zora-agent.top ./scripts/verify-staging.sh
```

随后人工验证 GitHub 登录、两个账号数据隔离、真实 Embedding 检索、长回答 SSE、知识库异步摄取、429 限流和数据库恢复。

单机方案的已知限制：应用只有一个副本；PostgreSQL/Redis 与应用共享故障域；升级存在短暂中断；本机磁盘损坏会影响数据。完成求职演示与功能验证后，再按生产 Runbook 迁移到 ACK + RDS + Tair。
