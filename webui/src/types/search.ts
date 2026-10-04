export interface SearchResultItem {
  platform: 'netease' | 'qq' | 'youtube' | 'bilibili'
  song_id: string
  song_mid?: string
  name: string
  artists: string
  album: string
  duration_sec: number
  url: string
  is_vip: boolean
}

export interface SearchResponse {
  items: SearchResultItem[]
  total: number
  errors: Record<string, string>
  keyword: string
}
