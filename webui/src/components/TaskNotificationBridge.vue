<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useMessage } from 'naive-ui'
import { subscribeTaskNotifications } from './taskNotifications'

const message = useMessage()
let unsubscribe: (() => void) | undefined

onMounted(() => {
  unsubscribe = subscribeTaskNotifications((task) => {
    const title = task.song_info?.map((song) => song.SongName).filter(Boolean).join('、') || '下载任务'
    if (task.status === 'completed') message.success(`${title} 已下载完成`, { duration: 5000 })
    else if (task.status === 'failed') message.error(`${title} 下载失败${task.error ? `：${task.error}` : ''}`, { duration: 8000 })
    else message.warning(`${title} 已取消`, { duration: 4000 })
  })
})
onUnmounted(() => unsubscribe?.())
</script>

<template><span aria-hidden="true" class="task-notification-bridge" /></template>
