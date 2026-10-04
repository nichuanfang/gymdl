const BASE = '/api/web'

async function decodeResponse<T>(response: Response, url: string): Promise<ApiResponse<T>> {
  if (response.status === 401 && !url.startsWith('/auth/login')) {
    window.dispatchEvent(new CustomEvent('gymdl:unauthorized'))
  }
  return response.json() as Promise<ApiResponse<T>>
}

export interface ApiResponse<T = any> {
  code: number
  message: string
  data?: T
  errors?: string[]
}

export async function get<T = any>(url: string, params?: Record<string, any>): Promise<ApiResponse<T>> {
  const search = params ? '?' + new URLSearchParams(
    Object.entries(params).filter(([, v]) => v != null).map(([k, v]) => [k, String(v)])
  ).toString() : ''
  const res = await fetch(`${BASE}${url}${search}`, { credentials: 'same-origin' })
  return decodeResponse<T>(res, url)
}

export async function post<T = any>(url: string, body?: any): Promise<ApiResponse<T>> {
  const res = await fetch(`${BASE}${url}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  return decodeResponse<T>(res, url)
}

export async function put<T = any>(url: string, body?: any): Promise<ApiResponse<T>> {
  const res = await fetch(`${BASE}${url}`, {
    method: 'PUT',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  return decodeResponse<T>(res, url)
}

export async function del<T = any>(url: string, params?: Record<string, any>): Promise<ApiResponse<T>> {
  const search = params ? '?' + new URLSearchParams(
    Object.entries(params).filter(([, v]) => v != null).map(([k, v]) => [k, String(v)])
  ).toString() : ''
  const res = await fetch(`${BASE}${url}${search}`, { method: 'DELETE', credentials: 'same-origin' })
  return decodeResponse<T>(res, url)
}
