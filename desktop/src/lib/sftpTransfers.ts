export type TransferDirection = 'upload' | 'download';
export type TransferStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted';
export type TransferFinalStatus = Exclude<TransferStatus, 'queued' | 'running'>;
export type TransferRecord = {
  id: string;
  connId: string;
  connectionName: string;
  name: string;
  direction: TransferDirection;
  status: TransferStatus;
  phase: string;
  bytes: number;
  totalBytes: number;
  files: number;
  totalFiles: number;
  startedAt: number;
  updatedAt: number;
  finishedAt?: number;
  localPath?: string;
  remotePath: string;
  error?: string;
  cancelling?: boolean;
};

export type NewTransfer = Pick<TransferRecord, 'id' | 'connId' | 'connectionName' | 'name' | 'direction' | 'remotePath'>
  & Partial<Pick<TransferRecord, 'phase' | 'totalBytes' | 'totalFiles' | 'localPath'>>
  & { status?: 'queued' | 'running' };
export type TransferUpdate = Partial<Pick<TransferRecord,
  'phase' | 'bytes' | 'totalBytes' | 'files' | 'totalFiles' | 'localPath' | 'error' | 'cancelling'>>
  & { status?: 'queued' | 'running' };

export const SFTP_TRANSFER_HISTORY_KEY = 'gcms.sftp-transfer-history.v1';
export const TRANSFER_HISTORY_LIMIT = 100;
const finalStatuses = new Set<TransferStatus>(['completed', 'failed', 'cancelled', 'interrupted']);
const transferPhases = new Set(['download', 'downloading', 'upload', 'uploading', 'archive']);
const nonnegative = (value: unknown, fallback = 0): number =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback;
const timestamp = (value: number): number => Math.floor(nonnegative(value));

export function isTransferActive(record: Pick<TransferRecord, 'status'>): boolean {
  return record.status === 'queued' || record.status === 'running';
}

/** Preserve display order while keeping every live operation and the newest 100 finished records. */
export function trimTransferHistory(history: TransferRecord[]): TransferRecord[] {
  const latest = [...history].sort((a, b) =>
    (b.finishedAt ?? b.updatedAt) - (a.finishedAt ?? a.updatedAt) || b.startedAt - a.startedAt);
  const keep = new Set(latest.filter(record => !isTransferActive(record)).slice(0, TRANSFER_HISTORY_LIMIT).map(record => record.id));
  return history.filter(record => isTransferActive(record) || keep.has(record.id));
}

export function createTransfer(input: NewTransfer, now = Date.now()): TransferRecord {
  const time = timestamp(now);
  const record: TransferRecord = {
    id: input.id, connId: input.connId, connectionName: input.connectionName, name: input.name,
    direction: input.direction, status: input.status === 'running' ? 'running' : 'queued',
    phase: input.phase || 'queued', bytes: 0, totalBytes: nonnegative(input.totalBytes),
    files: 0, totalFiles: Math.floor(nonnegative(input.totalFiles)), startedAt: time, updatedAt: time,
    remotePath: input.remotePath,
  };
  if (typeof input.localPath === 'string') record.localPath = input.localPath;
  return record;
}

export function addTransfer(history: TransferRecord[], record: TransferRecord): TransferRecord[] {
  // IDs are operation identities. A duplicate add must never revive a terminal transfer.
  if (history.some(existing => existing.id === record.id)) return history;
  return trimTransferHistory([record, ...history]);
}

function applyUpdate(record: TransferRecord, patch: TransferUpdate, now: number): TransferRecord {
  const next = { ...record, updatedAt: Math.max(record.updatedAt, timestamp(now)) };
  if (typeof patch.phase === 'string') next.phase = patch.phase;
  if (patch.status === 'running') next.status = 'running';
  for (const key of ['bytes', 'totalBytes', 'files', 'totalFiles'] as const) {
    if (patch[key] !== undefined) {
      const value = Math.max(record[key], nonnegative(patch[key], record[key]));
      next[key] = key === 'files' || key === 'totalFiles' ? Math.floor(value) : value;
    }
  }
  if (typeof patch.localPath === 'string') next.localPath = patch.localPath;
  if (typeof patch.error === 'string') next.error = patch.error;
  if (typeof patch.cancelling === 'boolean') next.cancelling = patch.cancelling;
  return next;
}

/** Ignore unknown IDs and late progress/cancel responses after any terminal event. */
export function updateTransfer(history: TransferRecord[], id: string, patch: TransferUpdate, now = Date.now()): TransferRecord[] {
  const index = history.findIndex(record => record.id === id && isTransferActive(record));
  if (index < 0) return history;
  const next = [...history];
  next[index] = applyUpdate(history[index], patch, now);
  return next;
}

export function finishTransfer(history: TransferRecord[], id: string, status: TransferFinalStatus, patch: TransferUpdate = {}, now = Date.now()): TransferRecord[] {
  if (!finalStatuses.has(status)) return history;
  const index = history.findIndex(record => record.id === id && isTransferActive(record));
  if (index < 0) return history;
  const updated = applyUpdate(history[index], patch, now);
  const next = [...history];
  next[index] = { ...updated, status, finishedAt: updated.updatedAt, cancelling: false };
  return trimTransferHistory(next);
}

/** Decode a deliberately small whitelist; connection objects and credentials are never persisted. */
function decodeRecord(value: unknown): TransferRecord | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  if (typeof raw.id !== 'string' || !raw.id || typeof raw.connId !== 'string'
    || typeof raw.name !== 'string' || !raw.name || typeof raw.remotePath !== 'string'
    || (raw.direction !== 'upload' && raw.direction !== 'download')
    || (raw.status !== 'queued' && raw.status !== 'running' && !finalStatuses.has(raw.status as TransferStatus))) return null;
  const startedAt = timestamp(nonnegative(raw.startedAt));
  const updatedAt = Math.max(startedAt, timestamp(nonnegative(raw.updatedAt, startedAt)));
  const record: TransferRecord = {
    id: raw.id, connId: raw.connId, connectionName: typeof raw.connectionName === 'string' ? raw.connectionName : '',
    name: raw.name, direction: raw.direction, status: raw.status as TransferStatus,
    phase: typeof raw.phase === 'string' ? raw.phase : '', bytes: nonnegative(raw.bytes),
    totalBytes: nonnegative(raw.totalBytes), files: Math.floor(nonnegative(raw.files)),
    totalFiles: Math.floor(nonnegative(raw.totalFiles)), startedAt, updatedAt, remotePath: raw.remotePath,
  };
  if (typeof raw.finishedAt === 'number' && Number.isFinite(raw.finishedAt)) record.finishedAt = Math.max(updatedAt, timestamp(raw.finishedAt));
  if (typeof raw.localPath === 'string') record.localPath = raw.localPath;
  if (typeof raw.error === 'string') record.error = raw.error;
  if (typeof raw.cancelling === 'boolean') record.cancelling = raw.cancelling;
  return record;
}

export function serializeTransferHistory(history: TransferRecord[]): string {
  return JSON.stringify(trimTransferHistory(history).map(decodeRecord).filter((record): record is TransferRecord => record !== null));
}

/** Restored records are history, not resumable jobs. Never replay an upload or download. */
export function readTransferHistory(raw: string | null | undefined, now = Date.now()): TransferRecord[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const records: TransferRecord[] = [];
    for (const value of parsed) {
      const record = decodeRecord(value);
      if (!record || seen.has(record.id)) continue;
      seen.add(record.id);
      if (isTransferActive(record)) {
        record.status = 'interrupted';
        record.phase = 'interrupted';
        record.updatedAt = Math.max(record.updatedAt, timestamp(now));
        record.finishedAt = record.updatedAt;
        record.cancelling = false;
        record.error = '应用重新启动，传输已中断';
      }
      records.push(record);
    }
    return trimTransferHistory(records);
  } catch { return []; }
}

export function transferPercent(record: Pick<TransferRecord, 'bytes' | 'totalBytes'>): number | null {
  if (!Number.isFinite(record.totalBytes) || record.totalBytes <= 0) return null;
  return Math.min(100, Math.max(0, Math.floor(nonnegative(record.bytes) / record.totalBytes * 100)));
}

/** Average throughput over the observed operation, not an instantaneous network speed. */
export function transferMetrics(record: TransferRecord): { bytesPerSecond: number | null; etaSeconds: number | null } {
  const elapsed = (record.updatedAt - record.startedAt) / 1000;
  if (record.status !== 'running' || record.cancelling || !transferPhases.has(record.phase)
    || elapsed < 1 || !Number.isFinite(elapsed) || record.bytes <= 0 || !Number.isFinite(record.bytes)) {
    return { bytesPerSecond: null, etaSeconds: null };
  }
  const bytesPerSecond = record.bytes / elapsed;
  const remaining = record.totalBytes - record.bytes;
  return {
    bytesPerSecond,
    etaSeconds: record.totalBytes > 0 && Number.isFinite(remaining) && remaining >= 0 ? Math.ceil(remaining / bytesPerSecond) : null,
  };
}

export function formatTransferBytes(value: number): string {
  let bytes = nonnegative(value);
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let unit = 0;
  while (bytes >= 1024 && unit < units.length - 1) { bytes /= 1024; unit++; }
  return `${unit === 0 ? Math.floor(bytes) : bytes >= 100 ? Math.round(bytes) : Number(bytes.toFixed(1))} ${units[unit]}`;
}

export function formatTransferDuration(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '';
  const seconds = Math.ceil(value);
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60), remainder = minutes % 60;
  return `${hours} 小时${remainder ? ` ${remainder} 分钟` : ''}`;
}

export function transferStatusText(record: Pick<TransferRecord, 'status' | 'direction' | 'phase' | 'cancelling'>): string {
  if (record.status === 'completed') return '已完成';
  if (record.status === 'failed') return '失败';
  if (record.status === 'cancelled') return '已取消';
  if (record.status === 'interrupted') return '已中断';
  if (record.cancelling) return '正在取消';
  const phases: Record<string, string> = {
    choosing: '选择保存位置', scanning: '扫描目录', archive: '打包下载', dragging: '拖到本机', dropped: '等待开始下载',
  };
  return (Object.hasOwn(phases, record.phase) ? phases[record.phase] : '')
    || (record.status === 'queued' ? '等待中' : record.direction === 'upload' ? '上传中' : '下载中');
}

export function transferProgressText(record: TransferRecord): string {
  if (record.phase === 'scanning' && isTransferActive(record)) return `已发现 ${record.totalFiles} 个文件`;
  const parts: string[] = [];
  if (record.totalBytes > 0) parts.push(`${formatTransferBytes(record.bytes)} / ${formatTransferBytes(record.totalBytes)}`);
  else if (record.bytes > 0) parts.push(formatTransferBytes(record.bytes));
  if (record.totalFiles > 0) parts.push(`${record.files}/${record.totalFiles} 个文件`);
  return parts.join(' · ');
}
