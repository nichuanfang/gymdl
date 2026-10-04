import { h } from 'vue'
import { useDialog } from 'naive-ui'
import { submitTask } from '../api'
import type { ApiResponse } from '../api/http'
import type { TaskSubmitResponse } from '../api'

export function useTaskSubmission() {
  const dialog = useDialog()

  async function submitWithDuplicateConfirmation(url: string): Promise<ApiResponse<TaskSubmitResponse>> {
    let response = await submitTask(url)
    const confirmation = response.data
    if (response.code !== 409 || !confirmation?.confirmation_required || !confirmation.duplicate) return response

    const result = confirmation.duplicate
    const title = result.status === 'duplicate' ? '发现重复歌曲' : '暂时无法确认是否重复'
    const detail = result.status === 'duplicate'
      ? `已存在文件：${result.match?.path || result.match?.name || '整理目录中的音频'}\n歌曲名、歌手、专辑名和格式均一致。`
      : result.reason || '重复检查暂不可用。继续下载可能会产生重复文件。'
    const target = result.target
    const targetDetail = target
      ? `\n\n本次下载：${target.name} / ${target.artist} / ${target.album} · ${target.ext}`
      : ''
    const confirmed = await new Promise<boolean>((resolve) => {
      let settled = false
      const finish = (value: boolean) => {
        if (settled) return
        settled = true
        resolve(value)
      }
      dialog.warning({
        title,
        content: () => h('div', { style: 'white-space: pre-line; line-height: 1.7' }, detail + targetDetail),
        positiveText: '仍然下载',
        negativeText: '取消',
        maskClosable: false,
        closeOnEsc: true,
        onPositiveClick: () => finish(true),
        onNegativeClick: () => finish(false),
        onClose: () => finish(false),
      })
    })
    if (!confirmed) return { ...response, code: 499, message: '已取消下载' }

    response = await submitTask(url, true)
    return response
  }

  return { submitWithDuplicateConfirmation }
}
