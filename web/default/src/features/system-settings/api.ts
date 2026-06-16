/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'
import type {
  ConfirmPaymentComplianceResponse,
  CreativeModelBindingsDryRunResponse,
  CreativeModelBindingsResponse,
  CreativeModelPolicyResponse,
  DeleteLogsResponse,
  FetchUpstreamRatiosRequest,
  SystemOptionsResponse,
  UpdateCreativeModelBindingsRequest,
  UpdateCreativeModelPolicyRequest,
  UpdateOptionRequest,
  UpdateOptionResponse,
  UpstreamChannelsResponse,
  UpstreamRatiosResponse,
  ValidateCreativeModelBindingsResponse,
} from './types'

export async function getSystemOptions() {
  const res = await api.get<SystemOptionsResponse>('/api/option/')
  return res.data
}

export async function updateSystemOption(request: UpdateOptionRequest) {
  const res = await api.put<UpdateOptionResponse>('/api/option/', request)
  return res.data
}

export async function getCreativeModelPolicy() {
  const res = await api.get<CreativeModelPolicyResponse>(
    '/api/creative/model-policy'
  )
  return res.data
}

async function getCreativeNonceHeaders(): Promise<Record<string, string>> {
  const res = await api.get<{
    success: boolean
    data?: {
      auth?: {
        csrfToken?: string
        nonce?: string
      }
    }
  }>('/creative/api/bootstrap', { skipErrorHandler: true })
  const auth = res.data.data?.auth
  if (!auth?.csrfToken || !auth?.nonce) {
    throw new Error('Creative session nonce is unavailable')
  }
  return {
    'X-Creative-CSRF': auth.csrfToken,
    'X-Creative-Nonce': auth.nonce,
  }
}

export async function updateCreativeModelPolicy(
  request: UpdateCreativeModelPolicyRequest
) {
  const creativeHeaders = await getCreativeNonceHeaders()
  const res = await api.put<CreativeModelPolicyResponse>(
    '/api/creative/model-policy',
    request,
    { headers: creativeHeaders }
  )
  return res.data
}

export async function getCreativeModelBindings() {
  const res = await api.get<CreativeModelBindingsResponse>(
    '/api/creative/model-bindings'
  )
  return res.data
}

export async function validateCreativeModelBindings(
  request: UpdateCreativeModelBindingsRequest
) {
  const creativeHeaders = await getCreativeNonceHeaders()
  const res = await api.post<ValidateCreativeModelBindingsResponse>(
    '/api/creative/model-bindings/validate',
    request,
    { headers: creativeHeaders }
  )
  return res.data
}

export async function dryRunCreativeModelBindings(
  request: UpdateCreativeModelBindingsRequest
) {
  const creativeHeaders = await getCreativeNonceHeaders()
  const res = await api.post<CreativeModelBindingsDryRunResponse>(
    '/api/creative/model-bindings/dry-run',
    request,
    { headers: creativeHeaders }
  )
  return res.data
}

export async function updateCreativeModelBindings(
  request: UpdateCreativeModelBindingsRequest
) {
  const creativeHeaders = await getCreativeNonceHeaders()
  const res = await api.put<CreativeModelBindingsResponse>(
    '/api/creative/model-bindings',
    request,
    { headers: creativeHeaders }
  )
  return res.data
}

export async function confirmPaymentCompliance() {
  const res = await api.post<ConfirmPaymentComplianceResponse>(
    '/api/option/payment_compliance',
    { confirmed: true }
  )
  return res.data
}

export async function deleteLogsBefore(targetTimestamp: number) {
  const res = await api.delete<DeleteLogsResponse>('/api/log/', {
    params: { target_timestamp: targetTimestamp },
  })
  return res.data
}

export async function resetModelRatios() {
  const res = await api.post<UpdateOptionResponse>(
    '/api/option/rest_model_ratio'
  )
  return res.data
}

export async function getUpstreamChannels() {
  const res = await api.get<UpstreamChannelsResponse>(
    '/api/ratio_sync/channels'
  )
  return res.data
}

export async function fetchUpstreamRatios(request: FetchUpstreamRatiosRequest) {
  const res = await api.post<UpstreamRatiosResponse>(
    '/api/ratio_sync/fetch',
    request
  )
  return res.data
}
