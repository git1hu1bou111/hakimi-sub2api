import { describe, expect, it } from 'vitest'
import { buildIntelligencePreview } from '../intelligencePreview'

describe('intelligence animation isolation', () => {
  it('retains inline animation scripts while removing navigable and embedded content', () => {
    const html = buildIntelligencePreview('<html><head><meta http-equiv="refresh" content="0;url=https://evil.invalid"></head><body onload="alert(1)"><script>alert(1)</script><iframe src="https://evil.invalid"></iframe><a href="https://evil.invalid">go</a><svg><foreignObject><div>nested</div></foreignObject><image href="https://evil.invalid/x"/><circle r="5"><animate attributeName="r" values="5;8;5" dur="1s" repeatCount="indefinite"/></circle></svg></body></html>')
    const doc = new DOMParser().parseFromString(html, 'text/html')
    expect(doc.querySelector('iframe,a,foreignObject,image,[onload],[href],[src]')).toBeNull()
    expect(doc.querySelector('script')?.textContent).toBe('alert(1)')
    expect(doc.querySelector('animate')).not.toBeNull()
    expect(doc.querySelector('meta[http-equiv="refresh"]')).toBeNull()
    const csp = doc.querySelector('meta[http-equiv="Content-Security-Policy"]')!.getAttribute('content')!
    expect(csp).toContain("script-src 'unsafe-inline'")
    expect(csp).toContain("script-src-attr 'none'")
    expect(csp).toContain("connect-src 'none'")
    expect(csp).toContain("default-src 'none'")
    expect(csp).toContain("worker-src 'none'")
    expect(csp).toContain("form-action 'none'")
    expect(csp).not.toMatch(/nonce-|unsafe-eval|https:/)
  })
  it('retains local CSS animation without trusting the model CSP', () => {
    const html = buildIntelligencePreview('<html><head><meta http-equiv="Content-Security-Policy" content="default-src *"><style>@keyframes ride {to{transform:rotate(360deg)}}.wheel{animation:ride 1s linear infinite}</style></head><body><svg class="wheel"><circle r="3"/></svg></body></html>')
    expect(html).toContain('@keyframes ride')
    expect(html).not.toContain('default-src *')
  })
  it('preserves SVG IDs used by the generated animation script', () => {
    const html = buildIntelligencePreview('<svg><g id="head"><circle r="4"/></g></svg><script>document.getElementById("head").setAttribute("transform","rotate(2)")</script>')
    const doc = new DOMParser().parseFromString(html, 'text/html')
    expect(doc.querySelector('svg #head')).not.toBeNull()
    expect(doc.querySelector('script')?.textContent).toContain('getElementById("head")')
  })
  it('keeps head and body script order with policy first, stripping external loaders and model nonces', () => {
    const html = buildIntelligencePreview('<html><head><script nonce="model">const wheelSpeed=2</script><style>svg{width:100%}</style></head><body><svg id="bike"/><script src="https://evil.invalid/a.js"></script><script type="importmap">{"imports":{"x":"https://evil.invalid/x"}}</script><script>requestAnimationFrame(()=>document.querySelector("svg").setAttribute("data-frame","1"))</script></body></html>')
    const doc = new DOMParser().parseFromString(html, 'text/html')
    expect(doc.querySelector('[src],[nonce],script[type="importmap"]')).toBeNull()
    expect(html.indexOf('Content-Security-Policy')).toBeLessThan(html.indexOf('<script'))
    expect(html.indexOf('const wheelSpeed')).toBeLessThan(html.indexOf('requestAnimationFrame'))
    expect(html).toContain('requestAnimationFrame')
    expect(html).not.toContain('evil.invalid')
  })
})
