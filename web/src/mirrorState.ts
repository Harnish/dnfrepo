/** Which slug (if any) the page currently treats as a mirror. Only first-stage repos can be mirrors. */
export function nextMirrorFor(was: string | null, slug: string, first: boolean, probe: boolean, wantParam: boolean): string | null {
  if (!first) return null
  if (probe || wantParam) return slug
  return was === slug ? was : null
}
