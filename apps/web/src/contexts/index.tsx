import type { ClientError } from 'graphql-request'
import type { GQLClientInterface } from '~/utils/graphql-client'
import { useStore } from '@nanostores/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { GraphQLClient } from 'graphql-request'
import { createContext, use, useEffect, useMemo } from 'react'
import { toast } from 'sonner'

import { isMockMode, MockGraphQLClient } from '~/mocks'
import { endpointURLAtom, routerSessionExpiredAtom, tokenAtom } from '~/store'
import { cancellableGraphQLClient } from '~/utils/graphql-client'
import { routerMode } from '~/utils/router-mode'

export type { GQLClientInterface } from '~/utils/graphql-client'

// Use an interface type to support both real and mock clients
export type GQLClient = GQLClientInterface

export const GQLClientContext = createContext<GQLClient>(null as unknown as GQLClient)

export function GQLQueryClientProvider({ client, children }: { client: GQLClient; children: React.ReactNode }) {
  return <GQLClientContext value={client}>{children}</GQLClientContext>
}

export const useGQLQueryClient = () => use(GQLClientContext)

type ColorScheme = 'dark' | 'light'
type ThemeMode = 'system' | 'light' | 'dark'

interface ColorSchemeContextValue {
  colorScheme: ColorScheme
  themeMode: ThemeMode
  setThemeMode: (mode: ThemeMode) => void
}

export const ColorSchemeContext = createContext<ColorSchemeContextValue>({
  colorScheme: 'light',
  themeMode: 'system',
  setThemeMode: () => {},
})

export const useColorScheme = () => use(ColorSchemeContext)

interface QueryProviderProps {
  children: React.ReactNode
  colorScheme: ColorScheme
  themeMode: ThemeMode
  setThemeMode: (mode: ThemeMode) => void
}

export function QueryProvider({ children, colorScheme, themeMode, setThemeMode }: QueryProviderProps) {
  const endpointURL = useStore(endpointURLAtom)
  const token = useStore(tokenAtom)

  // One cache per endpoint and token. Query observers keep the client they
  // were created with, so the provider is keyed too: an identity change
  // remounts the consumers instead of leaving them on the retired cache.
  const identity = `${endpointURL}|${token}`
  const queryClient = useMemo(() => new QueryClient(), [identity])

  useEffect(() => () => queryClient.clear(), [queryClient])

  const gqlClient = useMemo<GQLClient>(() => {
    // Use mock client in mock mode
    if (isMockMode()) {
      return new MockGraphQLClient('mock://localhost')
    }

    return cancellableGraphQLClient(
      new GraphQLClient(endpointURL, {
        credentials: routerMode ? 'same-origin' : undefined,
        headers: {
          authorization: `Bearer ${token}`,
        },
        responseMiddleware: (response) => {
          const status = (response as ClientError).response?.status
          if (routerMode && (status === 401 || status === 403)) routerSessionExpiredAtom.set(true)
          const error = (response as ClientError).response?.errors?.[0]

          if (error) {
            if (!/^latency_job_|^test_job_|^core_changed$|^invalid_test_job$/.test(error.message))
              toast.error(error.message)

            if (error.message === 'access denied') {
              if (routerMode) routerSessionExpiredAtom.set(true)
              else tokenAtom.set('')
            }
          }
        },
      }),
    )
  }, [endpointURL, token])

  const colorSchemeContextValue = useMemo(
    () => ({ colorScheme, themeMode, setThemeMode }),
    [colorScheme, themeMode, setThemeMode],
  )

  return (
    <ColorSchemeContext value={colorSchemeContextValue}>
      <QueryClientProvider key={identity} client={queryClient}>
        <GQLQueryClientProvider client={gqlClient}>{children}</GQLQueryClientProvider>
      </QueryClientProvider>
    </ColorSchemeContext>
  )
}
