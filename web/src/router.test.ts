import { describe, expect, it } from 'vitest'
import { href, pkgHref, route } from './router'

function at(h: string) {
  location.hash = h
  window.dispatchEvent(new HashChangeEvent('hashchange'))
  return route.value
}

describe('router', () => {
  it('parses routes', () => {
    expect(at('#/').name).toBe('overview')
    expect(at('#/status').name).toBe('status')
    expect(at('#/packages?q=ng&all=1').query.get('q')).toBe('ng')
    const r = at('#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.0')
    expect(r.name).toBe('package')
    expect(r.params).toEqual(['FreeBSD:15:amd64', 'hello', '0.1.0'])
    expect(at('#/nope').name).toBe('notfound')
    expect(at('#/packages/too/few').name).toBe('notfound')
  })
  it('builds links', () => {
    expect(href('/packages', { q: 'a b', abi: '' })).toBe('#/packages?q=a%20b')
    expect(pkgHref('FreeBSD:15:amd64', 'x', '1.0,1')).toBe('#/packages/FreeBSD%3A15%3Aamd64/x/1.0%2C1')
  })
})
