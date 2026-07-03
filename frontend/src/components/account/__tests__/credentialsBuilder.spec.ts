import { describe, it, expect } from 'vitest'
import {
  ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY,
  applyAntigravityProjectID,
  applyInterceptWarmup,
  buildKiroCredentials,
  validateKiroCredentials,
  type KiroCredentialInputs
} from '../credentialsBuilder'

const baseKiroInputs = (): KiroCredentialInputs => ({
  accountType: 'oauth',
  authMethod: 'social',
  accessToken: '',
  refreshToken: '',
  profileArn: '',
  region: '',
  authRegion: '',
  apiRegion: '',
  machineId: '',
  clientId: '',
  clientSecret: '',
  apiKey: ''
})

describe('applyInterceptWarmup', () => {
  it('create + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, true, 'create')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  it('create + enabled=false: should not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, false, 'create')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('edit + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, true, 'edit')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  it('edit + enabled=false + field exists: should delete the field', () => {
    const creds: Record<string, unknown> = { api_key: 'sk', intercept_warmup_requests: true }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('edit + enabled=false + field absent: should not throw', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('should not affect other fields', () => {
    const creds: Record<string, unknown> = {
      api_key: 'sk',
      base_url: 'url',
      intercept_warmup_requests: true
    }
    applyInterceptWarmup(creds, false, 'edit')
    expect(creds.api_key).toBe('sk')
    expect(creds.base_url).toBe('url')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })
})

describe('applyAntigravityProjectID', () => {
  it('create + project id: trims and stores configured project fallback', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '  configured-project  ', 'create')
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })

  it('create + empty project id: should not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '   ', 'create')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  it('edit + empty project id: deletes existing fallback', () => {
    const creds: Record<string, unknown> = {
      access_token: 'tok',
      [ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]: 'old-project'
    }
    applyAntigravityProjectID(creds, '', 'edit')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  it('does not affect onboard project_id or other credentials', () => {
    const creds: Record<string, unknown> = {
      project_id: 'onboard-project',
      model_mapping: { 'gemini-*': 'gemini-2.5-flash' }
    }
    applyAntigravityProjectID(creds, 'configured-project', 'edit')
    expect(creds.project_id).toBe('onboard-project')
    expect(creds.model_mapping).toEqual({ 'gemini-*': 'gemini-2.5-flash' })
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })
})

describe('buildKiroCredentials', () => {
  it('apikey: emits only kiro_api_key', () => {
    const creds = buildKiroCredentials({
      ...baseKiroInputs(),
      accountType: 'apikey',
      apiKey: '  kiro-key-123  '
    })
    expect(creds).toEqual({ kiro_api_key: 'kiro-key-123' })
  })

  it('oauth social: sets auth_method and only non-empty fields', () => {
    const creds = buildKiroCredentials({
      ...baseKiroInputs(),
      authMethod: 'social',
      refreshToken: 'rt-1',
      accessToken: 'at-1',
      profileArn: 'arn:aws:codewhisperer:profile/x',
      region: 'us-east-1',
      machineId: 'machine-1'
    })
    expect(creds).toEqual({
      auth_method: 'social',
      refresh_token: 'rt-1',
      access_token: 'at-1',
      profile_arn: 'arn:aws:codewhisperer:profile/x',
      region: 'us-east-1',
      machine_id: 'machine-1'
    })
    expect('client_id' in creds).toBe(false)
    expect('client_secret' in creds).toBe(false)
    expect('expires_at' in creds).toBe(false)
  })

  it('oauth idc: includes client credentials', () => {
    const creds = buildKiroCredentials({
      ...baseKiroInputs(),
      authMethod: 'idc',
      refreshToken: 'rt-2',
      clientId: 'client-1',
      clientSecret: 'secret-1',
      authRegion: 'us-west-2',
      apiRegion: 'eu-west-1'
    })
    expect(creds).toMatchObject({
      auth_method: 'idc',
      refresh_token: 'rt-2',
      client_id: 'client-1',
      client_secret: 'secret-1',
      auth_region: 'us-west-2',
      api_region: 'eu-west-1'
    })
  })

  it('oauth social: ignores client credentials even if present', () => {
    const creds = buildKiroCredentials({
      ...baseKiroInputs(),
      authMethod: 'social',
      refreshToken: 'rt-3',
      clientId: 'client-x',
      clientSecret: 'secret-x'
    })
    expect('client_id' in creds).toBe(false)
    expect('client_secret' in creds).toBe(false)
  })
})

describe('validateKiroCredentials', () => {
  it('create apikey without key: returns error key', () => {
    expect(validateKiroCredentials({ ...baseKiroInputs(), accountType: 'apikey' }, 'create'))
      .toBe('admin.accounts.kiro.errors.apiKeyRequired')
  })

  it('create oauth without refresh token: returns error key', () => {
    expect(validateKiroCredentials({ ...baseKiroInputs() }, 'create'))
      .toBe('admin.accounts.kiro.errors.refreshTokenRequired')
  })

  it('create idc without client id/secret: returns error keys in order', () => {
    expect(
      validateKiroCredentials(
        { ...baseKiroInputs(), authMethod: 'idc', refreshToken: 'rt' },
        'create'
      )
    ).toBe('admin.accounts.kiro.errors.clientIdRequired')
    expect(
      validateKiroCredentials(
        { ...baseKiroInputs(), authMethod: 'idc', refreshToken: 'rt', clientId: 'c' },
        'create'
      )
    ).toBe('admin.accounts.kiro.errors.clientSecretRequired')
  })

  it('create valid social oauth: returns null', () => {
    expect(validateKiroCredentials({ ...baseKiroInputs(), refreshToken: 'rt' }, 'create')).toBeNull()
  })

  it('edit mode: never errors (blank keeps existing)', () => {
    expect(validateKiroCredentials({ ...baseKiroInputs(), accountType: 'apikey' }, 'edit')).toBeNull()
    expect(validateKiroCredentials({ ...baseKiroInputs(), authMethod: 'idc' }, 'edit')).toBeNull()
  })
})
