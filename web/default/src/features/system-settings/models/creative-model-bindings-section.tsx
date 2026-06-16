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
import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  FlaskConical,
  ShieldCheck,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  dryRunCreativeModelBindings,
  getCreativeModelBindings,
  updateCreativeModelBindings,
  validateCreativeModelBindings,
} from '../api'
import { SettingsCard } from '../components/settings-card'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import type {
  CreativeModelBindingsConfig,
  CreativeModelBindingsDryRunResult,
  CreativeModelBindingsState,
} from '../types'

const queryKey = ['creative-model-bindings']

const emptyConfig: CreativeModelBindingsConfig = {
  version: 1,
  bindings: [],
}

const mockTemplate: CreativeModelBindingsConfig = {
  version: 1,
  bindings: [
    {
      id: 'mock:gpt-image-2:preview',
      providerModelId: 'gpt-image-2',
      priceModelId: 'mock-gpt-image-2-price',
      displayName: 'GPT Image 2 · Mock Preview',
      modality: 'image',
      enabled: false,
      canaryGroups: ['test'],
      adapterPreset: 'mock_image_task',
      parameterTemplate: 'mock_gpt_image',
      recommendedScore: 10,
      sortOrder: 1000,
      parameterSchema: [
        {
          id: 'size',
          label: 'Size',
          shortLabel: 'Size',
          description: 'Mock preview image size.',
          type: 'enum',
          defaultValue: '1024x1024',
          options: [
            { value: '1024x1024', label: '1024×1024' },
            { value: '16:9', label: '16:9' },
          ],
          order: 10,
        },
        {
          id: 'quality',
          label: 'Quality',
          shortLabel: 'Quality',
          description: 'Mock preview quality.',
          type: 'enum',
          defaultValue: 'auto',
          options: [
            { value: 'auto', label: 'Auto' },
            { value: 'high', label: 'High' },
          ],
          order: 20,
        },
      ],
    },
  ],
}

const grsaiDryRunTemplate: CreativeModelBindingsConfig = {
  version: 1,
  bindings: [
    {
      id: 'grsai:gpt-image-2:dryrun',
      providerModelId: 'gpt-image-2',
      priceModelId: 'gpt-image-2',
      displayName: 'GrsAI GPT Image 2 · Dry Run Only',
      modality: 'image',
      enabled: false,
      canaryGroups: ['test'],
      adapterPreset: 'grsai_gpt_image_dryrun',
      parameterTemplate: 'grsai_gpt_image',
      recommendedScore: 5,
      sortOrder: 1100,
      parameterSchema: [
        {
          id: 'aspectRatio',
          label: 'Aspect Ratio',
          shortLabel: 'Ratio',
          description: 'Offline GrsAI fixture request preview ratio.',
          type: 'enum',
          defaultValue: '1024x1024',
          options: [
            { value: '1024x1024', label: '1:1' },
            { value: '16:9', label: '16:9' },
            { value: '9:16', label: '9:16' },
          ],
          order: 10,
        },
      ],
    },
  ],
}

function parseEditorValue(value: string): CreativeModelBindingsConfig {
  const trimmed = value.trim()
  if (!trimmed) return emptyConfig
  return JSON.parse(trimmed) as CreativeModelBindingsConfig
}

function formatConfig(config: CreativeModelBindingsConfig): string {
  return JSON.stringify(config, null, 2)
}

function formatEditorValue(value: string): string {
  return formatConfig(parseEditorValue(value))
}

function createEditorValue(state: CreativeModelBindingsState): string {
  try {
    return formatEditorValue(state.configJSON)
  } catch {
    return formatConfig(state.config)
  }
}

function createSummary(config: CreativeModelBindingsConfig) {
  const bindings = Array.isArray(config.bindings) ? config.bindings : []
  return {
    total: bindings.length,
    enabled: bindings.filter((binding) => binding.enabled).length,
    canaryOnly: bindings.filter(
      (binding) => (binding.canaryGroups?.length ?? 0) > 0
    ).length,
    mock: bindings.filter((binding) => binding.adapterPreset === 'mock_image_task')
      .length,
    grsaiDryRun: bindings.filter(
      (binding) => binding.adapterPreset === 'grsai_gpt_image_dryrun'
    ).length,
  }
}

function PreviewBlock(props: { value: unknown }) {
  return (
    <pre className='bg-muted max-h-[420px] overflow-auto rounded-lg border p-3 text-xs whitespace-pre-wrap'>
      {JSON.stringify(props.value, null, 2)}
    </pre>
  )
}

export function CreativeModelBindingsSection() {
  const { t } = useTranslation()

  const bindingsQuery = useQuery({
    queryKey,
    queryFn: getCreativeModelBindings,
  })

  if (bindingsQuery.isLoading) {
    return (
      <SettingsSection title={t('Creative Model Bindings')}>
        <SettingsCard title={t('Creative Model Bindings')}>
          <div className='text-muted-foreground flex min-h-32 items-center justify-center text-sm'>
            {t('Loading settings...')}
          </div>
        </SettingsCard>
      </SettingsSection>
    )
  }

  if (bindingsQuery.isError) {
    return (
      <SettingsSection title={t('Creative Model Bindings')}>
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Failed to load settings')}</AlertTitle>
          <AlertDescription>
            {bindingsQuery.error instanceof Error
              ? bindingsQuery.error.message
              : t('Please try again later')}
          </AlertDescription>
        </Alert>
      </SettingsSection>
    )
  }

  const bindingsResponse = bindingsQuery.data
  const bindingsData = bindingsResponse?.data
  if (!bindingsResponse?.success || !bindingsData) {
    return (
      <SettingsSection title={t('Creative Model Bindings')}>
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Failed to load settings')}</AlertTitle>
          <AlertDescription className='space-y-3'>
            <p>
              {bindingsResponse?.message ||
                t('Server returned an invalid Creative model bindings payload')}
            </p>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => bindingsQuery.refetch()}
              disabled={bindingsQuery.isFetching}
            >
              {bindingsQuery.isFetching ? t('Retrying...') : t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      </SettingsSection>
    )
  }

  return (
    <CreativeModelBindingsLoaded
      key={bindingsData.configJSON}
      bindingsData={bindingsData}
    />
  )
}

function CreativeModelBindingsLoaded(props: {
  bindingsData: CreativeModelBindingsState
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editorValue, setEditorValue] = useState(() =>
    createEditorValue(props.bindingsData)
  )
  const editorValueRef = useRef(editorValue)
  useEffect(() => {
    editorValueRef.current = editorValue
  }, [editorValue])
  const [validatedState, setValidatedState] =
    useState<CreativeModelBindingsState | null>(null)
  const [validatedDraft, setValidatedDraft] = useState<string | null>(null)
  const [dryRunResult, setDryRunResult] =
    useState<CreativeModelBindingsDryRunResult | null>(null)
  const [dryRunDraft, setDryRunDraft] = useState<string | null>(null)

  const parsedSummary = useMemo(() => {
    try {
      return createSummary(parseEditorValue(editorValue))
    } catch {
      return null
    }
  }, [editorValue])

  const updateMutation = useMutation({
    mutationFn: updateCreativeModelBindings,
    onSuccess: (response) => {
      if (!response.success) {
        toast.error(response.message || t('Failed to update setting'))
        return
      }
      setEditorValue(createEditorValue(response.data))
      setValidatedState(response.data)
      setValidatedDraft(null)
      setDryRunResult(null)
      setDryRunDraft(null)
      queryClient.setQueryData(queryKey, response)
      queryClient.invalidateQueries({ queryKey })
      toast.success(t('Setting updated successfully'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Failed to update setting'))
    },
  })

  const validateMutation = useMutation({
    mutationFn: (variables: {
      config: CreativeModelBindingsConfig
      draft: string
    }) => validateCreativeModelBindings({ config: variables.config }),
    onSuccess: (response, variables) => {
      if (!response.success) {
        toast.error(response.message || t('Validation failed'))
        return
      }
      if (variables.draft !== editorValueRef.current) {
        toast.info(t('Validation result ignored because the draft changed'))
        return
      }
      setValidatedState(response.data.state)
      setValidatedDraft(variables.draft)
      toast.success(t('Creative model bindings validated'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Validation failed'))
    },
  })

  const dryRunMutation = useMutation({
    mutationFn: (variables: {
      config: CreativeModelBindingsConfig
      draft: string
    }) => dryRunCreativeModelBindings({ config: variables.config }),
    onSuccess: (response, variables) => {
      if (!response.success) {
        toast.error(response.message || t('Dry run failed'))
        return
      }
      if (variables.draft !== editorValueRef.current) {
        toast.info(t('Dry-run result ignored because the draft changed'))
        return
      }
      setDryRunResult(response.data)
      if (!response.data.noProviderCall) {
        setDryRunDraft(null)
        toast.error(
          t('Dry run reported provider-call risk; save remains disabled')
        )
        return
      }
      setDryRunDraft(variables.draft)
      toast.success(t('Dry run completed without provider calls'))
    },
    onError: (error: Error) => {
      toast.error(error.message || t('Dry run failed'))
    },
  })

  const parseForAction = () => {
    try {
      return parseEditorValue(editorValue)
    } catch {
      toast.error(t('Invalid JSON format'))
      return null
    }
  }

  const handleFormat = () => {
    try {
      setEditorValue(formatEditorValue(editorValue))
      setValidatedState(null)
      setValidatedDraft(null)
      setDryRunResult(null)
      setDryRunDraft(null)
    } catch {
      toast.error(t('Invalid JSON format'))
    }
  }

  const handleReload = async () => {
    const result = await queryClient.fetchQuery({
      queryKey,
      queryFn: getCreativeModelBindings,
    })
    if (result.data) {
      setEditorValue(createEditorValue(result.data))
      setValidatedState(null)
      setValidatedDraft(null)
      setDryRunResult(null)
      setDryRunDraft(null)
      toast.info(t('Server configuration reloaded'))
    }
  }

  const handleValidate = () => {
    const draft = editorValue
    const config = parseForAction()
    if (!config) return
    validateMutation.mutate({ config, draft })
  }

  const handleDryRun = () => {
    const draft = editorValue
    const config = parseForAction()
    if (!config) return
    dryRunMutation.mutate({ config, draft })
  }

  const handleSave = () => {
    const config = parseForAction()
    if (!config) return
    if (
      validatedDraft !== editorValue ||
      dryRunDraft !== editorValue ||
      dryRunResult?.noProviderCall !== true
    ) {
      toast.error(
        t('Validate and dry-run this exact draft before saving bindings')
      )
      return
    }
    updateMutation.mutate({ config })
  }

  const handleLoadTemplate = (config: CreativeModelBindingsConfig) => {
    setEditorValue(formatConfig(config))
    setValidatedState(null)
    setValidatedDraft(null)
    setDryRunResult(null)
    setDryRunDraft(null)
  }

  const isBusy =
    updateMutation.isPending ||
    validateMutation.isPending ||
    dryRunMutation.isPending
  const isSaveReady =
    validatedDraft === editorValue &&
    dryRunDraft === editorValue &&
    dryRunResult?.noProviderCall === true

  return (
    <SettingsSection title={t('Creative Model Bindings')}>
      <SettingsPageFormActions
        onSave={handleSave}
        onReset={handleReload}
        isSaving={updateMutation.isPending}
        isResetDisabled={isBusy}
        isSaveDisabled={isBusy || !isSaveReady}
        saveLabel='Save Bindings'
        savingLabel='Saving...'
        resetLabel='Reload'
      />

      <Alert>
        <ShieldCheck />
        <AlertTitle>{t('Backend-owned Creative adapter bindings')}</AlertTitle>
        <AlertDescription>
          {t(
            'This page writes only through the dedicated Creative model-bindings API. It cannot configure provider keys, base URLs, callbacks, headers, or channel authority through generic system options.'
          )}
        </AlertDescription>
      </Alert>

      <Alert>
        <FlaskConical />
        <AlertTitle>{t('Mock-first provider safety')}</AlertTitle>
        <AlertDescription>
          {t(
            'Duomi live adapters are unavailable. GrsAI is dry-run/fixture only here. Validate and dry-run are nonce-protected and must report noProviderCall before any save is considered safe.'
          )}
        </AlertDescription>
      </Alert>

      <SettingsCard
        title={t('Current draft summary')}
        description={t('Counts are calculated from the JSON editor draft.')}
      >
        {parsedSummary ? (
          <div className='flex flex-wrap gap-2'>
            <Badge variant='secondary'>
              {t('{{count}} bindings', { count: parsedSummary.total })}
            </Badge>
            <Badge variant={parsedSummary.enabled > 0 ? 'default' : 'outline'}>
              {t('{{count}} enabled', { count: parsedSummary.enabled })}
            </Badge>
            <Badge variant='outline'>
              {t('{{count}} canary-only', { count: parsedSummary.canaryOnly })}
            </Badge>
            <Badge variant='outline'>
              {t('{{count}} mock', { count: parsedSummary.mock })}
            </Badge>
            <Badge variant='outline'>
              {t('{{count}} GrsAI dry-run', {
                count: parsedSummary.grsaiDryRun,
              })}
            </Badge>
          </div>
        ) : (
          <Alert variant='destructive'>
            <AlertTriangle />
            <AlertTitle>{t('Draft JSON is invalid')}</AlertTitle>
            <AlertDescription>
              {t('Fix JSON syntax before validation, dry-run, or save.')}
            </AlertDescription>
          </Alert>
        )}
      </SettingsCard>

      <SettingsCard
        title={t('Bindings JSON')}
        description={t(
          'Use backend validation for every change. Keep enabled=false until canary, fixture, and rollback gates are complete.'
        )}
      >
        <div className='space-y-3'>
          <Textarea
            className='min-h-[420px] font-mono text-xs'
            spellCheck={false}
            value={editorValue}
            disabled={isBusy}
            onChange={(event) => {
              setEditorValue(event.target.value)
              setValidatedState(null)
              setValidatedDraft(null)
              setDryRunResult(null)
              setDryRunDraft(null)
            }}
          />
          <div className='flex flex-wrap gap-2'>
            <Button type='button' variant='outline' onClick={handleFormat}>
              {t('Format JSON')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() => handleLoadTemplate(emptyConfig)}
            >
              {t('Load empty config')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() => handleLoadTemplate(mockTemplate)}
            >
              {t('Load mock template')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() => handleLoadTemplate(grsaiDryRunTemplate)}
            >
              {t('Load GrsAI dry-run template')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={handleValidate}
              disabled={isBusy}
            >
              {validateMutation.isPending ? t('Validating...') : t('Validate')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={handleDryRun}
              disabled={isBusy}
            >
              {dryRunMutation.isPending ? t('Dry running...') : t('Dry Run')}
            </Button>
            <Button
              type='button'
              onClick={handleSave}
              disabled={isBusy || !isSaveReady}
            >
              {updateMutation.isPending ? t('Saving...') : t('Save Bindings')}
            </Button>
          </div>
        </div>
      </SettingsCard>

      {validatedState && (
        <SettingsCard
          title={t('Validation result')}
          description={t(
            'The backend accepted and normalized this draft. Review the canonical JSON before saving if needed.'
          )}
        >
          <div className='space-y-3'>
            <div className='flex flex-wrap items-center gap-2'>
              <Badge variant='default'>
                <CheckCircle2 data-icon='inline-start' />
                {t('Valid')}
              </Badge>
              <Badge variant='secondary'>
                {t('{{count}} normalized bindings', {
                  count: validatedState.config.bindings.length,
                })}
              </Badge>
            </div>
            <PreviewBlock value={validatedState.config} />
          </div>
        </SettingsCard>
      )}

      {dryRunResult && (
        <SettingsCard
          title={t('Dry-run preview')}
          description={t(
            'Preview is redacted and diagnostic only. It must remain offline and must not contact upstream providers.'
          )}
        >
          <div className='space-y-3'>
            <div className='flex flex-wrap gap-2'>
              <Badge
                variant={dryRunResult.noProviderCall ? 'default' : 'destructive'}
              >
                {dryRunResult.noProviderCall
                  ? t('noProviderCall=true')
                  : t('Provider call risk')}
              </Badge>
              <Badge variant='secondary'>
                {t('{{count}} previews', {
                  count: dryRunResult.bindings.length,
                })}
              </Badge>
            </div>
            {dryRunResult.bindings.map((binding) => (
              <div key={binding.id} className='space-y-2 rounded-xl border p-3'>
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='font-medium'>{binding.id}</span>
                  <Badge variant='outline'>{binding.adapterPreset}</Badge>
                  <Badge variant='outline'>{binding.parameterTemplate}</Badge>
                  <Badge variant={binding.enabled ? 'default' : 'secondary'}>
                    {binding.enabled ? t('Enabled') : t('Disabled')}
                  </Badge>
                </div>
                <PreviewBlock value={binding.requestPreview} />
              </div>
            ))}
          </div>
        </SettingsCard>
      )}
    </SettingsSection>
  )
}
