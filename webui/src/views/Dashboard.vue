<template>
  <section class="dashboard-page">
    <PageHeading eyebrow="OVERVIEW / SYSTEM HEALTH" title="仪表盘">
      <template #actions>
        <div class="refresh-note">
          <span class="pulse-dot" />
          {{ checkedAtLabel }}
        </div>
      </template>
    </PageHeading>

    <n-spin :show="loading && !status">
      <div class="metric-grid">
        <article class="metric-card accent">
          <span class="metric-label">正在下载</span>
          <strong>{{ metrics.running }}</strong>
          <span class="metric-foot">{{ metrics.pending }} 个任务排队中</span>
        </article>
        <article class="metric-card">
          <span class="metric-label">近 24 小时完成</span>
          <strong>{{ metrics.completed_24h }}</strong>
          <span class="metric-foot">成功任务</span>
        </article>
        <article class="metric-card warning">
          <span class="metric-label">近 24 小时失败</span>
          <strong>{{ metrics.failed_24h }}</strong>
          <span class="metric-foot">可在历史中筛选查看</span>
        </article>
        <article class="metric-card">
          <span class="metric-label">历史任务</span>
          <strong>{{ metrics.total_history }}</strong>
          <span class="metric-foot">累计记录</span>
        </article>
      </div>

      <section class="service-section">
        <div class="section-heading">
          <div>
            <p class="eyebrow">CONNECTED SERVICES</p>
            <h3>服务状态</h3>
          </div>
          <span class="muted">健康检查在后台更新，不阻塞页面加载</span>
        </div>
        <n-grid :cols="'1 s:2 m:3 l:4'" responsive="screen" :x-gap="12" :y-gap="12">
          <n-grid-item v-for="svc in services" :key="svc.name">
            <article class="service-card" :class="cardClass(svc)">
              <div class="service-topline">
                <span class="status-light" />
                <span class="service-name">{{ svc.name }}</span>
                <span class="service-state">{{ stateLabel(svc) }}</span>
              </div>
              <p>{{ svc.detail || (svc.enabled ? '等待状态检查' : '未启用') }}</p>
            </article>
          </n-grid-item>
        </n-grid>
      </section>

      <footer class="runtime-strip">
        <div><span>运行时长</span><strong>{{ formatUptime(status?.uptime || 0) }}</strong></div>
        <div><span>Go 版本</span><strong>{{ status?.go_version || '—' }}</strong></div>
        <div><span>应用版本</span><strong>{{ status?.version || '—' }}</strong></div>
      </footer>
    </n-spin>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NGrid, NGridItem, NSpin } from 'naive-ui'
import { getDashboardSummary } from '../api'
import PageHeading from '../components/PageHeading.vue'
import type { SystemStatus, ServiceStatus } from '../types'

const loading = ref(true)
const status = ref<SystemStatus | null>(null)
const services = ref<ServiceStatus[]>([])

const metrics = computed(() => status.value?.metrics || {
  pending: 0, running: 0, completed_24h: 0, failed_24h: 0, total_history: 0,
})
const initialCheckPending = computed(() => !status.value?.checked_at)
const checkedAtLabel = computed(() => {
  const checkedAt = status.value?.checked_at
  if (!checkedAt) return '正在初始化健康检查'
  return `上次检查 ${new Date(checkedAt).toLocaleTimeString('zh-CN', { hour12: false })}`
})

function cardClass(svc: ServiceStatus): string {
  if (!svc.enabled) return 'is-disabled'
  if (initialCheckPending.value) return 'is-checking'
  return svc.online ? 'is-online' : 'is-warning'
}
function stateLabel(svc: ServiceStatus): string {
  if (!svc.enabled) return 'OFF'
  if (initialCheckPending.value) return 'CHECKING'
  return svc.online ? 'ONLINE' : 'CHECK'
}
function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return days > 0 ? `${days}d ${hours}h` : `${hours}h ${minutes}m`
}

let refreshTimer: ReturnType<typeof setTimeout> | null = null
let requestInFlight = false
let nextInitialRefreshMs = 500
let disposed = false

async function load() {
  if (requestInFlight || disposed) return
  requestInFlight = true
  try {
    const res = await getDashboardSummary()
    if (res.code === 200 && res.data) {
      status.value = res.data
      services.value = res.data.services || []
    }
  } catch {
    // Leave the last cached snapshot visible if a later refresh fails.
  } finally {
    loading.value = false
    requestInFlight = false
    if (disposed) return

    const checked = !!status.value?.checked_at && !Number.isNaN(Date.parse(status.value.checked_at))
    const delay = checked ? 30000 : nextInitialRefreshMs
    if (!checked) nextInitialRefreshMs = Math.min(nextInitialRefreshMs * 2, 3000)
    refreshTimer = setTimeout(() => {
      refreshTimer = null
      void load()
    }, delay)
  }
}

onMounted(() => {
  disposed = false
  nextInitialRefreshMs = 500
  void load()
})
onUnmounted(() => {
  disposed = true
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = null
})
</script>

<style scoped>
.dashboard-page { max-width: 1440px; margin: 0 auto; }
.section-heading { display: flex; justify-content: space-between; align-items: flex-end; gap: 16px; }
.eyebrow { margin: 0 0 7px; color: #638078; font-size: 10px; font-weight: 700; letter-spacing: .19em; }
.refresh-note { display: flex; align-items: center; gap: 9px; color: #788580; font-size: 11px; }
.pulse-dot { width: 7px; height: 7px; background: #63e2b7; border-radius: 50%; box-shadow: 0 0 11px rgba(99,226,183,.7); }
.metric-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.metric-card { min-height: 135px; display: flex; flex-direction: column; justify-content: space-between; padding: 18px 20px; border: 1px solid rgba(171,190,184,.1); border-radius: 12px; background: linear-gradient(145deg, rgba(28,33,39,.98), rgba(18,21,26,.98)); }
.metric-card.accent { border-color: rgba(99,226,183,.25); }
.metric-card.warning { border-color: rgba(232,128,128,.2); }
.metric-label { color: #889590; font-size: 12px; }
.metric-card strong { color: #ecf3f0; font-size: 34px; font-weight: 600; letter-spacing: -.04em; }
.metric-card.accent strong { color: #63e2b7; }
.metric-card.warning strong { color: #e88080; }
.metric-foot { color: #64716d; font-size: 11px; }
.service-section { margin-top: 38px; }
.section-heading { margin-bottom: 15px; align-items: flex-end; }
.section-heading h3 { margin: 0; color: #e6ecea; font-size: 17px; font-weight: 600; }
.muted { color: #68736f; font-size: 11px; }
.service-card { min-height: 90px; padding: 15px; border: 1px solid rgba(171,190,184,.1); border-radius: 10px; background: rgba(20,24,29,.86); }
.service-topline { display: flex; align-items: center; gap: 9px; }
.service-name { color: #dce5e1; font-size: 12px; font-weight: 600; flex: 1; }
.service-state { color: #63706b; font-size: 9px; letter-spacing: .12em; }
.status-light { width: 7px; height: 7px; border-radius: 50%; background: #56615c; }
.service-card p { margin: 12px 0 0 16px; overflow: hidden; color: #76817c; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.is-online .status-light { background: #63e2b7; box-shadow: 0 0 10px rgba(99,226,183,.45); }
.is-online .service-state { color: #63e2b7; }
.is-checking .status-light { background: #8a9690; }
.is-checking .service-state { color: #9aa59f; }
.is-warning .status-light { background: #e8a06e; }
.is-warning .service-state { color: #e8a06e; }
.is-disabled { opacity: .55; }
.runtime-strip { display: flex; gap: clamp(26px, 6vw, 90px); margin-top: 36px; padding: 18px 2px; border-top: 1px solid rgba(171,190,184,.1); }
.runtime-strip div { display: grid; gap: 7px; }
.runtime-strip span { color: #68736f; font-size: 10px; text-transform: uppercase; letter-spacing: .11em; }
.runtime-strip strong { color: #bac5c0; font-size: 13px; font-weight: 500; }
@media (max-width: 800px) { .metric-grid { grid-template-columns: repeat(2,minmax(0,1fr)); } }
@media (max-width: 540px) { .metric-grid { grid-template-columns: 1fr; } .refresh-note { font-size: 0; } .runtime-strip { flex-wrap: wrap; } }
</style>
