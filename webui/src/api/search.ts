import type { SearchResultItem } from '../types/search'

export interface SearchPlatformEvent {
  platform: SearchResultItem['platform']
  status: 'searching' | 'partial' | 'complete' | 'error'
  items?: SearchResultItem[]
  error?: string
  truncated?: boolean
}

export interface SearchCompleteEvent {
  items: SearchResultItem[]
  total: number
  has_more: boolean
  truncated?: boolean
  errors: Record<string, string>
  keyword: string
}

export interface SearchStreamParams {
  keyword: string
  platform: string[]
  offset: number
  limit: number
  filterQuery: string
  filterPlatform: string
  filterVip: string
}

export async function streamSearch(
  params: SearchStreamParams,
  onPlatform: (event: SearchPlatformEvent) => void,
  onComplete: (event: SearchCompleteEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const query = new URLSearchParams({
    keyword: params.keyword,
    platform: params.platform.join(','),
    offset: String(params.offset),
    limit: String(params.limit),
    filter_query: params.filterQuery,
    filter_platform: params.filterPlatform,
    filter_vip: params.filterVip,
  })
  const response = await fetch(`/api/web/search/stream?${query}`, {
    credentials: 'same-origin',
    headers: { Accept: 'text/event-stream' },
    signal,
    cache: 'no-store',
  })
  if (response.status === 401) {
    window.dispatchEvent(new CustomEvent('gymdl:unauthorized'))
  }
  if (!response.ok) {
    let message = `搜索失败（HTTP ${response.status}）`
    try {
      const body = await response.json() as { message?: string }
      if (body.message) message = body.message
    } catch { /* keep HTTP fallback */ }
    throw new Error(message)
  }
  if (!response.body) throw new Error('浏览器不支持搜索结果流')

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let completed = false

  const consumeBlock = (block: string) => {
    let eventName = 'message'
    const data: string[] = []
    for (const line of block.split(/\r?\n/)) {
      if (!line || line.startsWith(':')) continue
      if (line.startsWith('event:')) eventName = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
    }
    if (data.length === 0) return
    const payload = JSON.parse(data.join('\n')) as SearchPlatformEvent | SearchCompleteEvent
    if (eventName === 'platform') onPlatform(payload as SearchPlatformEvent)
    else if (eventName === 'complete') {
      completed = true
      onComplete(payload as SearchCompleteEvent)
    }
  }

  try {
    while (!completed) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      while (true) {
        const boundary = /\r?\n\r?\n/.exec(buffer)
        if (!boundary || boundary.index === undefined) break
        const block = buffer.slice(0, boundary.index)
        buffer = buffer.slice(boundary.index + boundary[0].length)
        consumeBlock(block)
        if (completed) break
      }
    }
    if (!completed && buffer.trim()) consumeBlock(buffer)
    if (!completed && !signal.aborted) throw new Error('搜索结果流意外中断')
  } finally {
    await reader.cancel().catch(() => {})
  }
}
