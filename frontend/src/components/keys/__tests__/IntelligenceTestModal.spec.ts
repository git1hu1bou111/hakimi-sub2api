import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import IntelligenceTestModal from '../IntelligenceTestModal.vue'
import { intelligenceApi, type IntelligenceRun } from '@/api/intelligence'
import type { ApiKey } from '@/types'

vi.mock('@/api/intelligence', async (original) => ({...await original<typeof import('@/api/intelligence')>(), intelligenceApi:{options:vi.fn(),history:vi.fn(),get:vi.fn(),start:vi.fn()}}))
const key = {id:12,group:{id:47,name:'Claude',platform:'anthropic',status:'active',rate_multiplier:0.25}} as ApiKey
afterEach(()=>{vi.clearAllMocks();vi.useRealTimers()})

describe('intelligence test modal',()=>{
 it('defaults to Opus, resumes a running job, and previews only inside a scriptless frame',async()=>{
  vi.useFakeTimers()
  vi.mocked(intelligenceApi.options).mockResolvedValue({kind:'animation',models:['claude-opus-5-5','claude-sonnet-5-5'],questions:[],timeout_seconds:300})
  const job={id:'job',model:'claude-opus-5-5',kind:'animation',status:'running',created_at:new Date().toISOString(),deadline_at:new Date(Date.now()+300000).toISOString(),finished_at:null,result:{questions:[],input_tokens:0,output_tokens:0,cache_read_tokens:0,cache_creation_tokens:0}} as IntelligenceRun
  vi.mocked(intelligenceApi.history).mockResolvedValue([job]);vi.mocked(intelligenceApi.get).mockResolvedValue(job)
  const wrapper=mount(IntelligenceTestModal,{props:{show:true,apiKey:key},global:{stubs:{BaseDialog:{template:'<div><slot/></div>'}}}})
  await flushPromises()
  expect(wrapper.find('button[aria-pressed="true"]').text()).toBe('Opus 5.5')
  expect(intelligenceApi.start).not.toHaveBeenCalled()
  expect(wrapper.text()).not.toMatch(/第二次|重试|第.*次尝试/)
  vi.mocked(intelligenceApi.get).mockResolvedValue({...job,status:'completed',result:{...job.result,html:'<html><body><script>evil()</script><svg></svg></body></html>',filename:'pelican-11-11-0420-test.html'}})
  await vi.advanceTimersByTimeAsync(2500);await flushPromises()
  const frame=wrapper.find('iframe');expect(frame.attributes('sandbox')).toBe('');expect(frame.attributes('srcdoc')).not.toContain('<script>')
  expect(wrapper.text()).toContain('请自行观看判断')
  expect(wrapper.text()).toContain('请下载 HTML 查看完整动画后再判断效果')
  wrapper.unmount();const count=vi.mocked(intelligenceApi.get).mock.calls.length
  await vi.advanceTimersByTimeAsync(10000);expect(vi.mocked(intelligenceApi.get).mock.calls.length).toBe(count)
 })
})
