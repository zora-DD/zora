# Microsoft Entra 最小权限部署与真实租户验收

本文是 Zora V0.5 的生产化 Runbook。目标是让只读 Agent 连接器与审批后写执行器使用两个独立的服务主体、两个独立 Secret 和不同权限范围，避免“为了发一封邮件，让普通对话进程拥有整个租户写权限”。

## 1. 权限边界

建议注册两个单租户 Entra 应用：

| 应用 | Zora 进程前缀 | Exchange Application Role | 子进程能力 |
|---|---|---|---|
| `zora-reader` | `ZORA_MCP_MICROSOFT_` | `Application Mail.Read`、`Application Calendars.Read` | 只注册 4 个只读 MCP 工具 |
| `zora-office-writer` | `ZORA_OFFICE_MICROSOFT_` | `Application Mail.ReadWrite`、`Application Mail.Send`、`Application Calendars.ReadWrite` | 显式开启后注册 4 个只读 + 4 个执行协议工具 |

邮件创建/状态恢复需要 `Mail.ReadWrite`，发送还需要独立的 `Mail.Send`；创建日程需要 `Calendars.ReadWrite`。不要授予 `Directory.Read.All`、`User.Read.All` 或 Exchange Full Access，当前实现不需要这些权限。

应用身份没有登录用户，不能访问 `/me`。两个前缀的 `USER_ID` 都必须是专用测试邮箱的 SMTP 地址或 Graph user ID。

## 2. 使用 Exchange Online Application RBAC 收敛邮箱

Microsoft 推荐使用 Exchange Online 的 RBAC for Applications 将应用角色绑定到资源范围。以下命令是管理员操作模板，不应由 Zora 自动执行：

```powershell
# 使用 Enterprise applications 页面中的 Service Principal Object ID，
# 不要误用 App registrations 页面的另一个 Object ID。
New-ServicePrincipal `
  -AppId <APPLICATION_CLIENT_ID> `
  -ObjectId <SERVICE_PRINCIPAL_OBJECT_ID> `
  -DisplayName "zora-reader"

# 示例用专门的 CustomAttribute1 标记测试邮箱；也可以改用 Administrative Unit。
New-ManagementScope `
  -Name "ZoraTestMailboxScope" `
  -RecipientRestrictionFilter "CustomAttribute1 -eq 'ZoraTest'"

New-ManagementRoleAssignment `
  -Name "ZoraReaderMail" `
  -App <SERVICE_PRINCIPAL_OBJECT_ID> `
  -Role "Application Mail.Read" `
  -CustomResourceScope "ZoraTestMailboxScope"

New-ManagementRoleAssignment `
  -Name "ZoraReaderCalendar" `
  -App <SERVICE_PRINCIPAL_OBJECT_ID> `
  -Role "Application Calendars.Read" `
  -CustomResourceScope "ZoraTestMailboxScope"

Test-ServicePrincipalAuthorization `
  -Identity <SERVICE_PRINCIPAL_OBJECT_ID> `
  -Resource <TEST_MAILBOX>
```

对 writer 服务主体重复创建 `Mail.ReadWrite`、`Mail.Send` 和 `Calendars.ReadWrite` 三个受范围约束的 assignment。

Exchange RBAC 与 Entra 中的组织级 Graph Application Permissions 是相加关系。采用上述资源级 RBAC 后，应移除同名的 Entra 组织级授权，否则组织级授权会绕过邮箱范围。权限变更存在缓存，真实验收前应以 `Test-ServicePrincipalAuthorization` 为准，并预留传播时间。

## 3. Secret 文件与 OAuth

client credentials 流程使用：

```text
POST https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token
grant_type=client_credentials
scope=https://graph.microsoft.com/.default
```

此流程不会返回 refresh token；Zora 在 Microsoft MCP 子进程中缓存 access token，在到期前最多 2 分钟重新申请。Graph 返回 401 时会废弃缓存并只重试一次。

客户端 Secret 必须由部署平台挂载成文件。文件内容只能有一行、最大 64 KiB；路径可以通过环境变量传入，但 Secret 内容不得出现在 `.env`、命令行参数、日志、数据库或 RunEvent 中。生产优先使用证书或 workload identity federation，当前代码先实现共享 Secret 文件模式。

示例（路径仅作说明）：

```bash
chmod 0400 /run/secrets/zora-reader-client-secret
chmod 0400 /run/secrets/zora-writer-client-secret

export ZORA_MCP_MICROSOFT_TENANT_ID='<tenant-id>'
export ZORA_MCP_MICROSOFT_CLIENT_ID='<reader-client-id>'
export ZORA_MCP_MICROSOFT_CLIENT_SECRET_FILE='/run/secrets/zora-reader-client-secret'
export ZORA_MCP_MICROSOFT_USER_ID='zora-test@example.com'

export ZORA_OFFICE_MICROSOFT_TENANT_ID='<tenant-id>'
export ZORA_OFFICE_MICROSOFT_CLIENT_ID='<writer-client-id>'
export ZORA_OFFICE_MICROSOFT_CLIENT_SECRET_FILE='/run/secrets/zora-writer-client-secret'
export ZORA_OFFICE_MICROSOFT_USER_ID='zora-test@example.com'
export ZORA_OFFICE_MICROSOFT_WRITE_ENABLED='true'
```

短期人工调试仍可使用 `*_ACCESS_TOKEN`；可轮换的部署平台令牌可使用 `*_ACCESS_TOKEN_FILE`。令牌、令牌文件和 OAuth 客户端凭据三种模式互斥。

## 4. Zora 启动配置

只读连接器：

```bash
export ZORA_MCP_ENABLED='true'
export ZORA_MCP_SERVERS_JSON='[{"name":"microsoft","command":"./bin/zora-mcp-microsoft","args":[],"allowed_tools":["search_emails","get_email","list_calendar_events","get_calendar_event"],"pass_env":["PATH","TMPDIR","ZORA_MCP_MICROSOFT_TENANT_ID","ZORA_MCP_MICROSOFT_CLIENT_ID","ZORA_MCP_MICROSOFT_CLIENT_SECRET_FILE","ZORA_MCP_MICROSOFT_USER_ID"]}]'
```

审批后写执行器：

```bash
export ZORA_OFFICE_EXECUTOR='microsoft_graph'
export ZORA_OFFICE_EXECUTOR_COMMAND='./bin/zora-mcp-microsoft'
export ZORA_OFFICE_EXECUTOR_ARGS_JSON='[]'
export ZORA_OFFICE_MICROSOFT_WRITE_ENABLED='true'
```

启动期会执行两层门禁：没有 `ZORA_OFFICE_EXECUTOR=microsoft_graph` 时不会创建真实执行器；没有 `ZORA_OFFICE_MICROSOFT_WRITE_ENABLED=true` 时配置校验直接失败。写工具不适配成 Eino Tool，只能由已批准且已持久化的 Office Operation 调用。

## 5. 真实租户验收清单

只允许使用专用测试租户或专用测试邮箱，不要直接向真实客户或同事发送内容。

- [ ] `Test-ServicePrincipalAuthorization` 对测试邮箱返回 `InScope=True`。
- [ ] 对一个范围外邮箱返回 `InScope=False`，并实际验证 Graph 访问被拒绝。
- [ ] 只读连接器能够查询测试邮件和未来日程，工具列表中没有写工具。
- [ ] Web 创建邮件草稿 → 提交确认 → 批准 → 准备 Operation → 执行；测试邮箱只收到一封邮件。
- [ ] 人为制造第一次发送后的本地失败，使用同一幂等键恢复时不重复发送。
- [ ] 创建日程后重试同一 Operation，远端只有一个 transactionId 对应事件。
- [ ] `office_operation_events` 中能定位 attempt、检查点、失败和完成状态，日志中不存在 Secret 或 access token。
- [ ] 关闭 writer Secret 或撤销角色后，执行明确失败且不会把任务标记为 completed。

验收记录至少保存：日期、代码 commit、租户别名、测试邮箱别名、Entra 应用 client ID 后 6 位、RBAC scope、执行结果和远端对象 ID。不得保存 Secret 或 access token。

## 6. 当前边界

- 已实现共享 Secret 文件的 client credentials、令牌缓存/刷新、401 单次重试、错误脱敏和读写应用隔离。
- 尚未实现证书凭据、workload identity federation、交互式 delegated 登录和 Secret 管理平台 SDK；这些不影响服务账号式办公自动化，但属于后续生产增强。
- 未拿到真实租户资源前，只能完成协议和故障注入测试，不能把本地 Mock 结果写成真实发送成功。

参考：

- [Microsoft identity platform client credentials flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow)
- [Exchange Online RBAC for Applications](https://learn.microsoft.com/en-us/exchange/permissions-exo/application-rbac)
