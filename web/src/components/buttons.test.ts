import { describe, it, expect } from 'vitest'
import { btn, primaryFill, slateFill } from './buttons'

const lum = (hex: string) => {
  const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map(c => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a: string, b: string) => {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

// #3241: the slate buttons took the page's text colour, dark slate in the
// light theme, which put dark text on a dark fill (1.7:1). The shared fill
// must carry its own light text, at AA on both its rest and hover shades.
describe('slateFill', () => {
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

// #3265: the hand-written primary buttons set the emerald fill themselves and
// most set no text colour, so in the dark theme they took the page's zinc-100
// (3.3:1). Every primary button now takes its colours from primaryFill, so the
// fix for the remaining contrast gap is one line in buttons.ts.
describe('primaryFill', () => {
  const sources = import.meta.glob(['../**/*.{ts,tsx}', '!../**/*.test.{ts,tsx}', '!./buttons.ts'], {
    query: '?raw',
    import: 'default',
    eager: true,
  }) as Record<string, string>

  it('sets its own text colour, and btn.primary uses it', () => {
    expect(primaryFill.split(' ')).toContain('text-white')
    expect(btn.primary).toContain(primaryFill)
  })

  it('keeps white text at AA on the rest and hover fills', () => {
    // Tailwind v4 emerald-700 and emerald-800 in sRGB.
    expect(primaryFill).toContain('bg-emerald-700')
    expect(primaryFill).toContain('hover:bg-emerald-800')
    expect(ratio('#ffffff', '#007a55')).toBeGreaterThanOrEqual(4.5)
    expect(ratio('#ffffff', '#006045')).toBeGreaterThanOrEqual(4.5)
  })

  it('is the only place the primary emerald fill is written', () => {
    const hits: string[] = []
    for (const [file, s] of Object.entries(sources)) {
      s.split('\n').forEach((line, i) => {
        if (/bg-emerald-600 hover:bg-emerald-500/.test(line)) hits.push(`${file}:${i + 1}`)
      })
    }
    expect(Object.keys(sources).length).toBeGreaterThan(100)
    expect(hits).toEqual([])
  })
})
