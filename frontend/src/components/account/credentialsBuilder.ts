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
