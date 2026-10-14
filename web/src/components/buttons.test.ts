import { describe, it, expect } from 'vitest'
import { slateFill } from './buttons'

// #3241: the slate buttons took the page's text colour, dark slate in the
// light theme, which put dark text on a dark fill (1.7:1). The shared fill
// must carry its own light text, at AA on both its rest and hover shades.
describe('slateFill', () => {
  const lum = (hex: string) => {
    const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
      .map(c => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
    return 0.2126 * r + 0.7152 * g + 0.0722 * b
  }
  const ratio = (a: string, b: string) => {
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p)
    return (x + 0.05) / (y + 0.05)
  }

  it('sets its own text colour', () => {
    expect(slateFill.split(' ')).toContain('text-white')
  })

  it('keeps white text at AA on the rest and hover fills', () => {
    // Tailwind v4 slate-700 and slate-600 in sRGB.
    expect(slateFill).toContain('bg-slate-700')
    expect(slateFill).toContain('hover:bg-slate-600')
    expect(ratio('#ffffff', '#314158')).toBeGreaterThanOrEqual(4.5)
    expect(ratio('#ffffff', '#45556c')).toBeGreaterThanOrEqual(4.5)
  })
})
