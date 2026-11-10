import { describe, expect, it } from 'vitest'
import { buildIntelligencePreview } from '../intelligencePreview'

describe('intelligence animation isolation', () => {
  it('removes executable and navigable content while retaining SVG animation', () => {
    const html = buildIntelligencePreview('<html><head><meta http-equiv="refresh" content="0;url=https://evil.invalid"></head><body onload="alert(1)"><script>alert(1)</script><iframe src="https://evil.invalid"></iframe><a href="https://evil.invalid">go</a><svg><foreignObject><div>nested</div></foreignObject><image href="https://evil.invalid/x"/><circle r="5"><animate attributeName="r" values="5;8;5" dur="1s" repeatCount="indefinite"/></circle></svg></body></html>')
    const doc = new DOMParser().parseFromString(html, 'text/html')
    expect(doc.querySelector('script,iframe,a,foreignObject,image,[onload],[href],[src]')).toBeNull()
    expect(doc.querySelector('animate')).not.toBeNull()
    expect(doc.querySelector('meta[http-equiv="refresh"]')).toBeNull()
    const csp = doc.querySelector('meta[http-equiv="Content-Security-Policy"]')!.getAttribute('content')!
    expect(csp).toContain("script-src 'none'")
    expect(csp).toContain("connect-src 'none'")
    expect(csp).toContain("default-src 'none'")
  })
  it('retains local CSS animation without trusting the model CSP', () => {
    const html = buildIntelligencePreview('<html><head><meta http-equiv="Content-Security-Policy" content="default-src *"><style>@keyframes ride {to{transform:rotate(360deg)}}.wheel{animation:ride 1s linear infinite}</style></head><body><svg class="wheel"><circle r="3"/></svg></body></html>')
    expect(html).toContain('@keyframes ride')
    expect(html).not.toContain('default-src *')
  })
})
