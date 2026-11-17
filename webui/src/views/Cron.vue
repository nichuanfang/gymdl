<template>
  <div>
    <PageHeading eyebrow="SYSTEM / SCHEDULE" title="定时任务">
      <template #actions>
        <n-button size="small" :loading="loading" @click="load()">刷新</n-button>
      </template>
    </PageHeading>
    <n-alert v-if="loaded && !enabled" type="warning" :show-icon="false">
      定时任务没有开启。打开 additional_config.enable_cron 并重启后，这里才会列出任务。
    </n-alert>
    <n-alert v-else-if="error" type="error" :show-icon="false">{{ error }}</n-alert>
    <n-card v-else size="small" :bordered="false" class="cron-card">
      <div v-if="jobs.length === 0 && loaded" class="empty">当前没有已注册的任务。</div>
      <article v-for="job in jobs" :key="job.name" class="job">
        <div>
          <h3>{{ job.label }}</h3>
          <p>{{ job.schedule || '按已有计划执行' }}</p>
          <p class="meta">
            <span v-if="job.running">正在执行</span>
            <span v-else-if="job.last_run">上次 {{ formatTime(job.last_run) }}</span>
            <span v-else>还没跑过</span>
            <span v-if="job.next_run"> · 下次 {{ formatTime(job.next_run) }}</span>
          </p>
          <p v-if="job.last_error" class="error">{{ job.last_error }}</p>
        </div>
        <n-popconfirm @positive-click="run(job)">
          <template #trigger>
            <n-button size="small" :loading="running === job.name" :disabled="job.running">现在执行</n-button>
          </template>
          按原任务再跑一次，不改变下次计划。
        </n-popconfirm>
      </article>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { NAlert, NButton, NCard, NPopconfirm, useMessage } from 'naive-ui'
import PageHeading from '../components/PageHeading.vue'
import { getCronJobs, runCronJob, type CronJob } from '../api'

const message = useMessage()
const jobs = ref<CronJob[]>([])
const enabled = ref(true)
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const running = ref('')
let pollTimer: ReturnType<typeof setTimeout> | null = null
let disposed = false

function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function stopPoll() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
}

function schedulePoll() {
  stopPoll()
  if (disposed || !jobs.value.some((job) => job.running)) return
  pollTimer = setTimeout(() => { void load(true) }, 3000)
}

async function load(quiet = false) {
  if (!quiet) loading.value = true
  error.value = ''
  try {
    const response = await getCronJobs()
    if (response.code !== 200 || !response.data) throw new Error(response.message || '读取失败')
    enabled.value = response.data.enabled
    jobs.value = response.data.jobs || []
  } catch (err) {
    error.value = err instanceof Error ? err.message : '读取失败'
  } finally {
    if (!quiet) loading.value = false
    loaded.value = true
    schedulePoll()
  }
}

async function run(job: CronJob) {
  running.value = job.name
  try {
    const response = await runCronJob(job.name)
    if (response.code !== 200) throw new Error(response.message || '没有执行')
    message.success(`${job.label} 已开始`)
    await load()
  } catch (err) {
    message.error(err instanceof Error ? err.message : '没有执行')
  } finally {
    running.value = ''
  }
}

onMounted(() => void load())
onUnmounted(() => {
  disposed = true
  stopPoll()
})
</script>

<style scoped>
.cron-card { background: transparent; }
.job {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: center;
  padding: 16px 12px;
  border-bottom: 1px solid rgba(125, 150, 141, 0.18);
  border-radius: 12px;
  transition: background .22s var(--ease, ease);
}
.job:hover { background: rgba(99, 226, 183, .05); }
.job h3 { margin: 0 0 4px; font-size: 16px; font-weight: 600; }
.job p { margin: 0; color: #8ea39b; font-size: 13px; }
.meta { margin-top: 6px !important; }
.error { margin-top: 6px !important; color: #e7a0a0 !important; }
.empty { padding: 28px 0; color: #8ea39b; }
@media (prefers-reduced-motion: reduce) {
  .job { transition: none; }
}
</style>
