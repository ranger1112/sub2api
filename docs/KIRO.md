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

Kiro 账号凭据以明文 JSONB 存于 `account.credentials`。四种账号类型:

| 类型 | `account.type` | 说明 |
|---|---|---|
| **OAuth · Social** | `oauth` | kiro.dev 社交登录,用 refreshToken 自动刷新 |
| **OAuth · IdC** | `oauth` | AWS IAM Identity Center / Builder ID,需 clientId+clientSecret |
| **OAuth · External IdP** | `oauth` | 委托外部身份提供商(如 Microsoft Entra ID / Azure AD)登录,标准 OAuth2 刷新 |
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

### External IdP 凭据字段

委托外部身份提供商(如 Microsoft Entra ID / Azure AD)登录的 OAuth 账号,`auth_method = "external_idp"`,字段与上表的 Social/IdC 不同,单独列出:

| 键 | 必填 | 说明 |
|---|---|---|
| `auth_method` | 是 | 固定为 `external_idp` |
| `refresh_token` | 是 | 刷新用;来自 `refreshToken` |
| `client_id` | 是 | 外部 IdP 应用(客户端)ID;来自 `clientId` |
| `token_endpoint` | 是 | 外部 IdP 的 token 端点;来自 `tokenEndpoint`,例如 Microsoft Entra 的 `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token` |
| `profile_arn` | **是** | 注入生成请求体根 `profileArn`;Kiro 运行时网关对 `GenerateAssistantResponse` **必填**(缺失会 `400 ValidationException: profileArn is required`)。形如 `arn:aws:codewhisperer:us-east-1:<account>:profile/<id>` |
| `scopes` | 否 | OAuth scope(空格分隔);来自 `scopes`,建议包含 `offline_access` 以保证 refresh_token 可持续使用 |
| `access_token` | 否 | Bearer;过期会用 refresh_token 刷新;来自 `accessToken` |
| `region` | 否 | 兜底区域,默认 `us-east-1` |

> **生成走 Kiro 网关(与 Social/IdC 不同)**:external_idp 的微软/外部 IdP 令牌**不被 AWS 直连端点接受**。生成请求发往 Kiro 自有网关 `https://runtime.{region}.kiro.dev/`(AWS JSON-1.0 协议:`x-amz-target: KiroRuntimeService.GenerateAssistantResponse`,并带 `TokenType: EXTERNAL_IDP` 头),由网关在服务端完成到 CodeWhisperer 的鉴权翻译;请求体(`conversationState`)与响应(event-stream)与直连路径一致。见 `internal/pkg/kiro/client.go` 的 `buildExternalIdpAPIRequest`。
> **`profile_arn` 从哪来**:它**不在** `kiro-auth-token.json` 里。可在 Kiro IDE 的日志中获取:`%APPDATA%\Kiro\logs\<时间戳>\window1\exthost\kiro.kiroAgent\KiroLLMLogs.log`,搜索 `profileArn`(形如 `arn:aws:codewhisperer:us-east-1:...:profile/...`)。
>
> `client_secret` 通常**缺省**:该类应用是 public/PKCE client,登录本身不涉及客户端密钥。仅当本地 `kiro-auth-token.json` 里确实带有非空 `clientSecret` 时才需要一并填入。
> `expires_at` 与其余 OAuth 类型一致,由服务端管理,**不要手动填写**。
>
> 其余取值来自本地 `~/.aws/sso/cache/kiro-auth-token.json`:`tokenEndpoint`、`clientId`、`scopes`、`refreshToken`(以及可选的 `accessToken`)。

---

## 4. 通过管理后台添加账号

前端已支持 Kiro 平台(`frontend/src/components/account/`):

1. 后台 **账号 → 新增账号**,平台选择 **Kiro**。
2. 选择凭据类型:
   - **API Key**:仅填 `API Key`。
   - **OAuth · Social**:填 `refresh_token`(必填),`access_token`/`profile_arn`/`region` 可选。
   - **OAuth · IdC**:额外填 `client_id` + `client_secret`。
   - **OAuth · External IdP**:认证方式选择 **External IdP**,填入 `Refresh Token`、`Client ID`、`Token Endpoint`、`Profile ARN`(生成必需,见上文来源),以及可选的 `Scopes`。
3. 保存。前端会**精确**按上表键名写入 `credentials`(空值不写入,编辑态留空表示保留原值)。

前端校验(`credentialsBuilder.ts` → `validateKiroCredentials`)只在**创建**时强制必填项;**编辑**态允许留空(留空即保留已存密钥)。

### 命令行一键导入(`tools/kiro-import.py`)

在**本机登录过 Kiro**后(Kiro IDE 登录,或 `kiro-cli login --use-device-flow`),跑一条命令即可读本地 token 并**按账号名 upsert**(有则更新、无则创建,重跑只刷新 token、不产生重复号)。自动判断 social / idc / external_idp。

**本地 / 脚本化(环境变量覆盖)**:

```bash
KIRO_ACCOUNT_NAME=kiro-eu KIRO_GROUP_ID=2 python tools/kiro-import.py
# 常用变量:SUB2API_URL / SUB2API_ADMIN_EMAIL / SUB2API_ADMIN_PASSWORD /
#          KIRO_ACCOUNT_NAME / KIRO_GROUP_ID / KIRO_PROFILE_ARN / KIRO_LOG_DIR
```

**线上 / 交互模式**(提示输入地址、管理员、密码等;密码不回显):

```bash
python tools/kiro-import.py -i
```

**external_idp 专属**:`profile_arn`(生成必需)不在 token 文件里,脚本会**从最新 Kiro 会话日志自动识别**;日志里出现多个账号时,交互模式(`-i`)可从列表按序号选,非交互则用 `KIRO_PROFILE_ARN=<arn>` 指定。`region` 按 profileArn 的区自动设(如 `eu-central-1`),覆盖 token 里可能缺失的 region。profileArn 的来源见 §3「External IdP 凭据字段」。

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
| External IdP | `POST` 到凭据 `token_endpoint`(如 Microsoft Entra 的 `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token`) | 标准 OAuth2 **表单编码**(`application/x-www-form-urlencoded`,非 JSON):`grant_type=refresh_token&client_id={client_id}&refresh_token={refresh_token}&scope={scopes}` |

- 刷新在请求热路径按需触发(`KiroTokenProvider`):access token 过期前 `kiroTokenRefreshSkew = 1h` 内会刷新;成功后写入 token 缓存(key = `kiro:account:{id}`)。后台 `KiroTokenRefresher` 也会提前刷新。
- **永久失效判定**(`classifyRefreshError`):`400 + invalid_grant`(Social 还需含 `Invalid refresh token provided`;IdC 按 RFC 6749 §5.2 单独 `invalid_grant` 即永久失效;External IdP 同样按 RFC 6749 §5.2 判定,例如 Microsoft Entra 返回的 `400 invalid_grant`(`AADSTS...` 错误码)即视为永久失效)→ 账号被 `SetError`,停止调度。
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

> **⚠️ 已知限制 / TODO(用量窗口端点按账号类型分叉)**:`getUsageLimits` 目前只给 **external_idp** 账号改走了 Kiro 管理网关 `management.{region}.kiro.dev`(见 `buildExternalIdpUsageLimitsRequest`,真机验证可出数)。**social / idc 账号仍走老的 `q.amazonaws.com/getUsageLimits`,实测返回空**,因此前端用量窗口对 social/idc 显示 `-`。这**不是** free/pro 等级问题(external_idp PRO 账号正常出数)。修复方式:用一条**健康的 social/idc 账号**抓包(mitmproxy)确认其 `getUsageLimits` 的真实端点 + `TokenType`,再在 `BuildUsageLimitsRequest` 加对应分支——**不要凭空猜端点**。

### 计量与用量:token 为估算,cache 为合成,credit 为真实

**Kiro 上游不提供真实的 token / 提示词缓存账目**(经官方 `amzn-qdeveloper-streaming` 的 `MeteringEvent` 结构及多个开源实现交叉确认)。sub2api 在此基础上做了两层处理:

- **input token 只能估算**:Kiro 不给 prompt/completion 拆分。sub2api 用 `contextUsageEvent`(上下文占用% × 窗口)估算 input;output 由流式 chunk 累加(较准)。这与所有第三方工具的做法一致,非 sub2api 缺陷。

- **提示词缓存 = 中转层合成(非真缓存,不省 credit)**:上游是 `generateAssistantResponse`(Amazon Q / CodeWhisperer 格式),`cache_control` 传不到上游、也不回传 `cache_read/creation` token。sub2api 移植 M-JYuan/kiro.rs 的 `cache_tracker`,在中转层**模拟** Anthropic 滑动窗口缓存的「最长公共前缀命中」语义,给客户端一个符合 Anthropic 语义的账面(`internal/pkg/kiro/cache_tracker.go`):按 credential(account.ID)隔离的进程内前缀指纹表,`Compute` 只读、`Update` 仅在上游成功后写、命中不续 TTL、区分 5m/1h、按模型 min-cacheable 过滤。合成的 `cache_read/creation` 注入客户端响应 usage,并按既有 token 计费口径计价(路线①)。**重申:这是账面兼容层,不会降低 Kiro 的真实 credit 成本,重启即清空。**

- **`meteringEvent` = 真实 credit 消耗**:payload 形如 `{"unit":"credit","usage":0.34}`,是 Kiro **唯一给出的真实成本数字**(对应 Pro/Power 的月度 credit 额度)。sub2api 累加到 `StreamResult.CreditUsage`,并**三处透出**:① 客户端响应 usage 的 `credit_usage`/`credit_unit`;② `kiro.request_credit_usage` 结构化日志;③ 落库 `usage_logs.kiro_credit_usage`(迁移 `159`),管理端用量表 token 明细展示「Kiro 额度消耗」。credit 仅作观测/对账,不参与 token 计费。

> 结论:kiro 账号的 **token 是估算值、cache 是中转层合成的账面**,真实成本口径唯一看 **credit** 消耗。

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
| External IdP 刷新失败(`400 invalid_grant` / `AADSTS...`) | refresh_token 已过期或被吊销,需重新在外部 IdP(如 Microsoft Entra)完成登录获取新 token;确认 `token_endpoint`/`client_id` 未变。 |
