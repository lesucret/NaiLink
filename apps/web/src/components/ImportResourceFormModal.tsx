import { zodResolver } from '@hookform/resolvers/zod'
import { Minus, Plus } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useFieldArray, useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { FormActions } from '~/components/FormActions'
import { Button } from '~/components/ui/button'
import { Dialog, DialogTitle } from '~/components/ui/dialog'
import { Input } from '~/components/ui/input'
import {
  ScrollableDialogBody,
  ScrollableDialogContent,
  ScrollableDialogHeader,
} from '~/components/ui/scrollable-dialog'
import { useSetValue } from '~/hooks/useSetValue'
import { routerMode } from '~/utils/router-mode'
import { safeSubscriptionReason } from '~/utils/subscription-update'
import { NodeProtocolGuide } from './NodeProtocolGuide'

const schema = z.object({
  resources: z
    .array(
      z.object({
        link: z.string().trim().min(1, 'Link is required'),
        tag: z.string().trim(),
      }),
    )
    .min(1, 'At least one resource is required'),
})

type FormValues = z.infer<typeof schema>

const defaultValues: FormValues = {
  resources: [{ link: '', tag: '' }],
}

export function ImportResourceFormModal({
  title,
  opened,
  onClose,
  handleSubmit: onSubmitProp,
  resourceKind,
}: {
  title: string
  opened: boolean
  onClose: () => void
  handleSubmit: (values: FormValues) => Promise<void>
  resourceKind?: 'node'
}) {
  const { t } = useTranslation()
  const [submitFailed, setSubmitFailed] = useState(false)
  const [failureReason, setFailureReason] = useState('')

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues,
    mode: 'onChange',
  })

  const {
    handleSubmit,
    control,
    setValue: setValueOriginal,
    reset,
    formState: { errors, isDirty, isSubmitting },
  } = form

  const setValue = useSetValue(setValueOriginal)

  const { fields, append, remove } = useFieldArray({
    control,
    name: 'resources',
  })

  const resources = useWatch({ control, name: 'resources' })

  // Reset form when modal closes (with delay for animation)
  const handleClose = useCallback(() => {
    if (isSubmitting) return
    setSubmitFailed(false)
    onClose()
    setTimeout(() => {
      reset(defaultValues)
    }, 200)
  }, [onClose, reset, isSubmitting])

  const onSubmit = async (data: FormValues) => {
    setSubmitFailed(false)
    try {
      await onSubmitProp(data)
      onClose()
      reset(defaultValues)
    } catch (error) {
      const message = error instanceof Error ? error.message : ''
      setFailureReason(
        /^\d+\/\d+ 份订阅导入失败/.test(message) && !/:\/\//.test(message) ? message : safeSubscriptionReason(error),
      )
      setSubmitFailed(true)
    }
  }

  return (
    <Dialog open={opened} onOpenChange={handleClose}>
      <ScrollableDialogContent
        size="lg"
        onEscapeKeyDown={(event) => {
          if (isSubmitting) event.preventDefault()
        }}
        onPointerDownOutside={(event) => {
          if (isSubmitting) event.preventDefault()
        }}
      >
        <ScrollableDialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </ScrollableDialogHeader>
        <ScrollableDialogBody>
          {resourceKind === 'node' && routerMode && <NodeProtocolGuide opened={opened} />}
          {resourceKind !== 'node' && routerMode && (
            <p className="mb-4 text-sm leading-relaxed text-muted-foreground">
              支持 Clash YAML / JSON（含 proxies）、SIP008 和普通 / Base64 节点订阅。仅导入节点，保留现有 DNS
              与分流设置。
            </p>
          )}
          <form onSubmit={handleSubmit(onSubmit)}>
            {submitFailed && (
              <p role="alert" className="mb-3 whitespace-pre-line break-words text-sm text-destructive">
                {resourceKind !== 'node' && routerMode ? failureReason : t('panel.saveFailed')}
              </p>
            )}
            <fieldset disabled={isSubmitting}>
              <div className="flex flex-col gap-5">
                {fields.map((field, i) => (
                  <div
                    key={field.id}
                    className="grid min-w-0 grid-cols-[minmax(0,1fr)_36px] items-end gap-2.5 sm:grid-cols-[minmax(0,1fr)_176px_36px]"
                  >
                    <Input
                      wrapperClassName="min-w-0 col-span-2 sm:col-span-1"
                      withAsterisk
                      label={t('link')}
                      value={resources[i]?.link || ''}
                      onChange={(e) => setValue(`resources.${i}.link`, e.target.value)}
                      error={errors.resources?.[i]?.link?.message}
                    />
                    <Input
                      wrapperClassName="min-w-0"
                      label={t('importResource.optionalTag')}
                      placeholder={t('importResource.autoName')}
                      value={resources[i]?.tag || ''}
                      onChange={(e) => setValue(`resources.${i}.tag`, e.target.value)}
                      error={errors.resources?.[i]?.tag?.message}
                    />

                    <div className="flex flex-col">
                      {/* Spacer for label height */}
                      <div className="h-[22px]" />
                      <Button
                        type="button"
                        variant="destructive"
                        size="icon"
                        className="h-9 w-9 shrink-0"
                        onClick={() => remove(i)}
                        disabled={fields.length === 1 || isSubmitting}
                        aria-label={t('a11y.removeItem')}
                      >
                        <Minus className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                ))}
              </div>

              <div className="flex items-center justify-between mt-5">
                <Button
                  type="button"
                  variant="default"
                  size="icon"
                  className="bg-green-600 hover:bg-green-700"
                  onClick={() => append({ link: '', tag: '' })}
                  aria-label={t('a11y.addItem')}
                >
                  <Plus className="h-4 w-4" />
                </Button>

                <FormActions
                  reset={() => reset(defaultValues)}
                  isDirty={isDirty}
                  errors={errors}
                  loading={isSubmitting}
                />
              </div>
            </fieldset>
          </form>
        </ScrollableDialogBody>
      </ScrollableDialogContent>
    </Dialog>
  )
}
