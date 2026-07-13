export function applyInterceptWarmup(
  credentials: Record<string, unknown>,
  enabled: boolean,
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials.intercept_warmup_requests = true
  } else if (mode === 'edit') {
    delete credentials.intercept_warmup_requests
  }
}

// ── Kiro (Anthropic-compatible upstream) ──────────────────────────────
export type KiroAccountType = 'oauth' | 'apikey'
export type KiroAuthMethod = 'social' | 'idc' | 'external_idp'

export interface KiroCredentialInputs {
  accountType: KiroAccountType
  authMethod: KiroAuthMethod
  accessToken: string
  refreshToken: string
  profileArn: string
  region: string
  authRegion: string
  apiRegion: string
  machineId: string
  clientId: string
  clientSecret: string
  tokenEndpoint: string
  scopes: string
  apiKey: string
}

/**
 * Build the credentials map for a Kiro account.
 *
 * Only non-empty values are emitted so the same helper works for both create
 * (fresh map) and edit (spread over existing credentials => blank keeps the
 * previously stored secret). `expires_at` is intentionally never set here; the
 * server manages it.
 */
export function buildKiroCredentials(inputs: KiroCredentialInputs): Record<string, unknown> {
  const credentials: Record<string, unknown> = {}
  const set = (key: string, value: string): void => {
    const trimmed = value.trim()
    if (trimmed) credentials[key] = trimmed
  }

  if (inputs.accountType === 'apikey') {
    set('kiro_api_key', inputs.apiKey)
    return credentials
  }

  // OAuth (social / idc)
  credentials.auth_method = inputs.authMethod
  set('access_token', inputs.accessToken)
  set('refresh_token', inputs.refreshToken)
  set('profile_arn', inputs.profileArn)
  if (inputs.authMethod === 'idc' || inputs.authMethod === 'external_idp') {
    set('client_id', inputs.clientId)
    set('client_secret', inputs.clientSecret)
  }
  if (inputs.authMethod === 'external_idp') {
    set('token_endpoint', inputs.tokenEndpoint)
    set('scopes', inputs.scopes)
  }
  set('region', inputs.region)
  set('auth_region', inputs.authRegion)
  set('api_region', inputs.apiRegion)
  set('machine_id', inputs.machineId)
  return credentials
}

/**
 * Validate Kiro credential inputs. Returns an i18n key describing the first
 * error, or null when valid. In edit mode required secrets may be left blank
 * (blank keeps the existing stored value), so no error is produced.
 */
export function validateKiroCredentials(
  inputs: KiroCredentialInputs,
  mode: 'create' | 'edit' = 'create'
): string | null {
  if (mode === 'edit') {
    return null
  }
  if (inputs.accountType === 'apikey') {
    if (!inputs.apiKey.trim()) return 'admin.accounts.kiro.errors.apiKeyRequired'
    return null
  }
  if (!inputs.refreshToken.trim()) return 'admin.accounts.kiro.errors.refreshTokenRequired'
  if (inputs.authMethod === 'idc') {
    if (!inputs.clientId.trim()) return 'admin.accounts.kiro.errors.clientIdRequired'
    if (!inputs.clientSecret.trim()) return 'admin.accounts.kiro.errors.clientSecretRequired'
  }
  if (inputs.authMethod === 'external_idp') {
    if (!inputs.clientId.trim()) return 'admin.accounts.kiro.errors.clientIdRequired'
    if (!inputs.tokenEndpoint.trim()) return 'admin.accounts.kiro.errors.tokenEndpointRequired'
    // The Kiro runtime gateway requires profileArn for GenerateAssistantResponse.
    if (!inputs.profileArn.trim()) return 'admin.accounts.kiro.errors.profileArnRequired'
  }
  return null
}

export const ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY = 'antigravity_project_id'

export function applyAntigravityProjectID(
  credentials: Record<string, unknown>,
  projectId: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = projectId.trim()
  if (trimmed) {
    credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]
  }
}

// ========== 请求头覆写（仅 anthropic/openai 平台的 api_key 账号） ==========

export const HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY = 'header_override_enabled'
export const HEADER_OVERRIDES_CREDENTIAL_KEY = 'header_overrides'

export interface HeaderOverrideRow {
  name: string
  value: string
}

/** 请求头覆写支持的平台（与后端 IsHeaderOverrideEligible 保持一致） */
export function isHeaderOverridePlatform(platform: string): boolean {
  return platform === 'anthropic' || platform === 'openai'
}

/** 禁止覆写的请求头（与后端 headerOverrideBlockedNames 保持一致） */
const HEADER_OVERRIDE_BLOCKED_NAMES = new Set([
  'host',
  'content-length',
  'content-type',
  'transfer-encoding',
  'connection',
  'keep-alive',
  'proxy-authenticate',
  'proxy-authorization',
  'proxy-connection',
  'te',
  'trailer',
  'upgrade',
  'authorization',
  'x-api-key',
  'x-goog-api-key',
  'cookie',
  'accept-encoding',
  'sec-websocket-key',
  'sec-websocket-version',
  'sec-websocket-extensions',
  'sec-websocket-protocol',
  'sec-websocket-accept',
  'session_id',
  'conversation_id',
  'x-codex-turn-state',
  'x-codex-turn-metadata',
  'chatgpt-account-id',
  'x-claude-code-session-id',
  'x-client-request-id'
])

/** RFC 7230 token：合法的 HTTP header 名称字符集 */
const HEADER_NAME_PATTERN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/

function isValidHeaderOverrideName(name: string): boolean {
  return HEADER_NAME_PATTERN.test(name)
}

/** 模板：Claude Code CLI 标准客户端请求头（值留空由管理员填写） */
const ANTHROPIC_HEADER_OVERRIDE_TEMPLATE = [
  'user-agent',
  'x-app',
  'anthropic-beta',
  'anthropic-version',
  'anthropic-dangerous-direct-browser-access',
  'x-stainless-lang',
  'x-stainless-package-version',
  'x-stainless-os',
  'x-stainless-arch',
  'x-stainless-runtime',
  'x-stainless-runtime-version',
  'x-stainless-retry-count',
  'x-stainless-timeout'
]

/** 模板：Codex CLI 标准客户端请求头（值留空由管理员填写） */
const OPENAI_HEADER_OVERRIDE_TEMPLATE = [
  'user-agent',
  'originator',
  'openai-beta',
  'version',
  'accept',
  'accept-language'
]

export function getHeaderOverrideTemplate(platform: string): HeaderOverrideRow[] {
  const names =
    platform === 'openai' ? OPENAI_HEADER_OVERRIDE_TEMPLATE : ANTHROPIC_HEADER_OVERRIDE_TEMPLATE
  return names.map((name) => ({ name, value: '' }))
}

/** 与后端 maxHeaderOverride* 常量保持一致 */
const HEADER_OVERRIDE_MAX_ENTRIES = 64
const HEADER_OVERRIDE_MAX_NAME_LENGTH = 200
const HEADER_OVERRIDE_MAX_VALUE_LENGTH = 8192

/** header value 不允许包含控制字符（与后端 httpguts.ValidHeaderFieldValue 对齐） */
// eslint-disable-next-line no-control-regex
const HEADER_VALUE_INVALID_PATTERN = /[\x00-\x08\x0a-\x1f\x7f]/

/** 长度限制按 UTF-8 字节计（与后端 Go len() 对齐，避免多字节值前端放行后端 400） */
const HEADER_TEXT_ENCODER = new TextEncoder()
function utf8ByteLength(value: string): number {
  return HEADER_TEXT_ENCODER.encode(value).length
}

/**
 * 校验请求头覆写行，返回首个错误的 i18n key（无错误返回 null）。
 * 名称为空但值非空 → invalidName；名称非法 → invalidName；
 * 禁止覆写 → blockedName；大小写不敏感重名 → duplicateName；
 * 值含控制字符或超长 → invalidValue；条目过多 → tooManyEntries。
 */
export function validateHeaderOverrideRows(
  rows: HeaderOverrideRow[]
): 'invalidName' | 'blockedName' | 'duplicateName' | 'invalidValue' | 'tooManyEntries' | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    const value = row.value.trim()
    if (!name) {
      if (value) return 'invalidName'
      continue
    }
    if (!isValidHeaderOverrideName(name) || name.length > HEADER_OVERRIDE_MAX_NAME_LENGTH) {
      return 'invalidName'
    }
    const lower = name.toLowerCase()
    if (HEADER_OVERRIDE_BLOCKED_NAMES.has(lower)) return 'blockedName'
    if (seen.has(lower)) return 'duplicateName'
    if (
      HEADER_VALUE_INVALID_PATTERN.test(value) ||
      utf8ByteLength(value) > HEADER_OVERRIDE_MAX_VALUE_LENGTH
    ) {
      return 'invalidValue'
    }
    seen.add(lower)
  }
  if (seen.size > HEADER_OVERRIDE_MAX_ENTRIES) return 'tooManyEntries'
  return null
}

/** 行数组 → credentials 存储对象（名称小写化，丢弃空行） */
export function buildHeaderOverridesObject(rows: HeaderOverrideRow[]): Record<string, string> {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim().toLowerCase()
    if (!name) continue
    result[name] = row.value.trim()
  }
  return result
}

/** credentials 存储对象 → 行数组（按名称排序保证稳定展示） */
export function splitHeaderOverridesObject(record: unknown): HeaderOverrideRow[] {
  if (!record || typeof record !== 'object' || Array.isArray(record)) return []
  return Object.entries(record as Record<string, unknown>)
    .filter(([, value]) => typeof value === 'string')
    .map(([name, value]) => ({ name, value: value as string }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * 将请求头覆写写入 credentials。
 * create 模式：关闭时不写入任何字段；edit 模式：关闭时删除字段（全量替换语义）。
 */
export function applyHeaderOverride(
  credentials: Record<string, unknown>,
  enabled: boolean,
  rows: HeaderOverrideRow[],
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY] = true
    credentials[HEADER_OVERRIDES_CREDENTIAL_KEY] = buildHeaderOverridesObject(rows)
  } else if (mode === 'edit') {
    delete credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]
    delete credentials[HEADER_OVERRIDES_CREDENTIAL_KEY]
  }
}

// ===== OpenAI plan_type (ChatGPT 订阅档位) 手动覆盖 =====

export interface PlanTypeOption {
  value: string
  label: string
  // 兼容 common/Select.vue 的 SelectOption(含索引签名)
  [key: string]: unknown
}

/**
 * plan_type 值的友好显示标签，镜像 PlatformTypeBadge 的映射
 * （canonical 值 chatgptpro 显示为 Pro，team 显示为 Team）。未知值原样返回。
 */
export function planTypeDisplayLabel(value: string): string {
  switch (value.trim().toLowerCase()) {
    case 'plus':
      return 'Plus'
    case 'pro':
    case 'chatgptpro':
      return 'Pro'
    case 'free':
      return 'Free'
    case 'team':
      return 'Team'
    default:
      return value
  }
}

/**
 * 从凭据里读取 plan_type，仅接受字符串（脏数据 42/true 等一律视为空，
 * 避免被当作合法自定义项保留）。
 */
export function readPlanType(credentials: Record<string, unknown> | undefined | null): string {
  const v = credentials?.plan_type
  return typeof v === 'string' ? v : ''
}

/**
 * 构建 plan_type 下拉选项：清空 + Plus/Pro/Free 预设。
 * 若当前值是某预设的别名（如 chatgptpro↔Pro），用当前的 canonical 值占据该
 * 标签位（保留 canonical，显示友好标签，避免重复项）；若是完全预设外的值
 * （如 team 或异常值），追加为一项，避免编辑时下拉丢失原值。
 */
export function buildPlanTypeOptions(current: string, clearLabel: string): PlanTypeOption[] {
  const cur = (current || '').trim()
  const curLabel = cur ? planTypeDisplayLabel(cur) : ''
  const presets: PlanTypeOption[] = [
    { value: 'plus', label: 'Plus' },
    { value: 'pro', label: 'Pro' },
    { value: 'free', label: 'Free' }
  ]
  const opts: PlanTypeOption[] = [{ value: '', label: clearLabel }]
  for (const p of presets) {
    if (cur && p.value !== cur.toLowerCase() && p.label === curLabel) {
      // 当前值是该预设的别名：用 canonical 当前值占位，标签仍显示友好名
      opts.push({ value: cur, label: p.label })
    } else {
      opts.push(p)
    }
  }
  if (cur && !opts.some(o => o.value.toLowerCase() === cur.toLowerCase())) {
    opts.push({ value: cur, label: planTypeDisplayLabel(cur) })
  }
  return opts
}

/**
 * 把手动选择的 plan_type 写入凭据：非空则设置，空则删除该键（清空/自动识别）。
 * 直接修改传入对象并返回。
 */
export function applyPlanType(
  credentials: Record<string, unknown>,
  planType: string
): Record<string, unknown> {
  const pt = (planType || '').trim()
  if (pt) {
    credentials.plan_type = pt
  } else {
    delete credentials.plan_type
  }
  return credentials
}
