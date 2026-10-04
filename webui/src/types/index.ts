export type TaskStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'

export interface SongInfo {
  SongName: string
  SongArtists: string
  SongAlbum: string
  FileExt: string
  MusicSize: number
  Bitrate: string
  Duration: number
}

export interface Task {
  id: string
  url: string
  platform: string
  status: TaskStatus
  progress: string
  error?: string
  song_info?: SongInfo[]
  created_at: string
  updated_at: string
}

export interface ServiceStatus {
  name: string
  enabled: boolean
  online: boolean
  detail: string
}

export interface SystemStatus {
  services: ServiceStatus[]
  version: string
  go_version: string
  uptime: number
  checked_at?: string
  restart_supported?: boolean
  metrics?: {
    pending: number
    running: number
    completed_24h: number
    failed_24h: number
    total_history: number
  }
}

export interface FileEntry {
  name: string
  path: string
  is_dir: boolean
  size: number
  mod_time: string
  ext: string
  artist?: string
  album?: string
}

export interface CookieCloudStatus {
  available: boolean
  mode: number
  expire_min: number
  cookie_path: string
  file_exists: boolean
  file_mod_time?: string
}
