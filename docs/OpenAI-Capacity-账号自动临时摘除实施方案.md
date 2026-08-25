# OpenAI Capacity 账号自动临时摘除实施方案

> **状态：本地实现完成、待按 Shadow → Canary 上线验证（2026-08-04）。**
> **已完成：管理员策略控制面、独立 Redis 执行状态、跨实例分组保护锁、selector 筛选、half-open 单探针与 owner-bound 自动恢复。**
> **未包含：生产部署、线上 Redis/真实流量 smoke、管理员手工 release/运行态列表。**
> `enforce` 现在会真实影响调度；默认仍为 `disabled`，升级后不会自动摘除任何账号。

## 1. 要解决什么

同一个 OpenAI 上游账号持续返回已确认的 **Capacity / overload** 错误时，不应继续被调度；但这不是永久禁用：

```text
同一账号在窗口内达到阈值
  → 短暂从新请求调度中隔离
  → 冷却结束后只允许一个 half-open 探测请求
  → 探测完整成功：恢复正常调度
  → 探测再次 Capacity：按 retrip 冷却重新隔离
```

初始推荐默认值：

| 项目 | 默认值 |
| --- | ---: |
| rolling window | 300 秒（5 分钟） |
| error threshold | 3 次 |
| initial cooldown | 600 秒（10 分钟） |
| retrip window | 3600 秒（1 小时） |
| retrip cooldown | 1800 秒（30 分钟） |
| max cooldown | 1800 秒（30 分钟） |
| half-open 并发 | 固定 1 个 |
| half-open lease | 120 秒，30 秒续租 |

因此，**首次会临时不可用约 10 分钟；在随后 1 小时内再次触发则约 30 分钟**。执行层完成后，账号会自动进入 half-open；完整成功才恢复正常调度，不需要管理员手工恢复。

## 2. 非目标与硬边界

本机制只处理 OpenAI Capacity，不处理或不覆盖下列机制：

- 不修改 `accounts.schedulable=false`，不会永久禁用账号；
- 不自动删除账号、Token、代理或用户数据；
- 不把 HTTP `401`、`403`、`429`、配额耗尽、鉴权失败、普通网络/代理错误、`unexpected EOF`、客户端取消识别为 Capacity；
- 不通过 `tail -f` 日志再直接改数据库；
- 不对已经向客户端输出内容的流做透明重放；
- 不复用 `accounts.temp_unschedulable_until` 或 `accounts.temp_unschedulable_reason` 作为 Capacity 状态。

最后一条不可省略：现有临时不可调度字段属于 401、Token 刷新、代理、普通 502 等多个 owner。Capacity 自动恢复若清空它们，会误解除其他故障或人工隔离。

## 3. 当前已落地的管理员控制面

### 3.1 API

管理员接口（都位于 `/api/v1/admin/settings`）：

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `GET` | `/openai-capacity-quarantine` | 读取当前策略和 `revision` |
| `PUT` | `/openai-capacity-quarantine` | 以乐观锁保存策略 |
| `POST` | `/openai-capacity-quarantine/test-matcher` | 用脱敏结构化错误测试**已保存**的规则 |

`PUT` 必须携带读取时的 `revision`。若另一个管理员先保存，服务返回：

```text
409 Conflict
OPENAI_CAPACITY_QUARANTINE_REVISION_CONFLICT
```

管理员应刷新后合并修改，不允许静默覆盖。

### 3.2 管理端位置

后台 **系统设置 → 网关** 新增 “OpenAI Capacity 临时摘除” 卡片，可设置：

1. 三态模式；
2. 统计窗口、错误阈值、首次/再次/最大冷却；
3. half-open lease 和续租间隔；
4. 自定义 Capacity 规则；
5. OpenAI 分组的启用范围、保底账号数、最大隔离比例、全局尖峰保护；
6. 脱敏 matcher 测试。

页面明确显示 `disabled` / `shadow` / `enforce` 的实际含义；保存 `enforce` 后，执行层会在规则命中并通过分组保护时真实摘除账号。

### 3.3 三态模式

```text
disabled  不计数、不新增隔离。
shadow    识别并记录 would-trip；绝不改变调度。
enforce   执行层部署后，允许真正隔离与自动恢复。
```

默认值是 `disabled`，因而升级不会造成意外摘号。

## 4. 管理员策略契约

持久化键：`openai_capacity_quarantine_settings`。

```json
{
  "revision": 0,
  "mode": "disabled",
  "window_seconds": 300,
  "error_threshold": 3,
  "initial_cooldown_seconds": 600,
  "retrip_window_seconds": 3600,
  "retrip_cooldown_seconds": 1800,
  "max_cooldown_seconds": 1800,
  "half_open": {
    "max_requests": 1,
    "lease_seconds": 120,
    "renew_interval_seconds": 30
  },
  "match_rules": [],
  "group_policies": []
}
```

服务端保存时校验：

- `mode ∈ {disabled, shadow, enforce}`；
- `window_seconds` 为 30–3600，阈值为 2–20；
- 所有 cooldown 最大 24 小时，`retrip_window ≥ window`，`retrip_cooldown ≥ initial`，`max_cooldown ≥ retrip_cooldown`；
- `half_open.max_requests` 第一版固定为 `1`；lease 为 30 秒–15 分钟，续租必须小于 lease；
- 规则最多 32 条、每条 1–4 个条件；分组策略最多 256 条；
- 每个分组 ID 唯一、保底账号数至少 1、最大隔离比例在 `(0, 1]`。

读取到损坏或语义非法的旧 JSON 时，服务端回退为安全的 `disabled` 默认策略；不会把未知配置当作 `enforce`。

## 5. 自定义 Capacity 识别规则

规则是受限 DSL，不接受任意正则、JavaScript、Lua 或“所有 HTTP 503”。一条规则中的条件是 **AND**；多条规则是 **OR**。

```json
{
  "id": "vendor-overloaded",
  "name": "Vendor overloaded response",
  "enabled": true,
  "conditions": [
    { "source": "provider_code", "operator": "equals", "value": "model_capacity" },
    { "source": "message", "operator": "contains_ci", "value": "overloaded" }
  ]
}
```

允许字段与操作符：

| source | operator |
| --- | --- |
| `provider_code` | `equals`, `contains_ci` |
| `provider_type` | `equals`, `contains_ci` |
| `message` | `equals`, `contains_ci` |

内置默认规则：

```text
provider_code == model_capacity
message contains_ci "servers are currently overloaded"
```

无论管理员如何配置，下列错误均有不可绕过的排除门：

```text
HTTP 401 / 403 / 429
rate_limit / quota / insufficient_quota
authentication / unauthorized / forbidden
unexpected eof / client cancel
```

测试接口只接受 `http_status`、`provider_code`、`provider_type` 和脱敏后的 `message`；不接收/保存 Authorization、Cookie、Token 或原始上游响应体。

## 6. 分组策略与保底账号池

Capacity 是**账号级**隔离：一个账号若属于多个 OpenAI Group，隔离会影响它在这些 Group 中的全部路由。管理员需要为要启用的 Group 写入策略：

```json
{
  "group_id": 12,
  "enabled": true,
  "min_remaining_accounts": 2,
  "max_quarantined_fraction": 0.25,
  "global_spike_distinct_accounts": 5,
  "global_spike_window_seconds": 120
}
```

执行层在打开隔离前会：

1. 取得账号的全部关联、且在策略中启用的 Group；通过 Redis Lua 以 Group ID 固定顺序原子取得短期 `trip-lock`，把“检查池子 → 打开隔离”串行化；
2. 使用账号仓库的 `ListSchedulableByGroupIDAndPlatform` 可调度条件（活跃、可调度、未过期、未处于共享临时隔离/过载/限流窗口），而不是 `COUNT(accounts)`；
3. 模拟移除该账号；
4. 对任一关联且已启用的 Group，若剩余可用账号低于 `min_remaining_accounts`，或隔离比例超过 `max_quarantined_fraction`，则拒绝 trip；
5. 若指定时间窗内出现足够多的不同账号 Capacity 事件，标记 `global_spike` 并停止继续逐个隔离。

被 pool guard 抑制时只能记录 `pool_guard_suppressed`，不能把账号改成短 cooldown 作为替代动作。

`trip-lock` 仅保护 Capacity 自己的判断临界区；它不会写入或清理 `accounts.temp_unschedulable_*`。Redis 失效时整条 Capacity 路径 fail-open，而不是把可用账号全拦掉。

## 7. 已实现的执行层状态机

```text
closed
  -- threshold --> open_initial (默认 10m)
open_initial / open_retrip
  -- cooldown TTL expiry --> half_open (only one probe owner)
HALF_OPEN
  -- owner request full protocol success --> closed
  -- capacity --> open_retrip (默认 30m；仅在 retrip window 内)
```

关键语义：

- 同一个 `request_id + account_id` 在窗口内最多计数一次；
- 已经处于 OPEN 的账号收到晚到错误，不得无限延长本代 cooldown；
- 只有完整 HTTP/stream/WebSocket 成功终态能让 half-open 恢复；首个 SSE token、心跳或外层 HTTP 200 都不足以说明成功；
- probe 必须在真正建立上游请求**之前**原子获得，不得在 selector 已经消耗账号后再抢；
- 长 SSE/WebSocket 需要 lease heartbeat；进程崩溃后依赖 TTL 回收；
- Redis/执行状态存储不可用时 fail-open 并告警，不能造成全账号不可用。

实现细节：

- `open_initial` 的默认冷却是 600 秒；若 half-open 探针在原隔离打开后 `retrip_window_seconds`（默认 3600 秒）内再次命中 Capacity，则转为 `open_retrip` 并使用至少 1800 秒。连续 failed retrip 会按上一轮时长翻倍，但绝不超过 `max_cooldown_seconds`；默认 `retrip_cooldown == max_cooldown == 1800`，因此默认每次 retrip 都是 30 分钟。超过 retrip window 则重新走初始冷却；
- 冷却 TTL 结束后，selector 不会永久排除账号；真正建立上游请求前才原子抢占 half-open lease，未抢到的请求会释放普通账号并发槽并重新选号；
- 成功回收是 **owner-bound**：只有取得该 lease 的同一请求完整成功才可关闭状态。旧请求迟到成功、其他并发请求或已 retrip 的旧 owner 都不能误恢复账号；
- `shadow` 计数并记录 would-trip，但不打开隔离、不改变 selector；`disabled` 连事件也不计数。

## 8. 已实现的独立 Redis 状态存储

Capacity 状态不需要 PostgreSQL migration；执行态全部是可过期、可重建的 Redis 专属 key，前缀为：

```text
sub2api:openai:capacity:events:{account_id}
sub2api:openai:capacity:open:{account_id}
sub2api:openai:capacity:meta:{account_id}
sub2api:openai:capacity:probe:{account_id}
sub2api:openai:capacity:generation:{account_id}
sub2api:openai:capacity:group-events:{group_id}:{model}
sub2api:openai:capacity:trip-lock:group:{group_id}
```

Redis Lua 负责 rolling window 去重、按不同账号计数的 group spike、打开隔离、half-open owner lease、owner 校验恢复和多 Group trip-lock；不得用进程内 map 作为多实例生产状态源。`meta` 至少保留 48 小时，因此 open TTL 到期后仍能进入 half-open，而不会无状态直接放行。

这套 key 与已有共享临时隔离完全隔离，Capacity 实现不得调用或清理：

```text
accounts.temp_unschedulable_until
accounts.temp_unschedulable_reason
SetTempUnschedulable
ClearTempUnschedulable
BlockAccountScheduling（作为 Capacity 的实现手段）
```

## 9. 已实现的接入点与观察性边界

统一归一化 OpenAI upstream 错误，至少提取：

```go
type NormalizedOpenAICapacityError struct {
    HTTPStatus   int
    ProviderCode string
    ProviderType string
    Message      string // redacted / bounded
    RequestID    string
    AccountID    int64
    GroupID      int64
    Model        string
}
```

错误在 OpenAI 上游错误统一快路径归一化后计入；普通 `502/503` 不得仅凭状态码计为 Capacity。selector 的 fresh/recheck 阶段跳过未过期的 Capacity cooldown；`/responses` 的 HTTP、SSE 和 WebSocket，以及 chat completions、embeddings、alpha search、images 等共用账号槽位的转发路径，在真实上游建立前执行 half-open 准入。

当前实现使用结构化运行日志记录：

```text
openai_capacity_would_trip
openai_capacity_quarantine_opened
openai_capacity_pool_guard_suppressed
openai_capacity_recovered
```

日志/metrics 不得把 account ID、user ID、request ID 作为 Prometheus label。后续若接入 Prometheus 或 `ops_error_logs`，必须延续这个标签与脱敏边界；它不是本次本地实现的生产观测验收证据。

## 10. 分阶段交付与验收

### Phase 1 — 已完成：控制面

- [x] 三态 runtime policy，默认 `disabled`；
- [x] 自定义受限 matcher DSL 与硬性排除规则；
- [x] 时间、阈值、half-open、Group policy 校验；
- [x] 策略 revision CAS，冲突返回 409；
- [x] 管理端编辑、OpenAI Group 选择、脱敏 matcher 测试；
- [x] 后端策略单测与前端设置页定向测试。

### Phase 2 — P0：执行数据面（本地实现完成）

- [x] Capacity 专属 Redis repository；无需 PostgreSQL migration；
- [x] Redis rolling window、`request_id + account_id` 去重、TTL、trip lock；
- [x] 跨实例多 Group Redis Lua lock 与保底/比例/global spike guard；
- [x] OpenAI selector cooldown 排除、真正转发前的 half-open lease；
- [x] 结构化错误归一化、HTTP/SSE/WebSocket owner-bound 完整成功恢复；
- [ ] 管理端运行状态、手工 release 与审计（非本次需求，后续可单独增加）。

### Phase 3 — Shadow 与 Canary

1. 先仅启用 `shadow`，确认命中样本不含 401/403/429/配额/EOF；
2. 选择账号充足的单个 Group 开启 `enforce`；
3. 验证保底数量、最大隔离比例、half-open 单探测和自动恢复；
4. 通过观测窗口后再扩展 Group。

## 11. 回滚

- 首选：管理员将模式切回 `disabled`；新事件不再改变状态；
- 该模式下 selector 也会忽略已有 Capacity Redis 状态；原有 key 按 TTL 自然过期，无需也不应触碰账号表；
- 若未来增加手工 release，它只能删除本机制的 `sub2api:openai:capacity:*` key，且必须保留审计；
- **禁止**批量 `UPDATE accounts SET temp_unschedulable_until = NULL`，这会破坏其他 owner 的隔离；
- 不要把本功能的回滚与无关数据库、Redis 重启绑定。

## 12. 本次代码验证范围

已覆盖的本地回归：

- 默认策略为 `disabled`；
- 保存会递增 revision，过期 revision 会冲突；
- half-open 并发不为 1 会被拒绝；
- `status_code` 不能作为自定义规则 source；
- 即使用户自定义 `model_capacity`，HTTP 429 仍被硬性排除；
- 内置 `model_capacity` 规则命中；
- 管理端加载版本化策略、保留 revision 保存、调用脱敏 matcher endpoint；
- Redis request 去重、rolling window、同账号 signal 滚动续期、不同账号 global spike、open 不延长、half-open 单 owner、owner/旧 owner 恢复保护与多 Group trip-lock；
- `disabled` / `shadow` / `enforce`、首次/retrip 冷却、最小保底、最大隔离比例、global spike、设置/Redis 故障 fail-open；
- 真实账号槽位被 Capacity veto 后会释放普通并发槽；owner 请求成功后会自动恢复；
- Wire 生成与后端相关包空测试编译通过。

未声称完成：生产部署、线上 Redis/真实流量、SSE/WebSocket 长连接回归、Prometheus/`ops_error_logs` 观测和管理员运行态页面，必须在上线流程中单独验证。
