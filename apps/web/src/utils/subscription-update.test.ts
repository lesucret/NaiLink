import { describe, expect, it } from 'vitest'
import { subscriptionUpdateFailure } from './subscription-update'

describe('subscription update diagnostics', () => {
  it('preserves content diagnostics alongside partial batch success', () => {
    const error = subscriptionUpdateFailure([
      { status: 'fulfilled', value: {} },
      {
        status: 'rejected',
        reason: { response: { errors: [{ message: 'Clash 订阅有 2 项无法转换，未替换原节点。' }] } },
      },
    ])
    expect(error?.message).toContain('1/2')
    expect(error?.message).toContain('Clash 订阅有 2 项')
  })
  it('never serializes credentials, request variables or provider URLs', () => {
    const error = subscriptionUpdateFailure([
      { status: 'rejected', reason: new Error('https://private-token.invalid?key=private-token') },
      {
        status: 'rejected',
        reason: { response: { errors: [{ message: 'subscription HTTP 403: https://private-token.invalid' }] } },
      },
      {
        status: 'rejected',
        reason: { response: { errors: [{ message: '订阅: https://private-token.invalid' }] } },
      },
    ])
    expect(error?.message).toContain('HTTP 403')
    expect(error?.message).not.toContain('private-token')
  })
  it('does not turn successful batches into errors', () => {
    expect(subscriptionUpdateFailure([{ status: 'fulfilled', value: {} }])).toBeUndefined()
  })
  it('distinguishes partial imports from updates', () => {
    const error = subscriptionUpdateFailure(
      [
        { status: 'fulfilled', value: {} },
        { status: 'rejected', reason: {} },
      ],
      true,
    )
    expect(error?.message).toContain('1/2 份订阅导入失败')
    expect(error?.message).toContain('只重试失败项')
    expect(error?.message).not.toContain('保留原节点')
  })
})
