import { apiClient } from './client'
import type { ApiKey } from '@/types'

export interface IntelligenceQuestion { id: string; title: string; prompt: string; status: string; answer?: string }
export interface IntelligenceOptions { kind: 'questions' | 'animation'; models: string[]; questions: IntelligenceQuestion[]; timeout_seconds: number }
export interface IntelligenceRun {
  id: string; model: string; kind: 'questions' | 'animation'; status: string
  created_at: string; deadline_at: string; finished_at: string | null
  result: { questions: IntelligenceQuestion[]; html?: string; filename?: string; error?: string; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_creation_tokens: number; actual_cost?: number }
}
export function supportsIntelligence(key: ApiKey): boolean {
  return key.group?.status === 'active' && (key.group.platform === 'anthropic' || (key.group.platform === 'openai' && [71, 101, 105].includes(key.group.id)))
}
export const intelligenceApi = {
  async options(key: number) { return (await apiClient.get<IntelligenceOptions>(`/keys/${key}/intelligence/options`)).data },
  async start(key: number, model: string) { return (await apiClient.post<IntelligenceRun>(`/keys/${key}/intelligence`, {model})).data },
  async get(key: number, run: string) { return (await apiClient.get<IntelligenceRun>(`/keys/${key}/intelligence/${run}`)).data },
  async history(key: number) { return (await apiClient.get<IntelligenceRun[]>(`/keys/${key}/intelligence`)).data }
}
export const intelligenceModelLabel = (model: string): string => ({
  'gpt-6-astra': '6 Astra', 'gpt-6.1-sol': '6.1 Sol', 'claude-opus-5-5': 'Opus 5.5',
  'claude-sonnet-5-5': 'Sonnet 5.5', 'claude-fable-5-1': 'Fable 5.1', 'claude-fable-5-2': 'Fable 5.2'
}[model] || model)
