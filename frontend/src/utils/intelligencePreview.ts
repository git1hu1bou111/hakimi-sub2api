import DOMPurify from 'dompurify'

// This document is executable, untrusted model output. Only use it inside an
// iframe with sandbox="allow-scripts" (NEVER allow-same-origin or parent v-html).
// Inline scripts animate the SVG; the frame's opaque origin protects the panel.
// Never authorize a nonce in the frame's own CSP: it must reject external scripts,
// even when they copy the page nonce required by the inherited parent policy.
export function buildIntelligencePreview(html: string): string {
  const clean = DOMPurify.sanitize(html, {
    WHOLE_DOCUMENT: true,
    USE_PROFILES: { html: true, svg: true, svgFilters: true },
    ADD_TAGS: ['script', 'style', 'animate', 'animateTransform', 'animateMotion', 'mpath'],
    FORBID_TAGS: ['iframe', 'object', 'embed', 'form', 'input', 'button', 'textarea', 'select', 'meta', 'base', 'link', 'a', 'foreignObject', 'image', 'img', 'audio', 'video', 'source', 'use'],
    FORBID_ATTR: ['href', 'xlink:href', 'src', 'srcset', 'action', 'formaction', 'target'],
    ALLOW_DATA_ATTR: false,
    // Generated code uses IDs such as "head". Preserve these inside the opaque
    // frame; DOM clobbering here cannot affect the parent document.
    SANITIZE_DOM: false,
  })
  const doc = new DOMParser().parseFromString(clean, 'text/html')
  // srcdoc inherits the panel's nonce-only script policy. Inline scripts need the
  // current document's nonce too; the additional frame policy still permits ONLY
  // inline scripts, so copying this nonce cannot authorize external loaders.
  const panelNonce = document.querySelector<HTMLScriptElement>('script[nonce]')?.nonce
  // Only classic/module inline JavaScript is needed. Ignore import maps and other
  // script types that could alter resource loading, and discard model nonces.
  for (const script of doc.querySelectorAll('script')) {
    if (!['', 'text/javascript', 'application/javascript', 'module'].includes((script.getAttribute('type') || '').toLowerCase())) script.remove()
    script.removeAttribute('nonce')
    // setAttribute is required: the nonce IDL setter alone is not serialized.
    if (panelNonce) script.setAttribute('nonce', panelNonce)
  }
  const head = Array.from(doc.head.querySelectorAll('style,script')).map(node => node.outerHTML).join('')
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; script-src-attr 'none'; style-src 'unsafe-inline'; img-src 'none'; connect-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><meta name="viewport" content="width=device-width,initial-scale=1">${head}<style>html{width:100%;height:100%;overflow:auto}body{box-sizing:border-box;width:100%;min-height:100%;margin:0}svg{max-width:100%}body>svg:only-of-type{display:block;width:100%;height:auto;max-height:100vh}</style></head><body>${doc.body.innerHTML}</body></html>`
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
