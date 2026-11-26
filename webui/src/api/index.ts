import { get, post, put, del } from './http'
import type { SystemStatus, Task, FileEntry, CookieCloudStatus, PendingWebDAVUpload, ListeningReport } from '../types'

export const getSystemStatus = () => get<SystemStatus>('/system/status')
export const getDashboardSummary = () => get<SystemStatus>('/dashboard/summary')
export const getListeningReport = (month?: string) => get<ListeningReport>('/listening/report', month ? { month } : undefined)
export const getPendingWebDAVUploads = () => get<{ items: PendingWebDAVUpload[]; count: number }>('/webdav/pending')
export const getAuthSession = () => get<AuthSession>('/auth/session')
export const login = (username: string, password: string) => post<AuthSession>('/auth/login', { username, password })
export const logout = () => post('/auth/logout')
export const getQQLoginOverview = () => get<QQLoginOverview>('/qqmusic/status')
export const getSystemConfig = () => get('/system/config')
export const updateSystemConfig = (config: Record<string, any>, clearSecrets: string[] = []) => put('/system/config', { config, clear_secrets: clearSecrets })
export const requestApplicationRestart = () => post<{ restarting: boolean }>('/system/restart')
export const submitTask = (url: string, forceDownload = false) => post<TaskSubmitResponse>('/task/submit', { url, force_download: forceDownload })
export const getActiveTasks = () => get<Task[]>('/task/active')
export interface TaskHistoryFilters {
  q?: string
  platform?: string
  status?: string
  from?: string
  to?: string
}
export const getTaskHistory = (offset = 0, limit = 20, filters: TaskHistoryFilters = {}) =>
  get<{ items: Task[]; total: number }>('/task/history', { offset, limit, ...filters })
export const cancelTask = (id: string) => del(`/task/${id}`)
export const listFiles = (path = '/', q = '', field = 'all') => get<{ path: string; entries: FileEntry[]; target: 'local' | 'webdav'; truncated?: boolean; searching?: boolean }>('/files', { path, q: q || undefined, field })
export const getFileStreamUrl = (path: string) => `/api/web/files/stream?path=${encodeURIComponent(path)}`
export const deleteFile = (path: string) => del('/files', { path })
export const getCookieCloudStatus = () => get<CookieCloudStatus>('/cookiecloud/status')
export const syncCookieCloud = () => post('/cookiecloud/sync')
export const getCronJobs = () => get<CronJobs>('/cron/jobs')
export const runCronJob = (name: string) => post<{ name: string; started: boolean }>(`/cron/jobs/${encodeURIComponent(name)}/run`)

export interface CronJob {
  name: string
  label: string
  schedule: string
  running: boolean
  last_run?: string
  next_run?: string
  last_error?: string
}

export interface CronJobs {
  enabled: boolean
  jobs: CronJob[]
}

export interface AuthSession {
  auth_enabled: boolean
  authenticated: boolean
  username?: string
}

export interface QQLoginOverview {
  enabled: boolean
  status: 'disabled' | 'logged_in' | 'logged_out' | 'unknown'
  account?: string
  message?: string
}

export interface DuplicateCheckResult {
  status: 'duplicate' | 'clear' | 'unknown' | 'not_applicable'
  reason?: string
  target?: { name: string; artist: string; album: string; ext: string }
  match?: { path: string; name: string; ext: string }
}
export interface TaskSubmitResponse extends Task {
  confirmation_required?: boolean
  duplicate?: DuplicateCheckResult
}

export interface DiscoveryTrack {
  platform: string
  id: string
  url: string
  title: string
  artist: string
  album?: string
  duration?: number
  plays?: number
  last_played?: string
  verified: boolean
}

export interface RediscoverResult {
  tracks: DiscoveryTrack[]
  skipped_owned: number
  unconfirmed: number
  skipped_shown: number
  skipped_pending: number
  skipped_dismissed: number
  skipped_queued: number
  skipped_artist_cap: number
  skipped_duplicate: number
  skipped_other: number
  unavailable: number
  requested: number
  notice: string
}

export interface DiscoveryEnqueueResult {
  platform: string
  id: string
  state: 'queued' | 'skipped' | 'failed'
  reason?: string
}

export const rediscoverTracks = (platform: string, limit: number) =>
  post<RediscoverResult>('/discovery/rediscover', { platform, limit })
export const enqueueDiscoveryTracks = (tracks: DiscoveryTrack[]) =>
  post<{ queued: number; skipped: number; failed: number; results: DiscoveryEnqueueResult[] }>('/discovery/enqueue', { tracks })
export const dismissDiscoveryTracks = (tracks: DiscoveryTrack[]) =>
  post<{ dismissed: number }>('/discovery/dismiss', { tracks })
export const restoreDiscoveryTracks = (tracks: DiscoveryTrack[]) =>
  post<{ restored: number }>('/discovery/restore', { tracks })

export interface DiscoveryJob {
  platform: string
  id: string
  title: string
  artist: string
  state: string
  label: string
  error?: string
}

export const listDiscoveryJobs = () => get<{ jobs: DiscoveryJob[] }>('/discovery/jobs')

export interface FavoritePlatform {
  platform: string
  label: string
  bound: boolean
  account?: string
  last_sync?: string
  paused: boolean
  error?: string
  state: string
}

export interface FavoriteQueueCount {
  state: string
  label: string
  count: number
}

export interface FavoriteJob {
  id: string
  platform: string
  track_id: string
  title: string
  artist: string
  state: string
  label: string
  error?: string
}

export interface FavoritesPage {
  enabled: boolean
  poll_minutes: number
  notice?: string
  platforms: FavoritePlatform[]
  queue: FavoriteQueueCount[]
  pending: FavoriteJob[]
  ingest_exceptions: FavoriteJob[]
}

export interface FavoriteSample {
  title: string
  artist: string
}

export interface FavoritePreview {
  token: string
  platform: string
  account: string
  count: number
  enabled: boolean
  samples?: FavoriteSample[]
}

export const getFavorites = () => get<FavoritesPage>('/discovery/favorites')
export const previewFavorites = (platform: string) => post<FavoritePreview>('/discovery/favorites/preview', { platform })
export const bindFavorites = (token: string) => post<FavoritesPage>('/discovery/favorites/bind', { token })
export const pauseFavorites = (platform: string, paused: boolean) => post<FavoritesPage>('/discovery/favorites/pause', { platform, paused })
export const syncFavorites = () => post<FavoritesPage>('/discovery/favorites/sync')
export const retryFavorites = () => post<FavoritesPage>('/discovery/favorites/retry')
export const refreshFavorites = () => post<FavoritesPage>('/discovery/favorites/refresh')
export const resolveFavorite = (id: string, accept: boolean) => post<FavoritesPage>('/discovery/favorites/resolve', { id, accept })

export interface PlaylistDescription {
  id: string
  name: string
  desc: string
}

export const listPlaylistDescriptions = () =>
  get<{ synced: boolean; items: PlaylistDescription[] }>('/playlists')
export const updatePlaylistDescription = (id: string, desc: string) =>
  put<{ id: string; desc: string }>(`/playlists/${encodeURIComponent(id)}/desc`, { desc })
export const syncPlaylistCatalog = () =>
  post<{ added: number; updated: number; inactive: number; synced: boolean }>('/playlists/sync')

export interface AIPreset {
  id: string
  label: string
  text: string
}

export interface AIPlaylistItem {
  id: string
  name: string
  description: string
  song_count: number
}

export interface AIPlaylistPage {
  presets: AIPreset[]
  playlists: AIPlaylistItem[]
  warning?: string
}

export interface AIPreviewSong {
  title: string
  artist: string
  reason?: string
}

export interface AIPreviewPage {
  token: string
  name: string
  description: string
  intro: string
  total: number
  preview: AIPreviewSong[]
  used_fallback: boolean
  creating: boolean
  existing: number
}

export interface AIConfirmPage {
  name: string
  count: number
  intro: string
  created: boolean
  replaced: boolean
  used_fallback: boolean
}

export const listAIPlaylists = () => get<AIPlaylistPage>('/ai/playlists')
export const previewAIPlaylist = (body: { playlist_id?: string; name?: string; request: string; preset: string; seed?: string }) =>
  post<AIPreviewPage>('/ai/playlists/preview', body)
export const confirmAIPlaylist = (token: string) => post<AIConfirmPage>('/ai/playlists/confirm', { token })

export interface LibraryDuplicateTrack {
  path: string
  name: string
  ext: string
  size: number
  mod_unix: number
  lyric_evidence?: 'present' | 'explicit_instrumental' | 'unknown'
}

export interface LibraryDuplicateGroup {
  kind: 'same' | 'similar' | string
  tracks: LibraryDuplicateTrack[]
}

export interface LibraryDuplicateReport {
  status: string
  scanned: number
  total: number
  skipped: number
  error?: string
  updated_at?: string
  groups: LibraryDuplicateGroup[]
  lyric_conflicts?: LibraryDuplicateGroup[]
  lyric_conflict_count?: number
}

export const getLibraryDuplicates = () => get<LibraryDuplicateReport>('/duplicates')
export const startLibraryDuplicateScan = () => post<LibraryDuplicateReport>('/duplicates/scan')
export const keepLibraryDuplicate = (keep: string, remove: string[]) => post<{ kept: string; removed: string[] }>('/duplicates/keep', { keep, remove })
