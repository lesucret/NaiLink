import { describe, expect, it } from 'vitest'
import { nativeNodeJSON } from './native-node'

describe('native node editor', () => {
  it('preserves Unicode credentials and nested protocol fields', () => {
    const value = { type: 'ssh', tag: '测试节点', private_key: ['key\nvalue'], password: '密码' }
    const encoded = btoa(String.fromCharCode(...new TextEncoder().encode(JSON.stringify(value))))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '')
    expect(JSON.parse(nativeNodeJSON(`singbox://${encoded}`)!)).toEqual(value)
    expect(nativeNodeJSON('singbox://broken')).toBeUndefined()
    expect(nativeNodeJSON('vless://existing')).toBeUndefined()
  })
})
