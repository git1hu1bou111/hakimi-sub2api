import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import IntelligenceTestModal from '../IntelligenceTestModal.vue'
import { intelligenceApi, type IntelligenceRun } from '@/api/intelligence'
import type { ApiKey } from '@/types'

vi.mock('@/api/intelligence', async (original) => ({...await original<typeof import('@/api/intelligence')>(), intelligenceApi:{options:vi.fn(),history:vi.fn(),get:vi.fn(),start:vi.fn()}}))
const key = {id:12,group:{id:47,name:'Claude',platform:'anthropic',status:'active',rate_multiplier:0.25}} as ApiKey
afterEach(()=>{vi.clearAllMocks();vi.useRealTimers()})

describe('intelligence test modal',()=>{
 it('resumes a job, isolates executable previews, hides usage details, and stops playback on close',async()=>{
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
  const frame=wrapper.find('iframe');expect(frame.attributes('sandbox')).toBe('allow-scripts');expect(frame.attributes('srcdoc')).toContain('<script>evil()</script>')
  expect(wrapper.text()).toContain('请自行观看判断')
  expect(wrapper.text()).not.toMatch(/本轮输入|已入账费用|缓存读取|缓存写入|安全预览未执行脚本/)
  const buttons=()=>wrapper.findAll('button')
  await buttons().find(b=>b.text()==='停止播放')!.trigger('click')
  expect(wrapper.find('iframe').exists()).toBe(false)
  await buttons().find(b=>b.text()==='播放动画')!.trigger('click')
  expect(wrapper.find('iframe').exists()).toBe(true)
  const original=wrapper.find('iframe').element
  await buttons().find(b=>b.text()==='重新播放')!.trigger('click')
  expect(wrapper.find('iframe').element).not.toBe(original)
  expect(intelligenceApi.start).not.toHaveBeenCalled()
  await wrapper.setProps({show:false});await flushPromises()
  expect(wrapper.find('iframe').exists()).toBe(false)
  wrapper.unmount();const count=vi.mocked(intelligenceApi.get).mock.calls.length
  await vi.advanceTimersByTimeAsync(10000);expect(vi.mocked(intelligenceApi.get).mock.calls.length).toBe(count)
 })
})
