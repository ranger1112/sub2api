# Kiro 上游接入说明

本文档介绍 sub2api 中 **Kiro**(AWS Kiro / Amazon Q Developer,底层为 Anthropic Claude)上游的部署、账号配置与运维。

> ⚠️ **合规提示**:Kiro 接入通过复用 Kiro IDE 的私有 API(伪装 User-Agent、machineId 等)访问上游,可能违反 AWS/Kiro 的服务条款。请在你所在司法辖区允许的范围内、仅用于授权用途。是否启用与由此产生的风险由部署者自行承担。

---

## 1. 概述

- Kiro 是一个 **Anthropic 协议** 上游:请求走 `/v1/messages`(Messages API),与 Claude 账号完全一致。
- Kiro 账号以 `platform = "kiro"` 存储在 **anthropic 分组**下,由 `GatewayHandler` 按 `account.Platform` **逐账号分流**到 `KiroGatewayService`,**无需新增路由**。
- 上游返回的是 **AWS Event Stream 二进制流**,sub2api 内部完成:`EventStream 解码 → Kiro 事件 → Anthropic SSE`(流式)/ `完整消息 JSON`(非流式)。
- 无需数据库迁移:Kiro 凭据以**明文 JSONB**(`account.credentials` map)存储,沿用现有账号模型。

### 请求链路

```
客户端 /v1/messages
  └─ GatewayHandler (选号/并发/计费)
       └─ account.Platform == "kiro"  ──► KiroGatewayService.Forward
            ├─ KiroTokenProvider.GetAccessToken   (API Key 直取 / OAuth 按需刷新)
            ├─ kiro.Convert(AnthropicRequest)     (Anthropic → Kiro 请求体)
            ├─ POST https://q.{apiRegion}.amazonaws.com/generateAssistantResponse
            │      (伪装头 + Bearer + profileArn 注入)
            └─ kiro.StreamMessages / CollectMessages
                 └─ EventStream 解码 → Anthropic SSE / 完整消息 JSON
```

代码位置:

| 层 | 目录 |
|---|---|
| EventStream 解码器 | `backend/internal/pkg/kiro/eventstream/` |
| 模型映射 / 请求转换 / 事件 / 流 | `backend/internal/pkg/kiro/` |
| HTTP 请求构造 / token 刷新 | `backend/internal/pkg/kiro/{client,oauth}.go` |
| 服务层(网关/token/配额/健康) | `backend/internal/service/kiro_*.go` |

---

## 2. 支持的模型

Kiro 采用**严格版本匹配**(`internal/pkg/kiro/model_map.go`)。客户端模型名(大小写不敏感、`-thinking` 后缀不影响)必须能匹配到以下之一,否则返回 `model_not_found`:

| 客户端模型名(含子串) | 映射到 Kiro | 上下文窗口 |
|---|---|---|
| `sonnet` + `4.5`/`4-5` | `claude-sonnet-4.5` | 200K |
| `sonnet` + `4.6`/`4-6` | `claude-sonnet-4.6` | 1M |
| `opus` + `4.5`/`4-5` | `claude-opus-4.5` | 200K |
| `opus` + `4.6`/`4-6` | `claude-opus-4.6` | 1M |
| `opus` + `4.7`/`4-7` | `claude-opus-4.7` | 1M |
| `opus` + `4.8`/`4-8` | `claude-opus-4.8` | 1M |
| `haiku`(任意 4.5) | `claude-haiku-4.5` | 200K |

> 裸 `sonnet-4`、`gpt-*`、`claude-3-5-*` 等**不会**被识别。若要把这些别名归一到 Kiro 模型,请在 sub2api 的**渠道/分组级模型映射**里处理,而非依赖本层。
> `-thinking` 后缀用于向上游传递 thinking 意图(`ParseThinking`),不影响模型匹配。

---

## 3. 账号凭据

Kiro 账号凭据以明文 JSONB 存于 `account.credentials`。三种账号类型:

| 类型 | `account.type` | 说明 |
|---|---|---|
| **OAuth · Social** | `oauth` | kiro.dev 社交登录,用 refreshToken 自动刷新 |
| **OAuth · IdC** | `oauth` | AWS IAM Identity Center / Builder ID,需 clientId+clientSecret |
| **API Key** | `apikey` | 直接用 `kiro_api_key` 作 Bearer,无需刷新 |

### 凭据字段

| 键 | 适用 | 必填 | 说明 |
|---|---|---|---|
| `auth_method` | OAuth | 是 | `social` \| `idc`(留空按 clientId/clientSecret 推断) |
| `access_token` | OAuth | 否¹ | Bearer(过期会用 refresh_token 刷新) |
| `refresh_token` | OAuth | 是 | 刷新用;**须完整**,长度 <100 或含 `...` 会被判为截断而拒绝 |
| `profile_arn` | OAuth | 否 | 注入请求体根 `profileArn`;刷新响应也可能返回 |
| `client_id` | IdC | 是(IdC) | IdC 刷新必填 |
| `client_secret` | IdC | 是(IdC) | IdC 刷新必填 |
| `kiro_api_key` | API Key | 是(apikey) | 直接作 Bearer,并带 `tokentype: API_KEY` 头 |
| `region` | 全部 | 否 | 兜底区域,默认 `us-east-1` |
| `auth_region` | OAuth | 否 | token 刷新端点区域(优先级高于 `region`) |
| `api_region` | 全部 | 否 | `generateAssistantResponse` 端点区域(优先级高于 `region`) |
| `machine_id` | 全部 | 否 | 64 位 hex 或 UUID;留空则派生(见下) |
| `expires_at` | OAuth | 否 | RFC3339,由服务端管理,**前端不要填** |

¹ `access_token` 可留空:只要有有效 `refresh_token`,首次请求会自动刷新获取。

> **machineId 派生**(留空时):
> - API Key → `sha256hex("KiroAPIKey/" + kiro_api_key)`
> - OAuth → `sha256hex("KotlinNativeAPI/" + refresh_token)`
> - 都缺失 → 随机(按账号 ID 进程内稳定)

> **区域解析优先级**:凭据 `auth_region`/`api_region` > 凭据 `region` > 全局 `ClientConfig` 对应区域 > `ClientConfig.Region`(默认 `us-east-1`)。

---

## 4. 通过管理后台添加账号

前端已支持 Kiro 平台(`frontend/src/components/account/`):

1. 后台 **账号 → 新增账号**,平台选择 **Kiro**。
2. 选择凭据类型:
   - **API Key**:仅填 `API Key`。
   - **OAuth · Social**:填 `refresh_token`(必填),`access_token`/`profile_arn`/`region` 可选。
   - **OAuth · IdC**:额外填 `client_id` + `client_secret`。
3. 保存。前端会**精确**按上表键名写入 `credentials`(空值不写入,编辑态留空表示保留原值)。

前端校验(`credentialsBuilder.ts` → `validateKiroCredentials`)只在**创建**时强制必填项;**编辑**态允许留空(留空即保留已存密钥)。

---

## 5. 配置

Kiro 上游的版本/区域伪装参数由 `kiro.ClientConfig` 提供,默认值在 `provideKiroClientConfig()`(`backend/cmd/server/wire.go`)= `kiro.DefaultClientConfig()`:

| 字段 | 默认 | 用途 |
|---|---|---|
| `Region` | `us-east-1` | 兜底区域 |
| `KiroVersion` | `0.11.107` | 伪装 UA 中的 Kiro IDE 版本 |
| `SystemVersion` | `win32#10.0.22631` | 伪装 UA 中的系统 |
| `NodeVersion` | `22.22.0` | 伪装 UA 中的 Node |
| `MachineID` | 空 | 全局兜底 machineId(通常留空,按账号派生) |

如需修改默认伪装参数,编辑 `provideKiroClientConfig()` 后**重新生成 wire**:

```bash
cd backend && make generate   # go generate ./cmd/server + ./ent
```

> `ClientConfig` 目前是**编译期常量**(无环境变量开关)。若要按部署环境切换区域/版本,可把 `provideKiroClientConfig()` 改为从 `config` 读取——但当前实现是硬编码默认值。

### 代理

Kiro 走账号绑定的代理:`KiroHTTPClientFactory` = `repository.CreateKiroHTTPClient(proxyURL)`,`Timeout: 0`(不设整请求超时,流式依赖 ctx 取消)。账号绑定代理即自动生效。

---

## 6. Token 刷新

`internal/pkg/kiro/oauth.go`:

| 方式 | 端点 | 请求体(camelCase) |
|---|---|---|
| Social | `POST https://prod.{authRegion}.auth.desktop.kiro.dev/refreshToken` | `{refreshToken}` |
| IdC | `POST https://oidc.{authRegion}.amazonaws.com/token` | `{clientId, clientSecret, refreshToken, grantType:"refresh_token"}` |

- 刷新在请求热路径按需触发(`KiroTokenProvider`):access token 过期前 `kiroTokenRefreshSkew = 1h` 内会刷新;成功后写入 token 缓存(key = `kiro:account:{id}`)。后台 `KiroTokenRefresher` 也会提前刷新。
- **永久失效判定**(`classifyRefreshError`):`400 + invalid_grant`(Social 还需含 `Invalid refresh token provided`;IdC 按 RFC 6749 §5.2 单独 `invalid_grant` 即永久失效)→ 账号被 `SetError`,停止调度。
- 其余(401/403/429/5xx)按**临时不可调度**处理(可重试)。
- API Key 凭据**不支持刷新**(直接用 `kiro_api_key`)。

---

## 7. 运维:Failover / Ops 遥测 / 健康检查 / 配额

### Failover(跨账号重试)
`KiroGatewayService`:上游 **429 / 5xx / 连接级错误**且**首字节前**(未向客户端写出 SSE 字节)→ 返回 `UpstreamFailoverError`,由 handler 换号重试;**4xx 客户端错误**或流已开始 → 终止,写回 Anthropic 风格 JSON 错误。`context.Canceled`(客户端主动断开)不触发换号。

### Ops 遥测
每次上游错误都记录一次 ops 事件(`setOpsUpstreamError` / `appendOpsUpstreamError`),含真实上游状态码、`x-amzn-RequestId`、`Kind`(`failover`/`http_error`)。

### 账号健康检查
后台**测试账号连通性**(`account_test_service.go` → `testKiroAccountConnection`)通过 `getUsageLimits` 探测:
- **401**(`NeedsReauth`,凭据失效)或 **403**(`IsForbidden`)→ 标记账号 `error`(停止调度)。
- **429** → 瞬时限流,**不**标记 error。

### 配额查询
`KiroQuotaService.FetchUsage(ctx, accountID)` 通过 `getUsageLimits` 返回用量/配额,401/403/429 降级为 degraded `UsageInfo`。

> 该服务已在 wire 中注册,但**尚未接入 admin 路由/handler**(为后续配额展示端点预留)。接入时新增一个 admin handler 调用 `KiroQuotaService.FetchUsage` 即可。

---

## 8. 部署检查清单

- [x] 无需数据库迁移(凭据为明文 JSONB)。
- [x] 后端构建:`cd backend && go build ./...`
- [x] wire/ent 代码生成一致:`cd backend && make generate` 后 `git diff` 无变化。
- [x] 前端类型检查:`cd frontend && pnpm vue-tsc --noEmit`(项目使用 **pnpm**,非 npm)。
- [ ] **真机联调(尚未验证)**:用真实 Kiro 账号跑通「加账号 → `/v1/messages` 流式/非流式 → 账号测试/配额」。当前 `getUsageLimits` 的请求/响应为**防御式兼容**,machineId/伪装头是否过风控需真机确认。

---

## 9. 故障排查

| 现象 | 可能原因 / 处理 |
|---|---|
| `model_not_found` | 模型名未匹配严格版本表(见 §2);用渠道/分组模型映射归一化。 |
| `refreshToken appears truncated` | 粘贴的 refresh_token 被 Kiro IDE 截断(长度 <100 或含 `...`);重新完整导出。 |
| 账号被标记 error(刷新) | `400 invalid_grant` → refresh_token 已撤销/过期,需重新授权。 |
| 账号被标记 error(测试) | `getUsageLimits` 返回 401/403,凭据失效或无权限。 |
| 频繁 failover / 429 | 上游限流,增加账号数或降低并发;429 不标记 error,会自动换号重试。 |
| 非流式响应缺内容 / 截断 | 检查代理稳定性;`dec.Finish()` 会对 EventStream 截断报错。 |
| IdC 刷新失败 | 确认 `client_id` + `client_secret` 完整,`auth_method=idc`。 |
