<template>
  <div>
    <PageHeading eyebrow="TASKS / DOWNLOAD QUEUE" title="下载" />
    <n-space vertical :size="16">
      <n-input-group>
        <n-input v-model:value="url" placeholder="粘贴音乐/视频链接，支持 Apple Music / Spotify / QQ / 网易云 / YouTube 等" size="large" @keyup.enter="handleSubmit" />
        <n-button type="primary" size="large" @click="handleSubmit" :loading="submitting">提交</n-button>
      </n-input-group>

      <n-card title="活跃任务" size="small" v-if="activeTasks.length > 0">
        <n-space vertical :size="12">
          <div v-for="t in activeTasks" :key="t.id">
            <n-space justify="space-between" align="center">
              <n-space :size="8" align="center">
                <n-tag :type="tagType(t.status)" size="small">{{ t.platform }}</n-tag>
                <span>{{ t.progress || t.status }}</span>
              </n-space>
              <n-button v-if="t.status === 'pending'" size="tiny" quaternary type="error" @click="handleCancel(t.id)">取消</n-button>
              <n-tag v-else size="small" type="info">执行中</n-tag>
            </n-space>
          </div>
        </n-space>
      </n-card>

      <n-empty v-if="activeTasks.length === 0" style="margin-top: 40px">
        <template #default>
          <n-space vertical align="center" :size="8">
            <n-text>暂无进行中的任务</n-text>
            <n-text depth="3" style="font-size: 12px">粘贴链接到上方输入框，或去搜索页找音乐</n-text>
          </n-space>
        </template>
        <template #extra>
          <n-button size="small" type="primary" @click="$router.push('/search')">去搜索</n-button>
        </template>
      </n-empty>
    </n-space>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { NInputGroup, NInput, NButton, NCard, NSpace, NTag, NEmpty, NText, useMessage } from 'naive-ui'
import { getActiveTasks, cancelTask } from '../api'
import PageHeading from '../components/PageHeading.vue'
import { watchTaskCompletion } from '../components/taskNotifications'
import { useTaskSubmission } from '../components/useTaskSubmission'
import type { Task } from '../types'

const message = useMessage()
const { submitWithDuplicateConfirmation } = useTaskSubmission()
const url = ref('')
const submitting = ref(false)
const activeTasks = ref<Task[]>([])
let timer: ReturnType<typeof setInterval> | null = null

function tagType(status: string): 'default' | 'info' | 'success' | 'warning' | 'error' {
  switch (status) {
    case 'running': return 'info'
    case 'completed': return 'success'
    case 'failed': return 'error'
    case 'cancelled': return 'warning'
    default: return 'default'
  }
}

async function refresh() {
  try {
    const res = await getActiveTasks()
    if (res.code === 200) activeTasks.value = res.data || []
  } catch {
    // Keep the last snapshot visible if a background poll fails.
  }
}

async function handleSubmit() {
  if (!url.value.trim()) {
    message.warning('请输入链接')
    return
  }
  submitting.value = true
  try {
    const res = await submitWithDuplicateConfirmation(url.value.trim())
    if (res.code === 200) {
      message.success('任务已提交')
      if (res.data?.id) watchTaskCompletion(res.data.id)
      url.value = ''
      await refresh()
    } else if (res.code !== 499) {
      message.error(res.message || '提交失败')
    }
  } catch (error) {
    message.error(error instanceof Error ? `提交失败：${error.message}` : '提交失败')
  } finally {
    submitting.value = false
  }
}

async function handleCancel(id: string) {
  try {
    const res = await cancelTask(id)
    if (res.code === 200) {
      message.success('已取消')
      await refresh()
    } else message.error(res.message || '取消失败')
  } catch (error) {
    message.error(error instanceof Error ? `取消失败：${error.message}` : '取消失败')
  }
}

onMounted(() => {
  refresh()
  timer = setInterval(refresh, 3000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>
