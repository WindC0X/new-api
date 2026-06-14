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
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { getCreativeModelPolicy, updateCreativeModelPolicy } from '../api'
import { SettingsCard } from '../components/settings-card'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import type {
  CreativeEffectiveModelPolicy,
  CreativeModelPolicyGroupPool,
  CreativeModelPolicyRule,
} from '../types'

const queryKey = ['creative-model-policy']

const modalityLabelKeys: Record<string, string> = {
  text: 'Text',
  agent: 'Agent',
  image: 'Image',
  video: 'Video',
  audio: 'Audio',
}

function parseJsonEditorValue(value: string): unknown {
  const trimmed = value.trim()
  if (!trimmed) return { version: 1 }
  return JSON.parse(trimmed)
}

function formatJsonText(value: string): string {
  const parsed = parseJsonEditorValue(value)
  return JSON.stringify(parsed, null, 2)
}

function hasPolicyRuleEntries(rule: CreativeModelPolicyRule | undefined) {
  if (!rule) return false
  const defaultCount = Object.keys(rule.defaults ?? {}).length
  const recommendedCount = Object.values(rule.recommended ?? {}).reduce(
    (total, models) => total + models.length,
    0
  )
  return defaultCount > 0 || recommendedCount > 0
}

function PolicyRuleSummary(props: {
  title: string
  rule: CreativeModelPolicyRule | undefined
  emptyText: string
}) {
  const { t } = useTranslation()

  if (!hasPolicyRuleEntries(props.rule)) {
    return <p className='text-muted-foreground text-xs'>{props.emptyText}</p>
  }

  const defaults = Object.entries(props.rule?.defaults ?? {})
  const recommended = Object.entries(props.rule?.recommended ?? {})

  return (
    <div className='space-y-2'>
      <p className='text-xs font-medium'>{props.title}</p>
      {defaults.length > 0 && (
        <div className='space-y-1'>
          <p className='text-muted-foreground text-xs'>{t('Defaults')}</p>
          <div className='flex flex-wrap gap-1.5'>
            {defaults.map(([modality, modelId]) => (
              <Badge key={`${modality}-${modelId}`} variant='secondary'>
                {t(modalityLabelKeys[modality] ?? modality)}: {modelId}
              </Badge>
            ))}
          </div>
        </div>
      )}
      {recommended.length > 0 && (
        <div className='space-y-1'>
          <p className='text-muted-foreground text-xs'>{t('Recommended')}</p>
          <div className='flex flex-wrap gap-1.5'>
            {recommended.flatMap(([modality, models]) =>
              models.map((modelId) => (
                <Badge key={`${modality}-${modelId}`} variant='outline'>
                  {t(modalityLabelKeys[modality] ?? modality)}: {modelId}
                </Badge>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function EffectivePolicySummary(props: {
  policy: CreativeEffectiveModelPolicy
}) {
  const { t } = useTranslation()
  const rule: CreativeModelPolicyRule = {
    defaults: props.policy.defaults,
    recommended: props.policy.recommended,
  }
  return (
    <PolicyRuleSummary
      title={t('Effective policy')}
      rule={rule}
      emptyText={t('No effective defaults or recommendations for this group.')}
    />
  )
}

function GroupPoolPreview(props: { pool: CreativeModelPolicyGroupPool }) {
  const { t } = useTranslation()
  const visibleModels = props.pool.models.slice(0, 18)
  const hiddenCount = Math.max(
    props.pool.models.length - visibleModels.length,
    0
  )
  const staleRule = props.pool.effectivePolicy.stale

  return (
    <div className='rounded-xl border p-4'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0 space-y-1'>
          <div className='flex flex-wrap items-center gap-2'>
            <h4 className='font-medium'>{props.pool.group}</h4>
            <Badge variant='secondary'>
              {props.pool.modelCount} {t('models')}
            </Badge>
          </div>
          {props.pool.description && (
            <p className='text-muted-foreground text-xs'>
              {props.pool.description}
            </p>
          )}
        </div>
      </div>

      <div className='mt-3 flex flex-wrap gap-1.5'>
        {visibleModels.map((modelId) => (
          <Badge key={modelId} variant='outline'>
            {modelId}
          </Badge>
        ))}
        {hiddenCount > 0 && <Badge variant='secondary'>+{hiddenCount}</Badge>}
        {props.pool.models.length === 0 && (
          <span className='text-muted-foreground text-xs'>
            {t('No available models')}
          </span>
        )}
      </div>

      <div className='mt-4 grid gap-4 lg:grid-cols-2'>
        <EffectivePolicySummary policy={props.pool.effectivePolicy} />
        <PolicyRuleSummary
          title={t('Stale diagnostics')}
          rule={staleRule}
          emptyText={t('No stale entries detected')}
        />
      </div>
    </div>
  )
}

export function CreativeModelPolicySection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editorValue, setEditorValue] = useState('')

  const policyQuery = useQuery({
    queryKey,
    queryFn: getCreativeModelPolicy,
  })

  const policyData = policyQuery.data?.data

  useEffect(() => {
    if (!policyData?.policyJSON) return
    setEditorValue(formatJsonText(policyData.policyJSON))
  }, [policyData?.policyJSON])

  const updateMutation = useMutation({
    mutationFn: updateCreativeModelPolicy,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to update setting'))
        return
      }
      setEditorValue(formatJsonText(response.data.policyJSON))
      queryClient.setQueryData(queryKey, response)
      queryClient.invalidateQueries({ queryKey })
      toast.success(t('Setting updated successfully'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update setting'))
    },
  })

  const hasStaleEntries = useMemo(() => {
    const staleByGroup = policyData?.diagnostics.staleByGroup ?? {}
    return Object.values(staleByGroup).some((rule) =>
      hasPolicyRuleEntries(rule)
    )
  }, [policyData?.diagnostics.staleByGroup])

  const handleFormat = () => {
    try {
      setEditorValue(formatJsonText(editorValue))
    } catch {
      toast.error(t('Invalid JSON format'))
    }
  }

  const handleLoadCleanedPolicy = () => {
    if (!policyData?.cleanedPolicyJSON) return
    setEditorValue(formatJsonText(policyData.cleanedPolicyJSON))
    toast.info(t('Cleaned policy loaded into editor'))
  }

  const handleSave = () => {
    let parsed: unknown
    try {
      parsed = parseJsonEditorValue(editorValue)
    } catch {
      toast.error(t('Invalid JSON format'))
      return
    }
    updateMutation.mutate({ policy: parsed as Record<string, unknown> })
  }

  if (policyQuery.isLoading) {
    return (
      <SettingsSection title={t('Creative Model Policy')}>
        <SettingsCard title={t('Creative Model Policy')}>
          <div className='text-muted-foreground flex min-h-32 items-center justify-center text-sm'>
            {t('Loading settings...')}
          </div>
        </SettingsCard>
      </SettingsSection>
    )
  }

  if (policyQuery.isError) {
    return (
      <SettingsSection title={t('Creative Model Policy')}>
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Failed to load settings')}</AlertTitle>
          <AlertDescription>
            {policyQuery.error instanceof Error
              ? policyQuery.error.message
              : t('Please try again later')}
          </AlertDescription>
        </Alert>
      </SettingsSection>
    )
  }

  return (
    <SettingsSection title={t('Creative Model Policy')}>
      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={updateMutation.isPending}
      />

      <Alert>
        <CheckCircle2 />
        <AlertTitle>{t('New API owns Creative model routing')}</AlertTitle>
        <AlertDescription>
          {t(
            'Availability comes from Channel and Group configuration. OpenTU only receives logical model IDs; actual upstream routing stays inside new-api.'
          )}
        </AlertDescription>
      </Alert>

      {hasStaleEntries && (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Stale model policy entries detected')}</AlertTitle>
          <AlertDescription>
            {t(
              'Stale entries are diagnostics only and are never returned as selectable Creative defaults.'
            )}
          </AlertDescription>
        </Alert>
      )}

      <SettingsCard
        title={t('Validated JSON editor')}
        description={t(
          'Configure global defaults/recommendations and per-user-group overrides. Unsafe provider, channel, key, base URL, callback, webhook, owner, or user override fields are rejected on save.'
        )}
      >
        <div className='space-y-3'>
          <Textarea
            className='min-h-[360px] font-mono text-xs'
            spellCheck={false}
            value={editorValue}
            onChange={(event) => setEditorValue(event.target.value)}
          />
          <div className='flex flex-wrap gap-2'>
            <Button type='button' variant='outline' onClick={handleFormat}>
              {t('Format JSON')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={handleLoadCleanedPolicy}
              disabled={!policyData?.cleanedPolicyJSON}
            >
              {t('Load cleaned policy')}
            </Button>
            <Button
              type='button'
              onClick={handleSave}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? t('Saving...') : t('Save Policy')}
            </Button>
          </div>
        </div>
      </SettingsCard>

      <SettingsCard
        title={t('Group and model-pool preview')}
        description={t(
          'Each preview uses the same effective pool calculation as Creative bootstrap for that user group.'
        )}
      >
        <div className='space-y-3'>
          {(policyData?.modelPools ?? []).map((pool) => (
            <GroupPoolPreview key={pool.group} pool={pool} />
          ))}
        </div>
      </SettingsCard>
    </SettingsSection>
  )
}
