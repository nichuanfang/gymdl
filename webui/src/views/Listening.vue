<template>
  <section class="report-page">
    <header class="report-hero">
      <div class="hero-glow" aria-hidden="true" />
      <div class="hero-copy">
        <p class="hero-eyebrow">LAST.FM / LISTENING REPORT</p>
        <h1>
          <span class="hero-month">{{ report?.month_label || '听歌报告' }}</span>
          <span class="hero-sub">你的音乐，被这样记录</span>
        </h1>
        <p class="hero-note">
          数据来自 Last.fm scrobble 记录，只读汇总，不修改任何历史。
        </p>
      </div>
      <div class="hero-switcher">
        <button class="month-step" :disabled="loading" @click="shiftMonth(-1)">‹</button>
        <label class="month-picker">
          <input v-model="month" type="month" :max="currentMonth" @change="load" />
          <span>{{ monthLabel }}</span>
        </label>
        <button class="month-step" :disabled="loading" @click="shiftMonth(1)">›</button>
      </div>
    </header>

    <div v-if="error" class="report-state report-error">
      <strong>这份报告暂时拿不到</strong>
      <p>{{ error }}</p>
      <button class="retry-btn" @click="load">重试</button>
    </div>

    <div v-else-if="report && !report.available" class="report-state">
      <strong>这份报告暂时没有数据</strong>
      <p>检查收听历史数据库配置后重试。</p>
    </div>

    <div v-else-if="report && report.scrobbles === 0" class="report-state report-empty">
      <span class="empty-disc">♪</span>
      <strong>{{ report.month_label }}还没有收听记录</strong>
      <p>换一个月看看，或确认 Last.fm scrobble 同步是否正常。</p>
      <button class="retry-btn" @click="shiftMonth(-1)">看上月</button>
    </div>

    <div v-else-if="loading && !report" class="report-state">
      <span class="loading-disc" />
      <p>正在整理你的收听记录…</p>
    </div>

    <template v-else-if="report && report.available && report.scrobbles > 0">
      <div class="report-body" :key="report.month">
      <div class="stat-row">
        <article v-for="(stat, index) in stats" :key="stat.label" class="stat-card" :style="{ '--delay': `${index * 70}ms` }">
          <span class="stat-label">{{ stat.label }}</span>
          <strong class="stat-value">{{ stat.value }}</strong>
          <span class="stat-foot">{{ stat.foot }}</span>
        </article>
      </div>

      <section class="report-grid">
        <article class="panel trend-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">MONTHLY FLOW</p>
              <h2>近 12 个月播放趋势</h2>
            </div>
            <span class="panel-meta">{{ report.trend.length }} 个月有记录</span>
          </div>
          <div class="trend-chart">
            <div v-for="(point, index) in report.trend" :key="point.month" class="trend-column" :style="{ '--delay': `${index * 45}ms` }">
              <span class="trend-value">{{ point.plays }}</span>
              <div class="trend-track">
                <i class="trend-fill" :style="{ height: `${trendHeight(point.plays)}%`, '--height': `${trendHeight(point.plays)}%` }" />
              </div>
              <span class="trend-month">{{ shortMonth(point.month) }}</span>
            </div>
          </div>
        </article>

        <article class="panel clock-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">LISTENING CLOCK</p>
              <h2>你的一天</h2>
            </div>
            <span class="panel-meta">{{ peakHourLabel }}</span>
          </div>
          <div class="clock-bars">
            <span v-for="hour in report.hours" :key="hour.hour" class="clock-bar"
                  :style="{ height: `${hourHeight(hour.plays)}%` }" :title="`${hour.hour}:00 · ${hour.plays} 次`" />
          </div>
          <div class="clock-axis"><span>0:00</span><span>6:00</span><span>12:00</span><span>18:00</span><span>24:00</span></div>
        </article>
      </section>

      <section class="report-grid wide">
        <article class="panel artists-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">TOP ARTISTS</p>
              <h2>本月最常听</h2>
            </div>
            <span class="panel-meta">{{ report.artists }} 位艺人</span>
          </div>
          <ol class="artist-list">
            <li v-for="(artist, index) in report.top_artists" :key="artist.name" class="artist-row" :style="{ '--delay': `${index * 60}ms` }">
              <span class="artist-rank">{{ String(index + 1).padStart(2, '0') }}</span>
              <img v-if="artist.image" class="artist-cover" :src="artist.image" alt="" loading="lazy" @error="hideBroken" />
              <span v-else class="artist-cover artist-cover-fallback">{{ artist.name.slice(0, 1) }}</span>
              <div class="artist-copy">
                <strong>{{ artist.name }}</strong>
                <span>{{ artist.plays }} 次 · {{ formatMinutes(artist.minutes) }}</span>
              </div>
              <div class="artist-bar"><i :style="{ width: `${artistWidth(artist.plays)}%` }" /></div>
            </li>
          </ol>
        </article>

        <article class="panel tracks-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">TOP TRACKS</p>
              <h2>循环最多的歌</h2>
            </div>
            <span class="panel-meta">{{ report.tracks }} 首</span>
          </div>
          <ol class="track-list">
            <li v-for="(track, index) in report.top_tracks" :key="track.name + track.artist" class="track-row" :style="{ '--delay': `${index * 45}ms` }">
              <span class="track-rank">{{ index + 1 }}</span>
              <img v-if="track.image" class="track-cover" :src="track.image" alt="" loading="lazy" @error="hideBroken" />
              <span v-else class="track-cover track-cover-fallback">♪</span>
              <div class="track-copy">
                <strong>{{ track.name }}</strong>
                <span>{{ track.artist }}<template v-if="track.album"> · {{ track.album }}</template></span>
              </div>
              <span class="track-plays">{{ track.plays }}<small>次</small></span>
            </li>
          </ol>
        </article>
      </section>

      <section class="report-grid wide">
        <article class="panel albums-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">TOP ALBUMS</p>
              <h2>反复整张听完</h2>
            </div>
            <span class="panel-meta">{{ report.albums }} 张</span>
          </div>
          <div class="album-grid">
            <article v-for="(album, index) in report.top_albums" :key="album.name" class="album-card" :style="{ '--delay': `${index * 60}ms` }">
              <img v-if="album.image" class="album-art" :src="album.image" alt="" loading="lazy" @error="hideBroken" />
              <span v-else class="album-art album-art-fallback">♫</span>
              <div class="album-copy">
                <strong>{{ album.name }}</strong>
                <span>{{ album.artist }}</span>
                <em>{{ album.plays }} 次播放</em>
              </div>
            </article>
          </div>
        </article>

        <article class="panel heat-panel">
          <div class="panel-head">
            <div>
              <p class="panel-eyebrow">DAILY HEAT</p>
              <h2>每天听了多少</h2>
            </div>
            <span class="panel-meta">{{ activeDays }} 天有播放</span>
          </div>
          <div class="heat-grid">
            <span v-for="day in monthCells" :key="day.key" class="heat-cell"
                  :class="{ 'is-empty': !day.plays }"
                  :style="{ '--heat': heatLevel(day.plays), '--delay': `${day.index * 12}ms` }"
                  :title="`${day.key} · ${day.plays} 次`" />
          </div>
          <div class="heat-legend"><span>少</span><i /><i /><i /><i /><i /><span>多</span></div>
        </article>
      </section>

      </div>
      <footer class="report-foot">
        <span v-if="report.first_scrobble">本月第一条：{{ formatTime(report.first_scrobble) }}</span>
        <span v-if="report.last_scrobble">最后一条：{{ formatTime(report.last_scrobble) }}</span>
        <span class="foot-note">播放时长按每次约 3.5 分钟估算</span>
      </footer>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getListeningReport } from '../api'
import type { ListeningReport } from '../types'

const report = ref<ListeningReport | null>(null)
const loading = ref(true)
const error = ref('')
const currentMonth = new Date().toISOString().slice(0, 7)
const month = ref(currentMonth)

const monthLabel = computed(() => {
  const [year, value] = month.value.split('-')
  return `${year} 年 ${Number(value)} 月`
})

const stats = computed(() => {
  const data = report.value
  if (!data) return []
  const delta = data.prev_scrobbles ? Math.round(((data.scrobbles - data.prev_scrobbles) / data.prev_scrobbles) * 100) : 0
  return [
    { label: '播放次数', value: data.scrobbles.toLocaleString('zh-CN'), foot: data.prev_scrobbles ? `${delta >= 0 ? '+' : ''}${delta}% 相比上月` : '上月暂无记录' },
    { label: '估算时长', value: formatMinutes(data.minutes), foot: '按 3.5 分钟/次估算' },
    { label: '听过艺人', value: data.artists.toLocaleString('zh-CN'), foot: '本月去重' },
    { label: '不同曲目', value: data.tracks.toLocaleString('zh-CN'), foot: `${data.albums} 张专辑` },
  ]
})

const maxTrend = computed(() => Math.max(1, ...(report.value?.trend.map((p) => p.plays) ?? [1])))
const maxHour = computed(() => Math.max(1, ...(report.value?.hours.map((h) => h.plays) ?? [1])))
const maxArtist = computed(() => Math.max(1, ...(report.value?.top_artists.map((a) => a.plays) ?? [1])))
const activeDays = computed(() => report.value?.heatmap.filter((d) => d.plays > 0).length ?? 0)
const peakHourLabel = computed(() => {
  const hours = report.value?.hours ?? []
  if (!hours.length) return '暂无数据'
  const peak = hours.reduce((best, item) => (item.plays > best.plays ? item : best), hours[0])
  return `高峰 ${String(peak.hour).padStart(2, '0')}:00`
})

const monthCells = computed(() => {
  const [year, value] = month.value.split('-').map(Number)
  const total = new Date(year, value, 0).getDate()
  const byDate = new Map((report.value?.heatmap ?? []).map((day) => [day.date, day.plays]))
  return Array.from({ length: total }, (_, index) => {
    const date = `${year}-${String(value).padStart(2, '0')}-${String(index + 1).padStart(2, '0')}`
    return { key: date, index, plays: byDate.get(date) ?? 0 }
  })
})

function hideBroken(event: Event) {
  const image = event.target as HTMLImageElement
  image.style.display = 'none'
}
function trendHeight(value: number) {
  return Math.max(6, Math.round((value / maxTrend.value) * 100))
}
function hourHeight(value: number) {
  return Math.max(4, Math.round((value / maxHour.value) * 100))
}
function artistWidth(value: number) {
  return Math.max(6, Math.round((value / maxArtist.value) * 100))
}
function heatLevel(value: number) {
  if (!value) return 0
  return Math.min(4, 1 + Math.floor(Math.log2(value + 1)))
}
function shortMonth(value: string) {
  const [, m] = value.split('-')
  return `${Number(m)}月`
}
function formatMinutes(minutes: number) {
  if (minutes < 60) return `${minutes} 分钟`
  return `${Math.round(minutes / 60).toLocaleString('zh-CN')} 小时`
}
function formatTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}
function shiftMonth(offset: number) {
  const [year, value] = month.value.split('-').map(Number)
  const next = new Date(year, value - 1 + offset, 1)
  month.value = `${next.getFullYear()}-${String(next.getMonth() + 1).padStart(2, '0')}`
  void load()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const response = await getListeningReport(month.value)
    if (response.code === 200 && response.data) {
      report.value = response.data
    } else {
      error.value = response.message || '读取听歌报告失败'
    }
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '读取听歌报告失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => void load())
</script>

<style scoped>
.report-page { max-width: 1440px; margin: 0 auto; padding-bottom: 24px; }
.report-hero { position: relative; display: flex; justify-content: space-between; align-items: flex-end; gap: 24px; min-height: 210px; padding: 34px 38px; border: 1px solid rgba(171,190,184,.12); border-radius: 22px; overflow: hidden; background: radial-gradient(120% 140% at 12% 0%, rgba(99,226,183,.18), transparent 55%), linear-gradient(140deg, #17201f, #101418 70%); }
.hero-glow { position: absolute; width: 380px; height: 380px; right: -90px; top: -150px; border-radius: 50%; background: radial-gradient(circle, rgba(99,226,183,.24), transparent 65%); filter: blur(18px); animation: drift 9s ease-in-out infinite alternate; }
.hero-copy { position: relative; z-index: 1; }
.hero-eyebrow { margin: 0 0 12px; color: #63e2b7; font-size: 10px; font-weight: 700; letter-spacing: .22em; }
.hero-copy h1 { margin: 0; display: grid; gap: 8px; }
.hero-month { color: #f2f8f5; font-size: clamp(30px, 4vw, 48px); font-weight: 650; letter-spacing: -.03em; line-height: 1.05; }
.hero-sub { color: #93a49d; font-size: 14px; font-weight: 400; letter-spacing: .04em; }
.hero-note { margin: 16px 0 0; color: #6f8179; font-size: 11px; }
.hero-switcher { position: relative; z-index: 1; display: flex; align-items: center; gap: 8px; }
.month-step { width: 34px; height: 34px; border: 1px solid rgba(171,190,184,.16); border-radius: 10px; color: #b9c8c2; background: rgba(12,16,18,.6); font-size: 18px; cursor: pointer; transition: border-color .2s, color .2s, transform .2s; }
.month-step:hover:not(:disabled) { border-color: rgba(99,226,183,.5); color: #63e2b7; transform: translateY(-1px); }
.month-step:disabled { opacity: .4; cursor: default; }
.month-picker { position: relative; padding: 9px 16px; border: 1px solid rgba(171,190,184,.16); border-radius: 10px; background: rgba(12,16,18,.6); color: #d8e4df; font-size: 12px; cursor: pointer; transition: border-color .2s, background .2s, color .2s; }
.month-picker:hover, .month-picker:focus-within { border-color: rgba(99,226,183,.45); background: rgba(99,226,183,.06); color: #eaf5f0; }
.month-picker input { position: absolute; inset: 0; opacity: 0; width: 100%; cursor: pointer; }
.report-body { animation: fadein .45s ease both; }
@keyframes fadein { from { opacity: 0; } to { opacity: 1; } }
.stat-row { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-top: 14px; }
.stat-card { display: grid; gap: 7px; padding: 20px 22px; border: 1px solid rgba(171,190,184,.1); border-radius: 14px; background: linear-gradient(150deg, rgba(28,33,39,.96), rgba(17,20,24,.98)); animation: rise .5s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
.stat-label { color: #84928c; font-size: 11px; letter-spacing: .05em; }
.stat-value { color: #eef5f2; font-size: clamp(24px, 2.4vw, 34px); font-weight: 640; letter-spacing: -.03em; }
.stat-foot { color: #5f6e68; font-size: 10px; }
.report-grid { display: grid; grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr); gap: 12px; margin-top: 12px; }
.report-grid.wide { grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr); }
.panel { padding: 22px 24px; border: 1px solid rgba(171,190,184,.1); border-radius: 16px; background: rgba(18,22,27,.92); animation: rise .55s cubic-bezier(.22,.8,.32,1) both; animation-delay: .08s; }
.panel-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 14px; margin-bottom: 18px; }
.panel-eyebrow { margin: 0 0 6px; color: #5f7d73; font-size: 9px; font-weight: 700; letter-spacing: .2em; }
.panel-head h2 { margin: 0; color: #e6eeea; font-size: 16px; font-weight: 600; }
.panel-meta { color: #68766f; font-size: 10px; white-space: nowrap; }
.trend-chart { display: flex; align-items: flex-end; gap: clamp(5px, 1.2vw, 14px); height: 190px; padding-top: 10px; }
.trend-column { flex: 1; display: flex; flex-direction: column; align-items: center; gap: 7px; height: 100%; animation: rise .5s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
.trend-value { color: #7e908a; font-size: 9px; opacity: 0; transition: opacity .2s; }
.trend-column:hover .trend-value { opacity: 1; }
.trend-track { flex: 1; width: 100%; max-width: 34px; display: flex; align-items: flex-end; border-radius: 7px; background: rgba(99,226,183,.06); overflow: hidden; }
.trend-fill { width: 100%; height: var(--height, 0); border-radius: 7px; background: linear-gradient(180deg, #63e2b7, rgba(63,157,133,.35)); animation: grow .85s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
@keyframes grow { from { height: 0; } to { height: var(--height, 0); } }
.trend-month { color: #64736d; font-size: 9px; }
.clock-bars { display: flex; align-items: flex-end; gap: 3px; height: 150px; }
.clock-bar { flex: 1; min-height: 3px; border-radius: 3px 3px 1px 1px; background: linear-gradient(180deg, rgba(99,226,183,.9), rgba(99,226,183,.18)); transition: filter .2s, transform .2s; }
.clock-bar:hover { filter: brightness(1.35); transform: scaleY(1.04); }
.clock-axis { display: flex; justify-content: space-between; margin-top: 9px; color: #56645e; font-size: 9px; }
.artist-list, .track-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; }
.artist-row { position: relative; display: grid; grid-template-columns: 26px 42px minmax(0, 1fr) 74px; align-items: center; gap: 12px; padding: 8px 10px; border-radius: 10px; overflow: hidden; animation: slide .45s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
.artist-row { transition: background .2s; }
.artist-row:hover { background: rgba(99,226,183,.05); }
.artist-rank { color: #5e6f68; font-size: 11px; font-variant-numeric: tabular-nums; }
.artist-cover, .track-cover { width: 42px; height: 42px; border-radius: 9px; object-fit: cover; background: #1c2428; }
.artist-cover-fallback, .track-cover-fallback { display: grid; place-items: center; color: #7ccbb0; font-size: 15px; }
.artist-copy, .track-copy { min-width: 0; display: grid; gap: 3px; }
.artist-copy strong, .track-copy strong { overflow: hidden; color: #dfe8e4; font-size: 12px; font-weight: 550; text-overflow: ellipsis; white-space: nowrap; }
.artist-copy span, .track-copy span { overflow: hidden; color: #6f7e78; font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.artist-bar { height: 4px; border-radius: 4px; background: rgba(255,255,255,.05); overflow: hidden; }
.artist-bar i { display: block; height: 100%; border-radius: 4px; background: linear-gradient(90deg, rgba(99,226,183,.35), #63e2b7); }
.track-row { display: grid; grid-template-columns: 20px 40px minmax(0, 1fr) auto; align-items: center; gap: 11px; padding: 7px 8px; border-radius: 10px; animation: slide .45s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
.track-row { transition: background .2s; }
.track-row:hover { background: rgba(99,226,183,.05); }
.track-rank { color: #5d6d66; font-size: 11px; font-variant-numeric: tabular-nums; }
.track-cover { width: 40px; height: 40px; }
.track-plays { color: #a9bbb4; font-size: 13px; font-variant-numeric: tabular-nums; }
.track-plays small { margin-left: 2px; color: #5f6e68; font-size: 9px; }
.album-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.album-card { display: grid; gap: 10px; animation: rise .5s cubic-bezier(.22,.8,.32,1) both; animation-delay: var(--delay); }
.album-art { width: 100%; aspect-ratio: 1; border-radius: 12px; object-fit: cover; background: #1b2327; transition: transform .35s cubic-bezier(.22,.8,.32,1), box-shadow .35s; }
.album-card:hover .album-art { transform: translateY(-4px) scale(1.02); box-shadow: 0 18px 40px rgba(0,0,0,.45); }
.album-art-fallback { display: grid; place-items: center; color: #6da791; font-size: 28px; }
.album-copy { display: grid; gap: 3px; min-width: 0; }
.album-copy strong, .album-copy span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.album-copy strong { color: #dde7e3; font-size: 11px; font-weight: 550; }
.album-copy span { color: #6f7e78; font-size: 10px; }
.album-copy em { color: #5d6b65; font-size: 9px; font-style: normal; }
.heat-grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 5px; }
.heat-cell { aspect-ratio: 1; border-radius: 4px; background: rgba(99,226,183, calc(.08 + var(--heat) * .18)); animation: pop .35s ease both; animation-delay: var(--delay); }
.heat-cell.is-empty { background: rgba(255,255,255,.035); }
.heat-legend { display: flex; align-items: center; justify-content: flex-end; gap: 4px; margin-top: 10px; color: #5c6a64; font-size: 9px; }
.heat-legend i { width: 9px; height: 9px; border-radius: 2px; background: rgba(99,226,183,.25); }
.heat-legend i:nth-child(3) { background: rgba(99,226,183,.42); }
.heat-legend i:nth-child(4) { background: rgba(99,226,183,.62); }
.heat-legend i:nth-child(5) { background: rgba(99,226,183,.82); }
.heat-legend i:nth-child(6) { background: #63e2b7; }
.report-state { display: grid; place-items: center; gap: 12px; min-height: 320px; padding: 40px; border: 1px dashed rgba(171,190,184,.16); border-radius: 16px; color: #82918a; text-align: center; }
.report-state strong { color: #dfe8e4; font-size: 15px; }
.report-state p { margin: 0; font-size: 12px; }
.report-error { border-color: rgba(232,128,128,.28); }
.retry-btn { padding: 8px 16px; border: 1px solid rgba(99,226,183,.4); border-radius: 9px; color: #63e2b7; background: transparent; cursor: pointer; }
.loading-disc { width: 34px; height: 34px; border: 2px solid rgba(99,226,183,.16); border-top-color: #63e2b7; border-radius: 50%; animation: spin 1s linear infinite; }
.report-foot { display: flex; flex-wrap: wrap; gap: 18px; margin-top: 16px; padding: 14px 2px; border-top: 1px solid rgba(171,190,184,.08); color: #5f6d67; font-size: 10px; }
.foot-note { margin-left: auto; }
@keyframes rise { from { opacity: 0; transform: translateY(14px); } to { opacity: 1; transform: none; } }
@keyframes slide { from { opacity: 0; transform: translateX(-8px); } to { opacity: 1; transform: none; } }
@keyframes pop { from { opacity: 0; transform: scale(.82); } to { opacity: 1; transform: none; } }
@keyframes spin { to { transform: rotate(360deg); } }
@keyframes drift { to { transform: translate(-24px, 18px) scale(1.08); } }
.report-empty { border-style: solid; background: radial-gradient(circle at 50% 20%, rgba(99,226,183,.06), transparent 60%); }
.empty-disc { display: grid; place-items: center; width: 58px; height: 58px; border: 1px solid rgba(99,226,183,.3); border-radius: 50%; color: #63e2b7; font-size: 22px; animation: breathe 2.2s ease-in-out infinite alternate; }
@keyframes breathe { to { opacity: .55; transform: scale(.96); } }
.artist-cover, .track-cover, .album-art { background: #1b2327; }
@media (max-width: 1100px) { .report-grid, .report-grid.wide { grid-template-columns: 1fr; } }
@media (max-width: 760px) {
  .report-hero { flex-direction: column; align-items: flex-start; padding: 24px 20px; }
  .month-step { width: 44px; height: 44px; }
  .month-picker { min-height: 44px; display: flex; align-items: center; justify-content: center; }
  .track-row { padding: 9px 6px; }
  .track-copy strong, .artist-copy strong { font-size: 13px; }
  .artist-row { grid-template-columns: 22px 42px minmax(0, 1fr); }
  .artist-bar { display: none; }
  .stat-row { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .album-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .panel { padding: 18px 16px; }
  .hero-switcher { width: 100%; justify-content: space-between; }
  .month-picker { flex: 1; text-align: center; }
  .report-foot { flex-direction: column; gap: 6px; }
  .foot-note { margin-left: 0; }
}
@media (prefers-reduced-motion: reduce) {
  .stat-card, .panel, .artist-row, .track-row, .album-card, .heat-cell, .hero-glow { animation: none !important; }
}
</style>
