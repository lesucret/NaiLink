import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { jobActive, maintenanceAccessExpired, readMaintenance, startMaintenance } from '~/utils/router-maintenance'

export function useRouterMaintenance() {
  const client = useQueryClient()
  const query = useQuery({
    queryKey: ['router-maintenance'],
    queryFn: readMaintenance,
    staleTime: 30000,
    retry: false,
    refetchInterval: (query) =>
      !maintenanceAccessExpired(query.state.error) &&
      (jobActive(query.state.data?.job) || query.state.data?.job?.code === 'apply_unconfirmed')
        ? 2000
        : false,
  })
  const mutation = useMutation({
    mutationFn: startMaintenance,
    onSuccess: (result) => {
      client.setQueryData(['router-maintenance'], (previous: typeof result | undefined) => ({ ...previous, ...result }))
      client.invalidateQueries({ queryKey: ['router-maintenance'] })
    },
  })
  return {
    query,
    mutation,
    busy: mutation.isPending || jobActive(query.data?.job) || query.data?.job?.code === 'apply_unconfirmed',
  }
}
