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
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  NativeSelect,
  NativeSelectOption,
} from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import { getCreativeModelPolicy, updateCreativeModelPolicy } from '../api'
import { SettingsCard } from '../components/settings-card'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import type {
  CreativeEffectiveModelPolicy,
  CreativeModelPolicy,
  CreativeModelPolicyGroupPool,
  CreativeModelPolicyRule,
  CreativeModelPolicyState,
} from '../types'

const queryKey = ['creative-model-policy']

const modalityLabelKeys: Record<string, string> = {
  text: 'Text',
  agent: 'Agent',
  image: 'Image',
  video: 'Video',
  audio: 'Audio',
}

type PolicyScope =
  | { kind: 'global' }
  | { kind: 'group'; group: string }

type ModelsByModality = Record<string, string[]>

function parseJsonEditorValue(value: string): unknown {
  const trimmed = value.trim()
  if (!trimmed) return { version: 1 }
  return JSON.parse(trimmed)
}

function formatPolicyJson(policy: CreativeModelPolicy): string {
  return JSON.stringify(policy, null, 2)
}

function formatJsonText(value: string): string {
  const parsed = parseJsonEditorValue(value)
  return JSON.stringify(parsed, null, 2)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function cloneStringMap(value: unknown): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined
  const result: Record<string, string> = {}
  for (const key of Object.keys(value)) {
    const item = value[key]
    if (typeof item !== 'string') continue
    const trimmed = item.trim()
    if (trimmed) result[key] = trimmed
  }
  if (Object.keys(result).length === 0) return undefined
  return result
}

function cloneStringArrayMap(
  value: unknown
): Record<string, string[]> | undefined {
  if (!isRecord(value)) return undefined
  const result: Record<string, string[]> = {}
  for (const key of Object.keys(value)) {
    const item = value[key]
    if (!Array.isArray(item)) continue
    const models = item
      .filter((modelId): modelId is string => typeof modelId === 'string')
      .map((modelId) => modelId.trim())
      .filter(Boolean)
    const deduped = Array.from(new Set(models))
    if (deduped.length > 0) result[key] = deduped
  }
  if (Object.keys(result).length === 0) return undefined
  return result
}

function cloneRule(rule: CreativeModelPolicyRule | undefined) {
  if (!rule) return {}
  const cloned: CreativeModelPolicyRule = {}
  const defaults = cloneStringMap(rule.defaults)
  const recommended = cloneStringArrayMap(rule.recommended)
  if (defaults) cloned.defaults = defaults
  if (recommended) cloned.recommended = recommended
  return cloned
}

function coercePolicy(value: unknown): CreativeModelPolicy {
  if (!isRecord(value)) return { version: 1 }

  const version = typeof value.version === 'number' ? value.version : 1
  const policy: CreativeModelPolicy = { version }

  if (isRecord(value.global)) {
    const globalRule = cloneRule(value.global as CreativeModelPolicyRule)
    if (hasPolicyRuleEntries(globalRule)) policy.global = globalRule
  }

  if (isRecord(value.groups)) {
    for (const group of Object.keys(value.groups).sort()) {
      const rawRule = value.groups[group]
      if (!isRecord(rawRule)) continue
      const rule = cloneRule(rawRule as CreativeModelPolicyRule)
      if (!hasPolicyRuleEntries(rule)) continue
      if (!policy.groups) policy.groups = {}
      policy.groups[group] = rule
    }
  }

  return policy
}

function clonePolicy(policy: CreativeModelPolicy | undefined) {
  if (!policy) return { version: 1 }
  return coercePolicy(policy)
}

function compactRule(
  rule: CreativeModelPolicyRule | undefined,
  allowedModalities: string[]
): CreativeModelPolicyRule {
  const compacted: CreativeModelPolicyRule = {}

  for (const modality of allowedModalities) {
    const defaultModel = rule?.defaults?.[modality]?.trim()
    if (defaultModel) {
      if (!compacted.defaults) compacted.defaults = {}
      compacted.defaults[modality] = defaultModel
    }

    const recommended = rule?.recommended?.[modality] ?? []
    const models = recommended
      .map((modelId) => modelId.trim())
      .filter(Boolean)
    const deduped = Array.from(new Set(models))
    if (deduped.length > 0) {
      if (!compacted.recommended) compacted.recommended = {}
      compacted.recommended[modality] = deduped
    }
  }

  return compacted
}

function compactPolicy(
  policy: CreativeModelPolicy,
  allowedModalities: string[]
): CreativeModelPolicy {
  const compacted: CreativeModelPolicy = { version: policy.version || 1 }
  const globalRule = compactRule(policy.global, allowedModalities)
  if (hasPolicyRuleEntries(globalRule)) compacted.global = globalRule

  const groups = policy.groups ?? {}
  for (const group of Object.keys(groups).sort()) {
    const rule = compactRule(groups[group], allowedModalities)
    if (!hasPolicyRuleEntries(rule)) continue
    if (!compacted.groups) compacted.groups = {}
    compacted.groups[group] = rule
  }

  return compacted
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

function countPolicyOverrides(policy: CreativeModelPolicy | undefined) {
  if (!policy) return 0
  const rules = [policy.global, ...Object.values(policy.groups ?? {})]
  return rules.reduce((total, rule) => {
    if (!rule) return total
    const defaultCount = Object.keys(rule.defaults ?? {}).length
    const recommendedCount = Object.values(rule.recommended ?? {}).reduce(
      (sum, models) => sum + models.length,
      0
    )
    return total + defaultCount + recommendedCount
  }, 0)
}

function countStaleEntries(
  staleByGroup: CreativeModelPolicyState['diagnostics']['staleByGroup']
) {
  if (!staleByGroup) return 0
  return Object.values(staleByGroup).reduce((total, rule) => {
    const defaultCount = Object.keys(rule.defaults ?? {}).length
    const recommendedCount = Object.values(rule.recommended ?? {}).reduce(
      (sum, models) => sum + models.length,
      0
    )
    return total + defaultCount + recommendedCount
  }, 0)
}

function distinctModelCount(pools: CreativeModelPolicyGroupPool[]) {
  const models = new Set<string>()
  for (const pool of pools) {
    for (const modelId of pool.models) models.add(modelId)
  }
  return models.size
}

function getPoolModelsByModality(
  pool: CreativeModelPolicyGroupPool,
  allowedModalities: string[]
) {
  const result: ModelsByModality = {}
  for (const modality of allowedModalities) {
    if (
      pool.modelsByModality &&
      Object.prototype.hasOwnProperty.call(pool.modelsByModality, modality)
    ) {
      result[modality] = pool.modelsByModality[modality] ?? []
    } else {
      result[modality] = pool.models
    }
  }
  return result
}

function buildGlobalModelsByModality(
  pools: CreativeModelPolicyGroupPool[],
  allowedModalities: string[]
) {
  const result: ModelsByModality = {}
  const seenByModality: Record<string, Set<string>> = {}

  for (const modality of allowedModalities) {
    result[modality] = []
    seenByModality[modality] = new Set<string>()
  }

  for (const pool of pools) {
    const poolModelsByModality = getPoolModelsByModality(pool, allowedModalities)
    for (const modality of allowedModalities) {
      const seen = seenByModality[modality]
      for (const modelId of poolModelsByModality[modality] ?? []) {
        if (seen.has(modelId)) continue
        seen.add(modelId)
        result[modality].push(modelId)
      }
    }
  }

  return result
}

function setRuleDefault(
  rule: CreativeModelPolicyRule | undefined,
  modality: string,
  modelId: string
) {
  const next = cloneRule(rule)
  if (modelId.trim()) {
    next.defaults = { ...(next.defaults ?? {}), [modality]: modelId.trim() }
  } else if (next.defaults) {
    delete next.defaults[modality]
    if (Object.keys(next.defaults).length === 0) delete next.defaults
  }
  return next
}

function addRuleRecommended(
  rule: CreativeModelPolicyRule | undefined,
  modality: string,
  modelId: string
) {
  const trimmed = modelId.trim()
  if (!trimmed) return cloneRule(rule)
  const next = cloneRule(rule)
  const existing = next.recommended?.[modality] ?? []
  const models = Array.from(new Set([...existing, trimmed]))
  next.recommended = { ...(next.recommended ?? {}), [modality]: models }
  return next
}

function removeRuleRecommended(
  rule: CreativeModelPolicyRule | undefined,
  modality: string,
  modelId: string
) {
  const next = cloneRule(rule)
  const models = (next.recommended?.[modality] ?? []).filter(
    (item) => item !== modelId
  )
  if (models.length > 0) {
    next.recommended = { ...(next.recommended ?? {}), [modality]: models }
    return next
  }
  if (next.recommended) {
    delete next.recommended[modality]
    if (Object.keys(next.recommended).length === 0) delete next.recommended
  }
  return next
}

function updatePolicyRule(
  policy: CreativeModelPolicy,
  scope: PolicyScope,
  updater: (rule: CreativeModelPolicyRule | undefined) => CreativeModelPolicyRule
) {
  const next = clonePolicy(policy)
  if (scope.kind === 'global') {
    const updated = updater(next.global)
    if (hasPolicyRuleEntries(updated)) {
      next.global = updated
    } else {
      delete next.global
    }
    return next
  }

  const currentRule = next.groups?.[scope.group]
  const updated = updater(currentRule)
  if (hasPolicyRuleEntries(updated)) {
    next.groups = { ...(next.groups ?? {}), [scope.group]: updated }
  } else if (next.groups) {
    delete next.groups[scope.group]
    if (Object.keys(next.groups).length === 0) delete next.groups
  }
  return next
}

function getRuleForScope(policy: CreativeModelPolicy, scope: PolicyScope) {
  if (scope.kind === 'global') return policy.global
  return policy.groups?.[scope.group]
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

function PolicyOverview(props: {
  policyData: CreativeModelPolicyState
  draftPolicy: CreativeModelPolicy
}) {
  const { t } = useTranslation()
  const staleCount = countStaleEntries(props.policyData.diagnostics.staleByGroup)
  const cards = [
    {
      label: t('Policy version'),
      value: String(props.draftPolicy.version || 1),
      help: t('Saved through the dedicated Creative policy endpoint.'),
    },
    {
      label: t('Groups'),
      value: String(props.policyData.modelPools.length),
      help: t('Groups with available Creative model pools.'),
    },
    {
      label: t('Available models'),
      value: String(distinctModelCount(props.policyData.modelPools)),
      help: t('Logical model IDs from enabled channels.'),
    },
    {
      label: t('Policy choices'),
      value: String(countPolicyOverrides(props.draftPolicy)),
      help: t('Defaults and recommendations configured below.'),
    },
    {
      label: t('Stale entries'),
      value: String(staleCount),
      help: t('Entries no longer present in the current model pool.'),
    },
  ]

  return (
    <div className='grid gap-3 md:grid-cols-2 xl:grid-cols-5'>
      {cards.map((card) => (
        <div key={card.label} className='rounded-xl border p-3'>
          <div className='text-muted-foreground text-xs'>{card.label}</div>
          <div className='mt-1 text-2xl font-semibold'>{card.value}</div>
          <div className='text-muted-foreground mt-1 text-xs'>{card.help}</div>
        </div>
      ))}
    </div>
  )
}

function ModelSelect(props: {
  value: string
  options: string[]
  emptyLabel: string
  stalePrefix: string
  disabled?: boolean
  onChange: (modelId: string) => void
}) {
  const includesValue = props.options.includes(props.value)
  const showStale = props.value && !includesValue

  return (
    <NativeSelect
      className='w-full'
      value={props.value}
      disabled={props.disabled}
      onChange={(event) => props.onChange(event.target.value)}
    >
      <NativeSelectOption value=''>{props.emptyLabel}</NativeSelectOption>
      {showStale && (
        <NativeSelectOption value={props.value} disabled>
          {props.stalePrefix}: {props.value}
        </NativeSelectOption>
      )}
      {props.options.map((modelId) => (
        <NativeSelectOption key={modelId} value={modelId}>
          {modelId}
        </NativeSelectOption>
      ))}
    </NativeSelect>
  )
}

function RecommendedModelChips(props: {
  modality: string
  models: string[]
  availableModels: string[]
  onRemove: (modelId: string) => void
}) {
  const { t } = useTranslation()
  if (props.models.length === 0) {
    return (
      <p className='text-muted-foreground text-xs'>
        {t('No recommended models configured')}
      </p>
    )
  }

  return (
    <div className='flex flex-wrap gap-1.5'>
      {props.models.map((modelId) => {
        const available = props.availableModels.includes(modelId)
        return (
          <Badge
            key={`${props.modality}-${modelId}`}
            variant={available ? 'secondary' : 'destructive'}
            className='h-auto py-1 pr-1'
          >
            <span>{modelId}</span>
            <button
              type='button'
              className='hover:bg-background/60 rounded-full px-1'
              onClick={() => props.onRemove(modelId)}
              aria-label={t('Remove {{model}}', { model: modelId })}
            >
              ×
            </button>
          </Badge>
        )
      })}
    </div>
  )
}

function PolicyRuleEditor(props: {
  title: string
  description: string
  rule: CreativeModelPolicyRule | undefined
  allowedModalities: string[]
  modelsByModality: ModelsByModality
  emptyDefaultLabel: string
  onDefaultChange: (modality: string, modelId: string) => void
  onAddRecommended: (modality: string, modelId: string) => void
  onRemoveRecommended: (modality: string, modelId: string) => void
}) {
  const { t } = useTranslation()

  return (
    <div className='space-y-4 rounded-xl border p-4'>
      <div className='space-y-1'>
        <h3 className='font-medium'>{props.title}</h3>
        <p className='text-muted-foreground text-sm'>{props.description}</p>
      </div>

      <div className='space-y-3'>
        {props.allowedModalities.map((modality) => {
          const availableModels = props.modelsByModality[modality] ?? []
          const currentDefault = props.rule?.defaults?.[modality] ?? ''
          const recommended = props.rule?.recommended?.[modality] ?? []
          const addableModels = availableModels.filter(
            (modelId) => !recommended.includes(modelId)
          )

          return (
            <div key={modality} className='rounded-lg border p-3'>
              <div className='grid gap-3 lg:grid-cols-[140px_minmax(220px,1fr)_minmax(260px,1.5fr)]'>
                <div className='space-y-1'>
                  <div className='font-medium'>
                    {t(modalityLabelKeys[modality] ?? modality)}
                  </div>
                  <div className='text-muted-foreground text-xs'>
                    {t('{{count}} models', {
                      count: availableModels.length,
                    })}
                  </div>
                </div>

                <div className='space-y-1.5'>
                  <div className='text-muted-foreground text-xs'>
                    {t('Default model')}
                  </div>
                  <ModelSelect
                    value={currentDefault}
                    options={availableModels}
                    emptyLabel={props.emptyDefaultLabel}
                    stalePrefix={t('Stale')}
                    disabled={availableModels.length === 0 && !currentDefault}
                    onChange={(modelId) =>
                      props.onDefaultChange(modality, modelId)
                    }
                  />
                </div>

                <div className='space-y-2'>
                  <div className='flex flex-wrap items-center justify-between gap-2'>
                    <div className='text-muted-foreground text-xs'>
                      {t('Recommended models')}
                    </div>
                    <NativeSelect
                      size='sm'
                      className='w-full sm:w-64'
                      value=''
                      disabled={addableModels.length === 0}
                      onChange={(event) => {
                        props.onAddRecommended(modality, event.target.value)
                      }}
                    >
                      <NativeSelectOption value=''>
                        {t('Add recommended model')}
                      </NativeSelectOption>
                      {addableModels.map((modelId) => (
                        <NativeSelectOption key={modelId} value={modelId}>
                          {modelId}
                        </NativeSelectOption>
                      ))}
                    </NativeSelect>
                  </div>
                  <RecommendedModelChips
                    modality={modality}
                    models={recommended}
                    availableModels={availableModels}
                    onRemove={(modelId) =>
                      props.onRemoveRecommended(modality, modelId)
                    }
                  />
                </div>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function GroupPoolPreview(props: {
  pool: CreativeModelPolicyGroupPool
  allowedModalities: string[]
}) {
  const { t } = useTranslation()
  const visibleModels = props.pool.models.slice(0, 18)
  const hiddenCount = Math.max(
    props.pool.models.length - visibleModels.length,
    0
  )
  const staleRule = props.pool.effectivePolicy.stale
  const modelsByModality = getPoolModelsByModality(
    props.pool,
    props.allowedModalities
  )

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

      <div className='mt-3 flex flex-wrap gap-1.5'>
        {props.allowedModalities.map((modality) => (
          <Badge key={modality} variant='secondary'>
            {t(modalityLabelKeys[modality] ?? modality)}:{' '}
            {modelsByModality[modality]?.length ?? 0}
          </Badge>
        ))}
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

function DiagnosticsCard(props: {
  policyData: CreativeModelPolicyState
  isSaving: boolean
  onLoadCleaned: () => void
  onApplyCleaned: () => void
}) {
  const { t } = useTranslation()
  const staleByGroup = props.policyData.diagnostics.staleByGroup ?? {}
  const groupEntries = Object.entries(staleByGroup).filter(([, rule]) =>
    hasPolicyRuleEntries(rule)
  )

  return (
    <SettingsCard
      title={t('Diagnostics and cleanup')}
      description={t(
        'Stale entries are not sent to OpenTU as executable defaults. You can clean the saved policy after reviewing them.'
      )}
    >
      {groupEntries.length === 0 && (
        <Alert>
          <CheckCircle2 />
          <AlertTitle>{t('No stale entries detected')}</AlertTitle>
          <AlertDescription>
            {t('The saved policy only references models in current pools.')}
          </AlertDescription>
        </Alert>
      )}

      {groupEntries.length > 0 && (
        <div className='space-y-3'>
          <Alert variant='destructive'>
            <AlertTriangle />
            <AlertTitle>{t('Stale model policy entries detected')}</AlertTitle>
            <AlertDescription>
              {t(
                'Stale entries are diagnostics only and are never returned as selectable Creative defaults.'
              )}
            </AlertDescription>
          </Alert>

          <div className='space-y-3'>
            {groupEntries.map(([group, rule]) => (
              <div key={group} className='rounded-lg border p-3'>
                <PolicyRuleSummary
                  title={group}
                  rule={rule}
                  emptyText={t('No stale entries detected')}
                />
              </div>
            ))}
          </div>

          <div className='flex flex-wrap gap-2'>
            <Button type='button' variant='outline' onClick={props.onLoadCleaned}>
              {t('Load cleaned policy into builder')}
            </Button>
            <Button
              type='button'
              onClick={props.onApplyCleaned}
              disabled={props.isSaving}
            >
              {props.isSaving ? t('Saving...') : t('Apply cleaned policy')}
            </Button>
          </div>
        </div>
      )}
    </SettingsCard>
  )
}

type BuilderState = {
  draftPolicy: CreativeModelPolicy
  editorValue: string
}

function createBuilderState(policyData: CreativeModelPolicyState): BuilderState {
  return {
    draftPolicy: clonePolicy(policyData.policy),
    editorValue: formatJsonText(policyData.policyJSON),
  }
}

function createBuilderStateFromDraft(
  draftPolicy: CreativeModelPolicy,
  allowedModalities: string[]
): BuilderState {
  return {
    draftPolicy,
    editorValue: formatPolicyJson(compactPolicy(draftPolicy, allowedModalities)),
  }
}

export function CreativeModelPolicySection() {
  const { t } = useTranslation()

  const policyQuery = useQuery({
    queryKey,
    queryFn: getCreativeModelPolicy,
  })

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

  const policyData = policyQuery.data?.data
  if (!policyData) return null

  return (
    <CreativeModelPolicyLoaded
      key={policyData.policyJSON}
      policyData={policyData}
    />
  )
}

function CreativeModelPolicyLoaded(props: {
  policyData: CreativeModelPolicyState
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [builderState, setBuilderState] = useState<BuilderState>(() =>
    createBuilderState(props.policyData)
  )
  const allowedModalities = props.policyData.allowedModalities
  const draftPolicy = builderState.draftPolicy
  const editorValue = builderState.editorValue

  const updateMutation = useMutation({
    mutationFn: updateCreativeModelPolicy,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to update setting'))
        return
      }
      setBuilderState(createBuilderState(response.data))
      queryClient.setQueryData(queryKey, response)
      queryClient.invalidateQueries({ queryKey })
      toast.success(t('Setting updated successfully'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update setting'))
    },
  })

  const globalModelsByModality = useMemo(() => {
    return buildGlobalModelsByModality(
      props.policyData.modelPools,
      allowedModalities
    )
  }, [allowedModalities, props.policyData.modelPools])

  const updateRule = (
    scope: PolicyScope,
    updater: (rule: CreativeModelPolicyRule | undefined) => CreativeModelPolicyRule
  ) => {
    setBuilderState((current) => {
      const nextPolicy = updatePolicyRule(
        current.draftPolicy,
        scope,
        updater
      )
      return createBuilderStateFromDraft(nextPolicy, allowedModalities)
    })
  }

  const handleGuidedSave = () => {
    updateMutation.mutate({
      policy: compactPolicy(draftPolicy, allowedModalities),
    })
  }

  const handleFormat = () => {
    try {
      setBuilderState((current) => ({
        ...current,
        editorValue: formatJsonText(current.editorValue),
      }))
    } catch {
      toast.error(t('Invalid JSON format'))
    }
  }

  const handleLoadCleanedPolicy = () => {
    if (!props.policyData.cleanedPolicyJSON) return
    const cleaned = clonePolicy(props.policyData.cleanedPolicy)
    setBuilderState({
      draftPolicy: cleaned,
      editorValue: formatJsonText(props.policyData.cleanedPolicyJSON),
    })
    toast.info(t('Cleaned policy loaded into builder'))
  }

  const handleApplyCleanedPolicy = () => {
    updateMutation.mutate({ policy: props.policyData.cleanedPolicy })
  }

  const handleApplyJsonToBuilder = () => {
    let parsed: unknown
    try {
      parsed = parseJsonEditorValue(editorValue)
    } catch {
      toast.error(t('Invalid JSON format'))
      return
    }
    const policy = coercePolicy(parsed)
    setBuilderState(createBuilderStateFromDraft(policy, allowedModalities))
    toast.info(t('JSON loaded into builder'))
  }

  const handleJsonSave = () => {
    let parsed: unknown
    try {
      parsed = parseJsonEditorValue(editorValue)
    } catch {
      toast.error(t('Invalid JSON format'))
      return
    }
    updateMutation.mutate({ policy: parsed as Record<string, unknown> })
  }

  return (
    <SettingsSection title={t('Creative Model Policy')}>
      <SettingsPageFormActions
        onSave={handleGuidedSave}
        isSaving={updateMutation.isPending}
      />

      <Alert>
        <CheckCircle2 />
        <AlertTitle>{t('New API owns Creative model routing')}</AlertTitle>
        <AlertDescription>
          {t(
            'Channels and group model pools define availability. This policy only chooses defaults and recommendations from those pools; OpenTU never receives provider keys, base URLs, or channel routing authority.'
          )}
        </AlertDescription>
      </Alert>

      <PolicyOverview
        policyData={props.policyData}
        draftPolicy={draftPolicy}
      />

      <SettingsCard
        title={t('Guided policy builder')}
        description={t(
          'Use model pickers instead of raw JSON. The save action still goes through backend validation and normalization.'
        )}
      >
        <div className='space-y-4'>
          <PolicyRuleEditor
            title={t('Global defaults and recommendations')}
            description={t(
              'Global choices apply to every group unless a group override is configured for the same modality.'
            )}
            rule={draftPolicy.global}
            allowedModalities={allowedModalities}
            modelsByModality={globalModelsByModality}
            emptyDefaultLabel={t('No default')}
            onDefaultChange={(modality, modelId) =>
              updateRule({ kind: 'global' }, (rule) =>
                setRuleDefault(rule, modality, modelId)
              )
            }
            onAddRecommended={(modality, modelId) =>
              updateRule({ kind: 'global' }, (rule) =>
                addRuleRecommended(rule, modality, modelId)
              )
            }
            onRemoveRecommended={(modality, modelId) =>
              updateRule({ kind: 'global' }, (rule) =>
                removeRuleRecommended(rule, modality, modelId)
              )
            }
          />

          <div className='space-y-3'>
            <div className='flex items-center gap-2'>
              <SlidersHorizontal className='text-muted-foreground h-4 w-4' />
              <div>
                <h3 className='font-medium'>{t('Group overrides')}</h3>
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'Override only the modalities that should differ from the global policy. Empty groups inherit global choices.'
                  )}
                </p>
              </div>
            </div>

            {props.policyData.modelPools.map((pool) => {
              const scope: PolicyScope = { kind: 'group', group: pool.group }
              const rule = getRuleForScope(draftPolicy, scope)
              const overrideCount = countPolicyOverrides({
                version: 1,
                global: rule,
              })

              return (
                <div key={pool.group} className='space-y-3 rounded-xl border p-4'>
                  <div className='flex flex-wrap items-start justify-between gap-3'>
                    <div>
                      <div className='flex flex-wrap items-center gap-2'>
                        <h4 className='font-medium'>{pool.group}</h4>
                        <Badge variant='secondary'>
                          {t('{{count}} models', { count: pool.modelCount })}
                        </Badge>
                        <Badge variant={overrideCount > 0 ? 'default' : 'outline'}>
                          {overrideCount > 0
                            ? t('{{count}} override', { count: overrideCount })
                            : t('Inherits global')}
                        </Badge>
                      </div>
                      {pool.description && (
                        <p className='text-muted-foreground mt-1 text-xs'>
                          {pool.description}
                        </p>
                      )}
                    </div>
                  </div>

                  <PolicyRuleEditor
                    title={t('Override policy')}
                    description={t(
                      'Pick only from models currently available to this group.'
                    )}
                    rule={rule}
                    allowedModalities={allowedModalities}
                    modelsByModality={getPoolModelsByModality(
                      pool,
                      allowedModalities
                    )}
                    emptyDefaultLabel={t('Inherit global')}
                    onDefaultChange={(modality, modelId) =>
                      updateRule(scope, (currentRule) =>
                        setRuleDefault(currentRule, modality, modelId)
                      )
                    }
                    onAddRecommended={(modality, modelId) =>
                      updateRule(scope, (currentRule) =>
                        addRuleRecommended(currentRule, modality, modelId)
                      )
                    }
                    onRemoveRecommended={(modality, modelId) =>
                      updateRule(scope, (currentRule) =>
                        removeRuleRecommended(currentRule, modality, modelId)
                      )
                    }
                  />
                </div>
              )
            })}
          </div>
        </div>
      </SettingsCard>

      <DiagnosticsCard
        policyData={props.policyData}
        isSaving={updateMutation.isPending}
        onLoadCleaned={handleLoadCleanedPolicy}
        onApplyCleaned={handleApplyCleanedPolicy}
      />

      <SettingsCard
        title={t('Group and model-pool preview')}
        description={t(
          'Each preview uses the same effective pool calculation as Creative bootstrap for that user group.'
        )}
      >
        <div className='space-y-3'>
          {props.policyData.modelPools.map((pool) => (
            <GroupPoolPreview
              key={pool.group}
              pool={pool}
              allowedModalities={allowedModalities}
            />
          ))}
        </div>
      </SettingsCard>

      <SettingsCard
        title={t('Advanced JSON')}
        description={t(
          'Use this only for expert recovery or bulk edits. Guided controls above remain the default workflow.'
        )}
      >
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <CollapsibleTrigger
            render={
              <Button type='button' variant='outline'>
                {advancedOpen ? t('Hide JSON editor') : t('Show JSON editor')}
              </Button>
            }
          />
          <CollapsibleContent className='mt-3 space-y-3'>
            <Textarea
              className='min-h-[260px] font-mono text-xs'
              spellCheck={false}
              value={editorValue}
              onChange={(event) =>
                setBuilderState((current) => ({
                  ...current,
                  editorValue: event.target.value,
                }))
              }
            />
            <div className='flex flex-wrap gap-2'>
              <Button type='button' variant='outline' onClick={handleFormat}>
                {t('Format JSON')}
              </Button>
              <Button
                type='button'
                variant='outline'
                onClick={handleLoadCleanedPolicy}
                disabled={!props.policyData.cleanedPolicyJSON}
              >
                {t('Load cleaned policy')}
              </Button>
              <Button
                type='button'
                variant='outline'
                onClick={handleApplyJsonToBuilder}
              >
                {t('Load JSON into builder')}
              </Button>
              <Button
                type='button'
                onClick={handleJsonSave}
                disabled={updateMutation.isPending}
              >
                {updateMutation.isPending ? t('Saving...') : t('Save JSON')}
              </Button>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </SettingsCard>
    </SettingsSection>
  )
}
