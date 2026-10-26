import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useGQLQueryClient } from '~/contexts'

interface Protocol {
  name: string
  formats: string
  available: boolean
  note?: string
}
interface Capabilities {
  current: string
  engines: { id: string; version: string; protocols: Protocol[] }[]
}

export function NodeProtocolGuide({ opened }: { opened: boolean }) {
  const [selected, setSelected] = useState('')
  const client = useGQLQueryClient()
  const query = useQuery({
    queryKey: ['core-protocol-capabilities'],
    enabled: opened,
    staleTime: 2_000,
    refetchInterval: opened ? 3_000 : false,
    refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const data = await client.request<{ coreCapabilities: string }>('{coreCapabilities}', undefined, signal)
      return JSON.parse(data.coreCapabilities) as Capabilities
    },
  })
  const current = query.data?.engines.find((e) => e.id === query.data?.current)
  const selection = current?.protocols.find((p) => p.name === selected && p.available)
  return (
    <div className="mb-4 rounded-xl border bg-muted/30 p-3 text-xs" data-protocol-guide>
      <p className="mb-2 font-medium">
        当前内核：{query.data?.current || '正在读取…'} {current?.version}
      </p>
      {query.isError && <p role="alert">暂时无法读取协议范围，请重试。现有节点不受影响。</p>}
      <div className="flex flex-wrap gap-1.5">
        {current?.protocols
          .filter((p) => p.available)
          .map((p) => (
            <button
              key={p.name}
              type="button"
              aria-pressed={selected === p.name}
              onClick={() => setSelected(p.name)}
              className="rounded-md border bg-background px-2 py-1 hover:border-primary"
              title={`${p.formats}${p.note ? ` · ${p.note}` : ''}`}
            >
              {p.name}
            </button>
          ))}
      </div>
      {selection && (
        <p role="status" className="mt-2 text-muted-foreground">
          {selection.name}：{selection.formats} {selection.note}
        </p>
      )}
      {query.data?.current === 'sing-box' && (
        <p className="mt-2 text-muted-foreground">
          支持普通链接，也可粘贴 sing-box 单个出站 JSON 或含 outbounds 的出站组合。原生 JSON 节点仅供 sing-box 使用。
        </p>
      )}
      {!!current?.protocols.some((p) => !p.available) && (
        <details className="mt-2 text-muted-foreground">
          <summary className="cursor-pointer">查看组件限制和暂不支持的协议</summary>
          <ul className="mt-2 space-y-1">
            {current.protocols
              .filter((p) => !p.available)
              .map((p) => (
                <li key={p.name}>
                  {p.name}：{p.note}
                </li>
              ))}
          </ul>
        </details>
      )}
    </div>
  )
}
