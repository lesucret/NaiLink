export function safeSubscriptionReason(reason: unknown): string {
  if (!reason || typeof reason !== 'object') return '请求失败，请检查网络或后台登录状态。'
  const response = (reason as { response?: { errors?: { message?: unknown }[] } }).response
  const message = response?.errors?.find((item) => typeof item.message === 'string')?.message
  // graphql-request's Error.message includes the request and variables. Only
  // display the server's bounded, credential-free subscription diagnostic.
  if (typeof message === 'string' && /^(?:Clash|SIP008|订阅)/.test(message) && !/:\/\//.test(message))
    return message.slice(0, 1500)
  if (typeof message === 'string') {
    const status = message.match(/\bHTTP (\d{3})\b/)
    if (status) return `订阅服务器返回 HTTP ${status[1]}，请检查订阅有效期和下载出口。`
  }
  return '请求失败，请检查订阅地址、网络或后台登录状态。'
}

export function subscriptionUpdateFailure(
  results: PromiseSettledResult<unknown>[],
  importing = false,
): Error | undefined {
  const failures = results.filter((result): result is PromiseRejectedResult => result.status === 'rejected')
  if (!failures.length) return undefined
  const details = [...new Set(failures.map((result) => safeSubscriptionReason(result.reason)))].slice(0, 3)
  const summary = importing
    ? '订阅导入失败，失败项未创建；成功项已导入，请只重试失败项。'
    : '订阅更新失败，失败项保留原节点。'
  return new Error(`${failures.length}/${results.length} 份${summary}\n${details.join('\n')}`)
}
