// CSS Highlights style ranges without altering React's text nodes or copy data.
export function highlightActivity(root: HTMLElement, query: string): { ranges: Range[]; clear: () => void } {
  const registry = (CSS as unknown as { highlights?: Map<string, unknown> }).highlights;
  const HighlightClass = (globalThis as unknown as { Highlight?: new () => { add: (range: Range) => void } }).Highlight;
  const ranges: Range[] = [];
  const needle = query.toLowerCase();
  if (needle) {
    const bodies = root.querySelectorAll<HTMLElement>("[data-search-body]");
    for (const body of bodies.length ? bodies : [root]) {
      const walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
      const nodes: Text[] = [];
      let text = "";
      let node: Node | null;
      while ((node = walker.nextNode())) {
        if (node.parentElement?.closest('button,[aria-hidden="true"]')) continue;
        nodes.push(node as Text);
        text += node.textContent;
      }
      const lower = text.toLowerCase();
      const starts: number[] = [],
        ends: number[] = [];
      // Most case folding preserves UTF-16 length. Only build a character map
      // for expansions such as İ, avoiding two large arrays for long HTTP bodies.
      if (lower.length !== text.length) {
        let originalOffset = 0;
        for (const char of text) {
          for (let i = 0; i < char.toLowerCase().length; i++) {
            starts.push(originalOffset);
            ends.push(originalOffset + char.length);
          }
          originalOffset += char.length;
        }
      }
      const offsets: number[] = [];
      let total = 0;
      for (const node of nodes) {
        offsets.push(total);
        total += node.length;
      }
      const position = (offset: number) => {
        let lo = 0,
          hi = offsets.length - 1;
        while (lo < hi) {
          const mid = Math.ceil((lo + hi) / 2);
          if (offsets[mid] <= offset) lo = mid;
          else hi = mid - 1;
        }
        return lo;
      };
      for (let match = lower.indexOf(needle); match >= 0; match = lower.indexOf(needle, match + needle.length)) {
        const start = starts.length ? starts[match] : match;
        const finish = ends.length ? ends[match + needle.length - 1] : match + needle.length;
        const from = position(start),
          to = position(finish - 1);
        const range = document.createRange();
        range.setStart(nodes[from], start - offsets[from]);
        range.setEnd(nodes[to], finish - offsets[to]);
        ranges.push(range);
      }
    }
  }
  // One stable name is sufficient per mounted transcript; cleanup is identity checked.
  const value = HighlightClass ? new HighlightClass() : undefined;
  if (registry && value && ranges.length) {
    for (const range of ranges) value.add(range);
    registry.set("activity-search", value);
  }
  return {
    ranges,
    clear: () => {
      if (registry && registry.get("activity-search") === value) registry.delete("activity-search");
    },
  };
}
