/** Which slug (if any) the page currently treats as a mirror. Only first-stage repos can be mirrors. */
export function nextMirrorFor(was: string | null, slug: string, first: boolean, probe: boolean, wantParam: boolean): string | null {
  if (!first) return null
  if (probe || wantParam) return slug
  return was === slug ? was : null
}

/** Cache-mode package order: most recently pulled first (retention keeps these). */
export function byLastPulled<T extends { last_pulled: string }>(pkgs: T[]): T[] {
  return [...pkgs].sort((a, b) => b.last_pulled.localeCompare(a.last_pulled))
}
