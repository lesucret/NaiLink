import { describe, expect, it, vi } from 'vitest'
import { jobActive, maintenanceAccessExpired, newerVersion, readMaintenance } from './router-maintenance'

describe('maintenance state', () => {
  it('distinguishes expired maintenance permission from a running job', async () => {
    const parent = {
      L: {
        require: async () => ({
          declare: () => async () => {
            throw Object.assign(new Error('RPC permission denied'), { code: 6 })
          },
        }),
      },
    }
    vi.stubGlobal('window', { parent })
    try {
      await readMaintenance()
      throw new Error('permission failure accepted')
    } catch (error) {
      expect(maintenanceAccessExpired(error)).toBe(true)
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it.each([
    Object.assign(new Error('Login session is expired'), { name: 'SessionError' }),
    Object.assign(new Error('failed with ubus code 6: Permission denied'), { name: 'RPCError' }),
  ])('recognizes native LuCI session/permission failures', async (failure) => {
    vi.stubGlobal('window', {
      parent: {
        L: {
          require: async () => ({
            declare: () => async () => {
              throw failure
            },
          }),
        },
      },
    })
    try {
      await expect(readMaintenance()).rejects.toSatisfy(maintenanceAccessExpired)
    } finally {
      vi.unstubAllGlobals()
    }
  })
  it('orders numeric releases without downgrading or comparing package revisions', () => {
    expect(newerVersion('0.6.33', '0.6.32-1')).toBe(true)
    expect(newerVersion('0.6.9', '0.6.32-1')).toBe(false)
    expect(newerVersion('0.6.32', '0.6.32-1')).toBe(false)
    expect(newerVersion('not-a-version', '0.6.32')).toBe(false)
  })
  it('polls queued/running tasks and stops for terminal states', () => {
    expect(jobActive()).toBe(false)
    for (const state of ['queued', 'running', 'success', 'error'] as const)
      expect(jobActive({ id: 'a', action: 'update', state, phase: 'installing', started: 0, updated: 0 })).toBe(
        ['queued', 'running'].includes(state),
      )
  })
})
