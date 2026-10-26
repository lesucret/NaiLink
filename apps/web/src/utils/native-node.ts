// Keep native options intact instead of translating them through an unrelated form.
export function nativeNodeJSON(link: string): string | undefined {
  if (!link.startsWith('singbox://')) return undefined
  try {
    const encoded = link.slice('singbox://'.length).replace(/-/g, '+').replace(/_/g, '/')
    const bytes = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0))
    return JSON.stringify(JSON.parse(new TextDecoder().decode(bytes)), null, 2)
  } catch {
    return undefined
  }
}
