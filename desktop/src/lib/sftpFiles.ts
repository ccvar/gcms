/** Keep file extensions when choosing a non-destructive duplicate name. */
export function copyTargetName(name: string, dir: boolean, taken: Set<string>): string {
  if (!taken.has(name)) return name;
  let stem = name;
  let ext = '';
  if (!dir) {
    const dot = name.lastIndexOf('.');
    if (dot > 0) { stem = name.slice(0, dot); ext = name.slice(dot); }
  }
  for (let i = 1; ; i++) {
    const candidate = `${stem} 副本${i === 1 ? '' : ` ${i}`}${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
}

export function fitFileMenu(x: number, y: number, width: number, height: number, viewportWidth: number, viewportHeight: number) {
  return {
    x: Math.max(8, Math.min(x, viewportWidth - width - 8)),
    y: Math.max(8, Math.min(y, viewportHeight - height - 8)),
  };
}
