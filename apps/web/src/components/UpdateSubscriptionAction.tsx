import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useUpdateSubscriptionsMutation } from '~/apis'
import { Button } from '~/components/ui/button'

export function UpdateSubscriptionAction({ id, loading, quiet }: { id: string; loading?: boolean; quiet?: boolean }) {
  const { t } = useTranslation()
  const updateSubscriptionsMutation = useUpdateSubscriptionsMutation()

  return (
    <div className="space-y-2">
      <Button
        variant={quiet ? 'ghost' : 'outline'}
        size="sm"
        loading={loading || updateSubscriptionsMutation.isPending}
        onClick={() =>
          updateSubscriptionsMutation.mutate([id], { onSuccess: () => toast.success(t('panel.updateSuccess')) })
        }
      >
        <RefreshCw aria-hidden="true" className="h-4 w-4" />
        {updateSubscriptionsMutation.isError ? t('panel.retry') : t('panel.updateSubscription')}
      </Button>
      {updateSubscriptionsMutation.isError && (
        <p role="alert" className="max-w-lg whitespace-pre-line break-words text-xs text-destructive">
          {updateSubscriptionsMutation.error?.message || t('panel.updateFailed')}
        </p>
      )}
    </div>
  )
}
