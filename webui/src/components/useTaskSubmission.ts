import { h } from 'vue'
import { useDialog } from 'naive-ui'
import { submitTask } from '../api'
import type { ApiResponse } from '../api/http'
import type { DuplicateCheckResult, TaskSubmitResponse } from '../api'

export function useTaskSubmission() {
  const dialog = useDialog()

  async function submitWithDuplicateConfirmation(url: string): Promise<ApiResponse<TaskSubmitResponse>> {
    let response = await submitTask(url)
    const confirmation = response.data
    if (response.code !== 409 || !confirmation?.confirmation_required || !confirmation.duplicate) return response

    const result = confirmation.duplicate
    const duplicate = result.status === 'duplicate'
    const title = duplicate ? '可能是同一首歌' : '还没法确定'
    const detail = duplicate ? duplicateCard(result) : `${result.reason || '这次没能核对曲库'}\n\n仍要下载吗？`
    const confirmed = await new Promise<boolean>((resolve) => {
      let settled = false
      const finish = (value: boolean) => {
        if (settled) return
        settled = true
        resolve(value)
      }
      dialog.warning({
        title,
        content: () => h('div', { style: 'white-space: pre-line; line-height: 1.8; max-width: 28em' }, detail),
        positiveText: duplicate ? '再存一份' : '仍然下载',
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

function duplicateCard(result: DuplicateCheckResult): string {
  const lines = [result.match?.name || result.match?.path || '一首已入库的歌']
  if (result.target?.name) {
    lines.push('', result.target.name)
  }
  if (result.target?.artist) lines.push(result.target.artist)
  if (result.target?.album) lines.push(result.target.album)
  lines.push('', '歌名、歌手、专辑和格式一致', '', '要再存一份吗？')
  return lines.join('\n')
}
