import { get, post, put, del } from './http'
import type { SystemStatus, Task, FileEntry, CookieCloudStatus } from '../types'

export const getSystemStatus = () => get<SystemStatus>('/system/status')
export const getDashboardSummary = () => get<SystemStatus>('/dashboard/summary')
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
