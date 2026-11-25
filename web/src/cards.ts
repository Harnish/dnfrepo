/** Label for each cell of a row (null = none), given header texts and each cell's colSpan. */
export function cellLabels(headers: string[], spans: number[]): (string | null)[] {
  let col = 0
  return spans.map((span) => {
    const h = span === 1 ? headers[col]?.replace(/[▲▼]/g, '').trim() : ''
    col += span
    return h || null
  })
}

function labelTable(t: HTMLTableElement) {
  const headers = [...t.querySelectorAll('thead th')].map((th) => th.textContent ?? '')
  for (const tr of t.querySelectorAll<HTMLTableRowElement>('tbody tr')) {
    const cells = [...tr.cells]
    cellLabels(headers, cells.map((c) => c.colSpan)).forEach((l, i) => {
      const c = cells[i]
      if (l === null) c.removeAttribute('data-label')
      else if (c.dataset.label !== l) c.dataset.label = l
    })
  }
}

/** Copy header text onto every table cell as data-label, which the phone card layout shows. */
// ponytail: one DOM observer instead of data-label in each of the 10 tables; new tables get it free.
export function watchCardLabels(root: HTMLElement) {
  const run = () => root.querySelectorAll('table').forEach(labelTable)
  new MutationObserver(run).observe(root, { childList: true, subtree: true, characterData: true })
  run()
}
