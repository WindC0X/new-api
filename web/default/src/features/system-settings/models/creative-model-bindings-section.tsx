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
  DatabaseZap,
  FlaskConical,
  PlusCircle,
  ShieldCheck,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  NativeSelect,
  NativeSelectOption,
} from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'
import {
  dryRunCreativeModelBindings,
  getCreativeAdapterManifests,
  getCreativeChannelSummaries,
  getCreativeModelBindings,
  updateCreativeModelBindings,
  validateCreativeModelBindings,
} from '../api'
import { SettingsCard } from '../components/settings-card'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import type {
  CreativeAdapterManifest,
  CreativeParameterTemplate,
  CreativeChannelSummary,
  CreativeModelBindingConfig,
  CreativeModelBindingsConfig,
  CreativeModelBindingsDryRunResult,
  CreativeModelBindingsState,
} from '../types'

const queryKey = ['creative-model-bindings']
const channelsQueryKey = ['creative-model-bindings', 'channels']
const manifestsQueryKey = ['creative-model-bindings', 'adapter-manifests']

const emptyConfig: CreativeModelBindingsConfig = {
  version: 1,
  bindings: [],
}

function safeModelSlug(modelId: string): string {
  const slug = modelId
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
  return slug && /^[a-z]/.test(slug) ? slug : `model-${slug || 'unknown'}`
}

function generatedBindingId(
  manifest: CreativeAdapterManifest,
  providerModelId: string,
  channelId: number
): string {
  const prefix = manifest.bindingIdPrefix || manifest.providerFamily || 'creative'
  const suffix = manifest.bindingIdSuffix || manifest.transportMode || 'binding'
  const channelSegment = manifest.requiresChannel ? `:ch${channelId}` : ''
  return `${prefix}:${safeModelSlug(providerModelId)}${channelSegment}:${suffix}`
}

function templateById(
  templates: CreativeParameterTemplate[],
  templateId: string
): CreativeParameterTemplate | null {
  return templates.find((template) => template.id === templateId) ?? null
}

function defaultTemplateForManifest(
  manifest: CreativeAdapterManifest | null,
  templates: CreativeParameterTemplate[]
): CreativeParameterTemplate | null {
  if (!manifest) return null
  const allowedTemplates = manifest.allowedTemplates
    .map((templateId) => templateById(templates, templateId))
    .filter((template): template is CreativeParameterTemplate =>
      Boolean(template)
    )
  return (
    allowedTemplates.find(
      (template) => template.id === manifest.defaultTemplate
    ) ??
    allowedTemplates[0] ??
    null
  )
}

function configTemplateFromManifest(
  manifest: CreativeAdapterManifest,
  template: CreativeParameterTemplate,
  providerModelId = 'gpt-image-2',
  channelId = 1
): CreativeModelBindingsConfig {
  const bindingId = generatedBindingId(manifest, providerModelId, channelId)
  return {
    version: 1,
    bindings: [
      {
        id: bindingId,
        providerModelId,
        priceModelId: providerModelId,
        displayName: `${manifest.label} · ${providerModelId}`,
        modality: manifest.modality || template.modality || 'image',
        enabled: false,
        canaryGroups: ['default'],
        channelId: manifest.requiresChannel ? channelId : undefined,
        adapterPreset: manifest.id,
        parameterTemplate: template.id,
        recommendedScore: 0,
        sortOrder: 1000,
        parameterSchema: template.schema,
      },
    ],
  }
}

function parseChannelModels(models: string[] | null | undefined): string[] {
  const list = Array.isArray(models) ? models : []
  return Array.from(
    new Set(
      list
        .map((model) => model.trim())
        .filter(Boolean)
    )
  )
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

function upsertBinding(
  config: CreativeModelBindingsConfig,
  binding: CreativeModelBindingConfig
): CreativeModelBindingsConfig {
  const bindings = Array.isArray(config.bindings) ? config.bindings : []
  const next = bindings.filter((item) => item.id !== binding.id)
  next.push(binding)
  return {
    version: config.version || 1,
    bindings: next,
  }
}

function bindingExists(config: CreativeModelBindingsConfig, id: string): boolean {
  const bindings = Array.isArray(config.bindings) ? config.bindings : []
  return bindings.some((item) => item.id === id)
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
  const [draftChannelSearch, setDraftChannelSearch] = useState('')
  const [draftChannelId, setDraftChannelId] = useState('')
  const manualChannelId = useMemo(() => {
    const numeric = Number(draftChannelId)
    return Number.isInteger(numeric) && numeric > 0 ? numeric : null
  }, [draftChannelId])
  const channelsQuery = useQuery({
    queryKey: [...channelsQueryKey, draftChannelSearch],
    queryFn: () =>
      getCreativeChannelSummaries({
        p: 1,
        page_size: 50,
        keyword: draftChannelSearch.trim() || undefined,
      }),
  })
  const manifestsQuery = useQuery({
    queryKey: manifestsQueryKey,
    queryFn: getCreativeAdapterManifests,
  })
  const adapterManifests = useMemo(
    () => manifestsQuery.data?.data?.manifests ?? [],
    [manifestsQuery.data]
  )
  const parameterTemplates = useMemo(
    () => manifestsQuery.data?.data?.parameterTemplates ?? [],
    [manifestsQuery.data]
  )
  const channels = useMemo(
    () => channelsQuery.data?.data?.items ?? [],
    [channelsQuery.data]
  )
  const selectedListedChannel = useMemo(
    () =>
      channels.find((channel) => String(channel.id) === draftChannelId) ?? null,
    [channels, draftChannelId]
  )
  const selectedChannelLookupQuery = useQuery({
    queryKey: [...channelsQueryKey, 'lookup', manualChannelId],
    queryFn: () =>
      getCreativeChannelSummaries({
        channel_id: manualChannelId ?? undefined,
        page_size: 1,
      }),
    enabled: manualChannelId !== null && !selectedListedChannel,
  })
  const selectedChannel = useMemo(
    () =>
      selectedListedChannel ??
      selectedChannelLookupQuery.data?.data?.items?.[0] ??
      null,
    [selectedListedChannel, selectedChannelLookupQuery.data]
  )
  const selectedChannelModels = useMemo(
    () => parseChannelModels(selectedChannel?.models),
    [selectedChannel]
  )
  const [draftProviderModelId, setDraftProviderModelId] = useState('')
  const [draftBindingId, setDraftBindingId] = useState('')
  const [draftDisplayName, setDraftDisplayName] = useState('')
  const [draftPriceModelId, setDraftPriceModelId] = useState('')
  const [draftCanaryGroups, setDraftCanaryGroups] = useState('test')
  const [draftAdapterPreset, setDraftAdapterPreset] =
    useState('mock_image_task')
  const [draftParameterTemplate, setDraftParameterTemplate] =
    useState('mock_gpt_image')
  const firstAvailableAdapterManifest = useMemo(
    () =>
      adapterManifests.find((manifest) => manifest.status === 'available') ??
      null,
    [adapterManifests]
  )
  const selectedAdapterManifest = useMemo(
    () =>
      adapterManifests.find((manifest) => manifest.id === draftAdapterPreset) ??
      firstAvailableAdapterManifest,
    [adapterManifests, draftAdapterPreset, firstAvailableAdapterManifest]
  )
  const allowedTemplatesForSelectedManifest = useMemo(
    () =>
      selectedAdapterManifest
        ? selectedAdapterManifest.allowedTemplates
            .map((templateId) => templateById(parameterTemplates, templateId))
            .filter((template): template is CreativeParameterTemplate =>
              Boolean(template)
            )
        : [],
    [parameterTemplates, selectedAdapterManifest]
  )
  const selectedParameterTemplate = useMemo(
    () =>
      allowedTemplatesForSelectedManifest.find(
        (template) => template.id === draftParameterTemplate
      ) ??
      defaultTemplateForManifest(selectedAdapterManifest, parameterTemplates),
    [
      allowedTemplatesForSelectedManifest,
      draftParameterTemplate,
      parameterTemplates,
      selectedAdapterManifest,
    ]
  )
  const manifestsReady =
    !manifestsQuery.isLoading &&
    !manifestsQuery.isError &&
    adapterManifests.length > 0 &&
    parameterTemplates.length > 0
  const selectedAdapterCanDraft =
    selectedAdapterManifest?.status === 'available' &&
    allowedTemplatesForSelectedManifest.length > 0
  const formatChannelLabel = (channel: CreativeChannelSummary) => {
    const status =
      channel.status === 1
        ? t('Enabled')
        : t('Channel status {{status}}', { status: channel.status })
    return `#${channel.id} · ${channel.name || t('Unnamed channel')} · ${channel.group || t('Default group')} · ${status}`
  }

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

  const resetDraftGates = () => {
    setValidatedState(null)
    setValidatedDraft(null)
    setDryRunResult(null)
    setDryRunDraft(null)
  }

  const handleUpsertDraftBinding = () => {
    const channelId = Number(draftChannelId)
    const providerModelId = draftProviderModelId.trim()
    if (!manifestsReady || !selectedAdapterManifest || !selectedParameterTemplate) {
      toast.error(t('Adapter manifests are unavailable. Reload and try again.'))
      return
    }
    if (!selectedAdapterCanDraft) {
      toast.error(t('This adapter cannot create binding drafts yet.'))
      return
    }
    const bindingId =
      draftBindingId.trim() ||
      generatedBindingId(selectedAdapterManifest, providerModelId, channelId)
    const priceModelId = draftPriceModelId.trim() || providerModelId
    if (
      selectedAdapterManifest.requiresChannel &&
      (!Number.isInteger(channelId) || channelId <= 0)
    ) {
      toast.error(t('Select a channel before adding a binding'))
      return
    }
    if (!providerModelId) {
      toast.error(t('Select a provider model from the channel models list'))
      return
    }
    if (selectedChannel?.status !== undefined && selectedChannel.status !== 1) {
      toast.error(
        t('Selected channel is disabled. Enable it before adding a binding draft.')
      )
      return
    }
    if (selectedChannel && selectedChannelModels.length === 0) {
      toast.error(
        t('This channel has no models configured. Add models in the channel first.')
      )
      return
    }
    if (
      selectedChannel &&
      selectedChannelModels.length > 0 &&
      !selectedChannelModels.includes(providerModelId)
    ) {
      toast.warning(
        t(
          'Provider model is not directly listed by the selected channel. The draft is validation-gated so backend model mapping can still accept it.'
        )
      )
    }
    const canaryGroups = draftCanaryGroups
      .split(',')
      .map((group) => group.trim())
      .filter(Boolean)
    const binding: CreativeModelBindingConfig = {
      id: bindingId,
      providerModelId,
      priceModelId,
      displayName: draftDisplayName.trim() || providerModelId,
      modality: selectedAdapterManifest.modality || selectedParameterTemplate.modality,
      enabled: false,
      canaryGroups,
      channelId: Number.isInteger(channelId) && channelId > 0 ? channelId : undefined,
      adapterPreset: selectedAdapterManifest.id,
      parameterTemplate: selectedParameterTemplate.id,
      recommendedScore: 0,
      sortOrder: 1000,
      parameterSchema: selectedParameterTemplate.schema,
    }
    try {
      const currentConfig = parseEditorValue(editorValue)
      if (
        bindingExists(currentConfig, binding.id) &&
        !window.confirm(
          t(
            'A binding with this ID already exists. Replace that existing draft?'
          )
        )
      ) {
        return
      }
      const config = upsertBinding(currentConfig, binding)
      setEditorValue(formatConfig(config))
      resetDraftGates()
      toast.success(t('Binding draft added to JSON editor'))
    } catch {
      toast.error(t('Fix JSON syntax before using the binding builder'))
    }
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
        <AlertTitle>
          {t('Creative model bindings map channels to Creative models')}
        </AlertTitle>
        <AlertDescription>
          {t(
            'Provider keys, Base URLs, and upstream credentials are configured in Channels. This page only binds a Creative-visible model ID to a channel, provider model, adapter preset, and safe parameter schema.'
          )}
        </AlertDescription>
      </Alert>

      <Alert>
        <FlaskConical />
        <AlertTitle>{t('Manifest-driven adapter safety')}</AlertTitle>
        <AlertDescription>
          {t(
            'Adapter manifests come from the backend. Duomi and GrsAI live adapters are channel-backed and can be enabled only after backend validation passes for a locked channel, provider model, canary group, and safe parameter schema. Validate and dry-run are nonce-protected and must report noProviderCall=true before save.'
          )}
        </AlertDescription>
      </Alert>

      {!manifestsQuery.isLoading && !manifestsReady && (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Failed to load adapter manifests')}</AlertTitle>
          <AlertDescription>
            {t(
              'Binding builder is disabled because adapter capabilities could not be loaded from the backend. Existing JSON can still be reviewed, but new presets must not be guessed.'
            )}
          </AlertDescription>
        </Alert>
      )}

      <SettingsCard
        title={t('How to configure image provider channels')}
        description={t(
          'Use this flow to prepare Duomi, GrsAI, and future image provider bindings without exposing provider credentials to OpenTU users.'
        )}
      >
        <div className='grid gap-3 text-sm md:grid-cols-3'>
          <div className='rounded-xl border p-3'>
            <div className='mb-1 flex items-center gap-2 font-medium'>
              <DatabaseZap className='size-4' aria-hidden='true' />
              {t('1. Configure channel')}
            </div>
            <p className='text-muted-foreground'>
              {t(
                'Create or edit a new-api channel with provider Base URL, API key, group, and channel model list. Channel secrets stay in the channel subsystem.'
              )}
            </p>
          </div>
          <div className='rounded-xl border p-3'>
            <div className='mb-1 font-medium'>
              {t('2. Bind Creative model')}
            </div>
            <p className='text-muted-foreground'>
              {t(
                'Choose a sanitized channel summary below, create a binding ID, and attach an adapter preset plus parameter schema.'
              )}
            </p>
          </div>
          <div className='rounded-xl border p-3'>
            <div className='mb-1 font-medium'>{t('3. Validate and save')}</div>
            <p className='text-muted-foreground'>
              {t(
                'Run backend validation and offline dry-run for the exact JSON draft before saving. Dry-run previews request mapping only; real provider calls happen only from user task submit or an explicitly authorized live smoke.'
              )}
            </p>
          </div>
        </div>
      </SettingsCard>

      <SettingsCard
        title={t('Guided binding builder')}
        description={t(
          'This helper uses a sanitized channel summary endpoint and writes a validation-gated draft into the JSON editor. It does not save until validation and offline dry-run pass.'
        )}
      >
        <div className='grid gap-4 lg:grid-cols-2'>
          <div className='space-y-2'>
            <Label htmlFor='creative-binding-channel-search'>
              {t('Search channel summaries')}
            </Label>
            <Input
              id='creative-binding-channel-search'
              value={draftChannelSearch}
              disabled={isBusy}
              onChange={(event) => setDraftChannelSearch(event.target.value)}
              placeholder={t('Search by channel name, ID, or model')}
            />
            <Label htmlFor='creative-binding-channel'>{t('Channel')}</Label>
            <NativeSelect
              id='creative-binding-channel'
              className='w-full'
              value={draftChannelId}
              disabled={channelsQuery.isLoading || isBusy}
              onChange={(event) => {
                setDraftChannelId(event.target.value)
                setDraftProviderModelId('')
              }}
            >
              <NativeSelectOption value=''>
                {channelsQuery.isLoading
                  ? t('Loading channels...')
                  : t('Select channel')}
              </NativeSelectOption>
              {channels.map((channel) => (
                <NativeSelectOption key={channel.id} value={channel.id}>
                  {formatChannelLabel(channel)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              value={draftChannelId}
              disabled={isBusy}
              onChange={(event) => setDraftChannelId(event.target.value)}
              placeholder={t('Or enter channel ID manually')}
            />
            {channelsQuery.isError && (
              <p className='text-destructive text-xs'>
                {t('Failed to load channels; you can still edit JSON manually.')}
              </p>
            )}
            {manualChannelId &&
              !selectedChannel &&
              !selectedChannelLookupQuery.isFetching && (
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Manual channel ID is not loaded in summaries. The draft is validation-gated; backend validation will reject missing, disabled, or unsupported channels.'
                  )}
                </p>
              )}
            {selectedChannel && selectedChannel.status !== 1 && (
              <p className='text-destructive text-xs'>
                {t(
                  'Selected channel is disabled. Enable it before adding a binding draft.'
                )}
              </p>
            )}
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-provider-model'>
              {t('Provider model from channel')}
            </Label>
            <NativeSelect
              id='creative-binding-provider-model'
              className='w-full'
              value={draftProviderModelId}
              disabled={
                !selectedChannel || selectedChannelModels.length === 0 || isBusy
              }
              onChange={(event) => {
                setDraftProviderModelId(event.target.value)
                setDraftBindingId('')
                setDraftDisplayName('')
                setDraftPriceModelId('')
              }}
            >
              <NativeSelectOption value=''>
                {selectedChannel
                  ? t('Select provider model')
                  : t('Select channel first')}
              </NativeSelectOption>
              {selectedChannelModels.map((modelId) => (
                <NativeSelectOption key={modelId} value={modelId}>
                  {modelId}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              value={draftProviderModelId}
              disabled={isBusy}
              onChange={(event) => {
                setDraftProviderModelId(event.target.value)
                setDraftBindingId('')
                setDraftDisplayName('')
                setDraftPriceModelId('')
              }}
              placeholder={t('Or enter provider model manually')}
            />
            {selectedChannel && selectedChannelModels.length === 0 && (
              <p className='text-destructive text-xs'>
                {t(
                  'This channel has no models configured. Add models in the channel first.'
                )}
              </p>
            )}
            {selectedChannel &&
              selectedChannelModels.length > 0 &&
              draftProviderModelId.trim() !== '' &&
              !selectedChannelModels.includes(draftProviderModelId.trim()) && (
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Provider model is not directly listed by this channel. Continue only if channel model mapping resolves to this provider model; backend validation and dry-run remain authoritative.'
                  )}
                </p>
              )}
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-adapter'>
              {t('Adapter')}
            </Label>
            <NativeSelect
              id='creative-binding-adapter'
              className='w-full'
              value={selectedAdapterManifest?.id ?? draftAdapterPreset}
              disabled={isBusy || !manifestsReady}
              onChange={(event) => {
                const nextPreset = event.target.value
                setDraftAdapterPreset(nextPreset)
                const nextManifest =
                  adapterManifests.find(
                    (manifest) => manifest.id === nextPreset
                  ) ?? null
                setDraftParameterTemplate(nextManifest?.defaultTemplate ?? '')
                setDraftBindingId('')
              }}
            >
              {adapterManifests.map((manifest) => (
                <NativeSelectOption
                  key={manifest.id}
                  value={manifest.id}
                  disabled={manifest.status !== 'available'}
                >
                  {manifest.label}
                  {manifest.status !== 'available'
                    ? ` · ${t('Unavailable')}`
                    : ''}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className='text-muted-foreground text-xs'>
              {selectedAdapterManifest
                ? selectedAdapterManifest.description
                : t('Load adapter manifests before creating a binding draft.')}
            </p>
            {selectedAdapterManifest && (
              <div className='flex flex-wrap gap-2'>
                <Badge variant='outline'>
                  {selectedAdapterManifest.transportMode}
                </Badge>
                <Badge
                  variant={
                    selectedAdapterManifest.canBeEnabled
                      ? 'default'
                      : 'secondary'
                  }
                >
                  {selectedAdapterManifest.canBeEnabled
                    ? t('Can be enabled')
                    : t('Disabled by policy')}
                </Badge>
              </div>
            )}
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-parameter-template'>
              {t('Parameter template')}
            </Label>
            <NativeSelect
              id='creative-binding-parameter-template'
              className='w-full'
              value={selectedParameterTemplate?.id ?? draftParameterTemplate}
              disabled={
                isBusy ||
                !selectedAdapterManifest ||
                allowedTemplatesForSelectedManifest.length === 0
              }
              onChange={(event) => {
                setDraftParameterTemplate(event.target.value)
              }}
            >
              {allowedTemplatesForSelectedManifest.map((template) => (
                <NativeSelectOption key={template.id} value={template.id}>
                  {template.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className='text-muted-foreground text-xs'>
              {selectedParameterTemplate
                ? selectedParameterTemplate.description
                : t('The backend template supplies model-specific controls such as 尺寸、分辨率、质量、比例.')}
            </p>
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-id'>{t('Binding ID')}</Label>
            <Input
              id='creative-binding-id'
              value={draftBindingId}
              disabled={isBusy}
              onChange={(event) => setDraftBindingId(event.target.value)}
              placeholder='mock-image:gpt-image-2:preview'
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'OpenTU submits this logical ID as model. Leave blank to generate an ID from the selected backend manifest prefix, channel, model, and suffix.'
              )}
            </p>
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-display'>
              {t('Display name')}
            </Label>
            <Input
              id='creative-binding-display'
              value={draftDisplayName}
              disabled={isBusy}
              onChange={(event) => setDraftDisplayName(event.target.value)}
              placeholder='GPT Image 2 · Preview'
            />
          </div>

          <div className='space-y-2'>
            <Label htmlFor='creative-binding-price-model'>
              {t('Billing model ID')}
            </Label>
            <Input
              id='creative-binding-price-model'
              value={draftPriceModelId}
              disabled={isBusy}
              onChange={(event) => setDraftPriceModelId(event.target.value)}
              placeholder='gpt-image-2'
            />
            <p className='text-muted-foreground text-xs'>
              {t('Pricing uses this ID; channel routing still uses provider model.')}
            </p>
          </div>

          <div className='space-y-2 lg:col-span-2'>
            <Label htmlFor='creative-binding-canary'>
              {t('Canary groups')}
            </Label>
            <Input
              id='creative-binding-canary'
              value={draftCanaryGroups}
              disabled={isBusy}
              onChange={(event) => setDraftCanaryGroups(event.target.value)}
              placeholder='test,vip'
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Generated drafts always use enabled=false. Expose to users only after canary and rollback gates are complete.'
              )}
            </p>
          </div>
        </div>
        <div className='mt-4 flex flex-wrap gap-2'>
          <Button
            type='button'
            variant='outline'
            onClick={handleUpsertDraftBinding}
            disabled={isBusy || !manifestsReady || !selectedAdapterCanDraft}
          >
            <PlusCircle data-icon='inline-start' />
            {t('Add or replace binding draft')}
          </Button>
        </div>
      </SettingsCard>

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
          'Use backend validation for every change. Keep enabled=false until canary, fixture, and rollback gates are complete; dry-run must stay offline.'
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
            {adapterManifests
              .filter((manifest) => manifest.status === 'available')
              .map((manifest) => {
                const template = defaultTemplateForManifest(
                  manifest,
                  parameterTemplates
                )
                return (
                  <Button
                    key={manifest.id}
                    type='button'
                    variant='outline'
                    onClick={() => {
                      if (!template) {
                        toast.error(t('Adapter template is unavailable'))
                        return
                      }
                      handleLoadTemplate(
                        configTemplateFromManifest(manifest, template)
                      )
                    }}
                    disabled={!manifestsReady || !template}
                  >
                    {t('Load {{name}} template', { name: manifest.label })}
                  </Button>
                )
              })}
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
                  ? t('noProviderCall=true (offline dry-run only)')
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
