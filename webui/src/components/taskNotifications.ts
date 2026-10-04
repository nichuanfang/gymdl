export interface TaskEvent {
  id: string
  status: string
  error?: string
  song_info?: Array<{ SongName: string; SongArtists: string }>
}

type TaskNotificationHandler = (task: TaskEvent) => void
const handlers = new Set<TaskNotificationHandler>()
const watched = new Map<string, EventSource>()

export function subscribeTaskNotifications(handler: TaskNotificationHandler) {
  handlers.add(handler)
  return () => handlers.delete(handler)
}

export function watchTaskCompletion(taskId: string) {
  if (!taskId || watched.has(taskId)) return
  const stream = new EventSource(`/api/web/task/${encodeURIComponent(taskId)}/events`, { withCredentials: true })
  watched.set(taskId, stream)
  const handle = (event: Event) => {
    const message = event as MessageEvent<string>
    let task: TaskEvent
    try {
      task = JSON.parse(message.data) as TaskEvent
    } catch {
      return
    }
    if (!['completed', 'failed', 'cancelled'].includes(task.status)) return
    stream.close()
    watched.delete(taskId)
    handlers.forEach((listener) => listener(task))
  }
  stream.addEventListener('task', handle)
  stream.onerror = () => {
    // The history page remains the source of truth if a stream is interrupted.
    stream.close()
    watched.delete(taskId)
  }
}
