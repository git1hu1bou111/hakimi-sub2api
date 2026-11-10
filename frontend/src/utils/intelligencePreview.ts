import DOMPurify from 'dompurify'

// Never mount a model answer in the parent DOM. This also removes scripts,
// event handlers, hyperlinks, refresh directives and embedded documents.
export function buildIntelligencePreview(html: string): string {
  const clean = DOMPurify.sanitize(html, {
    WHOLE_DOCUMENT: true,
    USE_PROFILES: { html: true, svg: true, svgFilters: true },
    ADD_TAGS: ['style', 'animate', 'animateTransform', 'animateMotion', 'mpath'],
    FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'input', 'button', 'textarea', 'select', 'meta', 'base', 'link', 'a', 'foreignObject', 'image', 'img', 'audio', 'video', 'source', 'use'],
    FORBID_ATTR: ['href', 'xlink:href', 'src', 'srcset', 'action', 'formaction', 'target'],
    ALLOW_DATA_ATTR: false,
  })
  const doc = new DOMParser().parseFromString(clean, 'text/html')
  const styles = Array.from(doc.head.querySelectorAll('style')).map(style => style.outerHTML).join('')
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body{margin:0;min-height:100%;overflow:auto}svg{max-width:100%}</style>${styles}</head><body>${doc.body.innerHTML}</body></html>`
}

export function intelligenceDownload(html: string, filename: string, createdAt: string): void {
  // The download keeps the original model HTML; the preview remains sanitized.
  const stamp = new Date(createdAt).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
  const blob = new Blob([`<!-- 哈机密智商测试答卷；北京时间 ${stamp} -->\n`, html], { type: 'text/html;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = /^pelican-[\w-]+\.html$/.test(filename) ? filename : 'pelican.html'
  link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
