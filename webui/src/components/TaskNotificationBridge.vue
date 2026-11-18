<script setup lang="ts">
import { h, onMounted, onUnmounted } from 'vue'
import { useDialog, useMessage } from 'naive-ui'
import { submitTask } from '../api'
import { subscribeTaskNotifications, watchTaskCompletion } from './taskNotifications'
import type { TaskEvent } from './taskNotifications'

const message = useMessage()
const dialog = useDialog()
let unsubscribe: (() => void) | undefined

function askToKeep(task: TaskEvent) {
  let settled = false
  const finish = (keep: boolean) => {
    if (settled) return
    settled = true
    if (!keep || !task.url) return
    submitTask(task.url, true).then((response) => {
      if (response.data?.id) watchTaskCompletion(response.data.id)
    }).catch(() => {
      message.error('没能重新开始下载')
    })
  }
  dialog.warning({
    title: '听感很接近',
    content: () => h('div', { style: 'white-space: pre-line; line-height: 1.8; max-width: 28em' }, `${task.error || '和曲库里的一段录音接近'}\n\n要再存一份吗？`),
    positiveText: '再存一份',
    negativeText: '取消',
    maskClosable: false,
    closeOnEsc: true,
    onPositiveClick: () => finish(true),
    onNegativeClick: () => finish(false),
    onClose: () => finish(false),
  })
}

onMounted(() => {
  unsubscribe = subscribeTaskNotifications((task) => {
    const title = task.song_info?.map((song) => song.SongName).filter(Boolean).join('、') || '下载任务'
    if (task.status === 'completed') message.success(`${title} 已下载完成`, { duration: 5000 })
    else if (task.status === 'failed' && task.audio_confirm) askToKeep(task)
    else if (task.status === 'failed') message.error(`${title} 下载失败${task.error ? `：${task.error}` : ''}`, { duration: 8000 })
    else message.warning(`${title} 已取消`, { duration: 4000 })
  })
})
onUnmounted(() => unsubscribe?.())
</script>

<template><span aria-hidden="true" class="task-notification-bridge" /></template>
