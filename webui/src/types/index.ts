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
  pending_webdav_uploads?: number
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

export interface PendingWebDAVUpload {
  id: string
  remote_dir: string
  file_name: string
  created_at: string
  attempts: number
  last_attempt?: string
  last_error?: string
}

export interface CookieCloudStatus {
  available: boolean
  mode: number
  expire_min: number
  cookie_path: string
  file_exists: boolean
  file_mod_time?: string
}

export interface ListeningArtist {
  name: string
  image?: string
  plays: number
  minutes: number
}

export interface ListeningTrack {
  name: string
  artist: string
  album?: string
  image?: string
  plays: number
  minutes: number
}

export interface ListeningAlbum {
  name: string
  artist: string
  image?: string
  plays: number
}

export interface ListeningMonthPoint {
  month: string
  plays: number
  artists: number
}

export interface ListeningDay {
  date: string
  plays: number
}

export interface ListeningHour {
  hour: number
  plays: number
}

export interface ListeningReport {
  available: boolean
  month: string
  month_label: string
  scrobbles: number
  prev_scrobbles: number
  artists: number
  tracks: number
  albums: number
  minutes: number
  top_artists: ListeningArtist[]
  top_tracks: ListeningTrack[]
  top_albums: ListeningAlbum[]
  trend: ListeningMonthPoint[]
  heatmap: ListeningDay[]
  hours: ListeningHour[]
  first_scrobble?: string
  last_scrobble?: string
}
