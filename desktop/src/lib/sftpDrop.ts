export type SftpDropRow = {
  path?: string;
  dir: boolean;
  link: boolean;
};

export type SftpDropContext = {
  connId: string;
  remoteDir: string;
  busy: boolean;
  transferring: boolean;
  nativeDragging: boolean;
  insidePanel: boolean;
  /** Resolve this row from the current remote file list, never from dragged data. */
  hoveredRow?: SftpDropRow | null;
  /** Whether the resolved destination (or one of its ancestors) is being deleted. */
  targetDeleting?: boolean;
};

export type SftpDropTarget = Readonly<{ connId: string; remoteDir: string }>;

/** Resolve and freeze the destination before any asynchronous local-file work. */
export function resolveSftpDropTarget(context: SftpDropContext): SftpDropTarget | null {
  if (!context.insidePanel || context.busy || context.transferring || context.nativeDragging
    || context.targetDeleting || !context.connId || !context.remoteDir) return null;
  const row = context.hoveredRow;
  if (row && !row.path) return null;
  const remoteDir = row?.dir && !row.link ? row.path! : context.remoteDir;
  return Object.freeze({ connId: context.connId, remoteDir });
}

/**
 * Obtain a native dropped file/directory name without changing meaningful spaces.
 * POSIX absolute paths may contain literal backslashes. Drive and UNC paths use
 * Windows separators; relative backslash paths are accepted as Windows paths.
 * Roots and dot segments are not uploadable entries. No filesystem access occurs.
 */
export function localFileBasename(path: string): string | null {
  if (!path || path.includes('\0')) return null;
  const drive = /^[A-Za-z]:/.test(path);
  const unc = path.startsWith('\\\\') || path.startsWith('//');
  const windows = drive || unc || path.startsWith('\\') || (!path.includes('/') && path.includes('\\'));
  if (drive && !/^[A-Za-z]:[\\/]/.test(path)) return null;
  const segments = (drive ? path.slice(2) : path)
    .split(windows ? /[\\/]+/ : /\/+/)
    .filter(segment => segment !== '');
  if (!segments.length || segments.some(segment => segment === '.' || segment === '..')) return null;
  if (unc && segments.length <= 2) return null;
  return segments[segments.length - 1];
}
