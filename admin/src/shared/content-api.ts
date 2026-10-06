import { requestEnvelope } from './admin-api';

export interface ContentSummary {
  content_id: string;
  content_type: string;
  display_name: string;
  draft_id?: string;
  draft_version?: string;
  latest_revision: number;
  approved_revision: number;
  updated_at: string;
}

export interface ReferenceHit {
  content_id: string;
  pointer: string;
}

export interface References {
  content_id: string;
  incoming: ReferenceHit[];
  outgoing: ReferenceHit[];
  broken: ReferenceHit[];
}

export interface DiffChange {
  pointer: string;
  kind: 'added' | 'removed' | 'changed';
  before?: unknown;
  after?: unknown;
}

export interface CatalogIssue {
  content_id: string;
  path: string;
  code: string;
  message: string;
}

export interface ValidationReport {
  source: 'draft' | 'approved';
  entry_count: number;
  valid: boolean;
  issues: CatalogIssue[];
}

export interface ImportResult {
  content_id: string;
  status: 'created' | 'would_create' | 'conflict' | 'invalid';
  draft_id?: string;
  errors: { pointer: string; code: string; message: string }[];
}

export interface GenericDraft {
  draft_id: string;
  game_id: string;
  content_id: string;
  draft_version: string;
  definition: Record<string, unknown>;
  updated_at: string;
}

export interface SnapshotJob {
  job_id: string;
  game_id: string;
  kind: 'game_snapshot';
  release_id: string;
  release_version: number;
  entry_count: number;
  status: 'queued' | 'running' | 'retry_wait' | 'built' | 'failed';
  public_artifact_sha256: string | null;
  error_code: string | null;
  created_at: string;
}

export interface ReleaseRow {
  release_id: string;
  release_version: number;
  status: string;
  scope: string | null;
  entry_count: number;
  created_at: string;
  activated_at: string | null;
  public_artifact_sha256: string | null;
}

export interface ReleaseListing {
  releases: ReleaseRow[];
  realms: { realm_id: string; display_name: string; active_release_id: string | null }[];
  snapshot_jobs: SnapshotJob[];
  activations: {
    request_id: string;
    realm_id: string;
    release_id: string;
    status: 'pending' | 'activated' | 'rejected';
    error_code: string | null;
    previous_release_id: string | null;
    created_at: string;
    decided_at: string | null;
  }[];
}

const gamePath = (gameId: string, suffix: string): string =>
  `/api/v1/games/${encodeURIComponent(gameId)}${suffix}`;

function asRecord(value: unknown, message: string): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(message);
  }
  return value as Record<string, unknown>;
}

function asArray<T>(value: unknown, message: string): T[] {
  if (!Array.isArray(value)) {
    throw new Error(message);
  }
  return value as T[];
}

/** 生成符合服务端约束（16–128 个安全字符）的随机幂等键。 */
export function newIdempotencyKey(): string {
  const bytes = new Uint8Array(18);
  crypto.getRandomValues(bytes);
  return `adm-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`;
}

export const contentApi = {
  async contentTypes(): Promise<string[]> {
    return asArray<string>(
      (await requestEnvelope('/api/v1/content-types')).data,
      '内容类型列表无效',
    );
  },

  async search(gameId: string, query: string, contentType: string): Promise<ContentSummary[]> {
    const params = new URLSearchParams();
    if (query !== '') params.set('q', query);
    if (contentType !== '') params.set('type', contentType);
    const { data } = await requestEnvelope(gamePath(gameId, `/content?${params.toString()}`));
    return asArray<ContentSummary>(data, '内容搜索结果无效');
  },

  async references(gameId: string, contentId: string): Promise<References> {
    const { data } = await requestEnvelope(
      gamePath(gameId, `/content-references?content_id=${encodeURIComponent(contentId)}`),
    );
    const value = asRecord(data, '引用结果无效');
    if (value.content_id !== contentId) throw new Error('引用结果与请求内容不一致');
    return value as unknown as References;
  },

  async diff(gameId: string, contentId: string, from: string, to: string): Promise<DiffChange[]> {
    const params = new URLSearchParams({ content_id: contentId, from, to });
    const { data } = await requestEnvelope(gamePath(gameId, `/content-diff?${params.toString()}`));
    return asArray<DiffChange>(asRecord(data, '差异结果无效').changes, '差异结果无效');
  },

  async getDraft(gameId: string, draftId: string): Promise<GenericDraft> {
    const { data } = await requestEnvelope(
      gamePath(gameId, `/content-drafts/${encodeURIComponent(draftId)}`),
    );
    return asRecord(data, '草稿无效') as unknown as GenericDraft;
  },

  async createDraft(
    gameId: string,
    contentId: string,
    definition: Record<string, unknown>,
    csrfToken: string,
  ): Promise<GenericDraft> {
    const { data } = await requestEnvelope(gamePath(gameId, '/content-drafts'), {
      method: 'POST',
      body: { content_id: contentId, expected_draft_version: '0', definition },
      csrfToken,
    });
    return asRecord(data, '草稿无效') as unknown as GenericDraft;
  },

  async updateDraft(
    gameId: string,
    draft: GenericDraft,
    definition: Record<string, unknown>,
    csrfToken: string,
  ): Promise<GenericDraft> {
    const { data } = await requestEnvelope(
      gamePath(gameId, `/content-drafts/${encodeURIComponent(draft.draft_id)}`),
      {
        method: 'PUT',
        body: { expected_draft_version: draft.draft_version, definition },
        csrfToken,
      },
    );
    return asRecord(data, '草稿无效') as unknown as GenericDraft;
  },

  async validateDraft(
    gameId: string,
    draftId: string,
    csrfToken: string,
  ): Promise<{ valid: boolean; errors: ImportResult['errors'] }> {
    const { data } = await requestEnvelope(
      gamePath(gameId, `/content-drafts/${encodeURIComponent(draftId)}/validate`),
      { method: 'POST', csrfToken },
    );
    return asRecord(data, '校验结果无效') as unknown as {
      valid: boolean;
      errors: ImportResult['errors'];
    };
  },

  async submitDraft(gameId: string, draft: GenericDraft, csrfToken: string): Promise<unknown> {
    return (
      await requestEnvelope(
        gamePath(gameId, `/content-drafts/${encodeURIComponent(draft.draft_id)}/submit`),
        {
          method: 'POST',
          body: { expected_draft_version: draft.draft_version },
          csrfToken,
        },
      )
    ).data;
  },

  async revision(
    gameId: string,
    contentId: string,
    revision: number,
  ): Promise<Record<string, unknown>> {
    const params = new URLSearchParams({ content_id: contentId, revision: String(revision) });
    const { data } = await requestEnvelope(
      gamePath(gameId, `/content-revisions?${params.toString()}`),
    );
    return asRecord(asRecord(data, 'revision 无效').definition, 'revision 无效');
  },

  async importDefinitions(
    gameId: string,
    definitions: unknown[],
    dryRun: boolean,
    csrfToken: string,
  ): Promise<ImportResult[]> {
    const { data } = await requestEnvelope(gamePath(gameId, '/content-imports'), {
      method: 'POST',
      body: { dry_run: dryRun, definitions },
      csrfToken,
    });
    return asArray<ImportResult>(data, '导入结果无效');
  },

  async validationReport(
    gameId: string,
    source: 'draft' | 'approved',
    csrfToken: string,
  ): Promise<ValidationReport> {
    const { data } = await requestEnvelope(gamePath(gameId, '/validation-report'), {
      method: 'POST',
      body: { source },
      csrfToken,
    });
    return asRecord(data, '校验报告无效') as unknown as ValidationReport;
  },

  async previewBattle(
    gameId: string,
    source: 'draft' | 'approved',
    request: Record<string, unknown>,
    csrfToken: string,
  ): Promise<Record<string, unknown>> {
    const { data } = await requestEnvelope(gamePath(gameId, '/previews/battle'), {
      method: 'POST',
      body: { source, request },
      csrfToken,
    });
    return asRecord(data, '预览结果无效');
  },

  async startSnapshot(
    gameId: string,
    idempotencyKey: string,
    csrfToken: string,
  ): Promise<SnapshotJob> {
    const { data } = await requestEnvelope(gamePath(gameId, '/snapshot-builds'), {
      method: 'POST',
      csrfToken,
      idempotencyKey,
    });
    const job = asRecord(data, '快照任务无效') as unknown as SnapshotJob;
    if (job.game_id !== gameId) throw new Error('快照任务与当前游戏不一致');
    return job;
  },

  async getSnapshot(gameId: string, jobId: string): Promise<SnapshotJob> {
    const { data } = await requestEnvelope(
      gamePath(gameId, `/snapshot-builds/${encodeURIComponent(jobId)}`),
    );
    const job = asRecord(data, '快照任务无效') as unknown as SnapshotJob;
    if (job.job_id !== jobId || job.game_id !== gameId) throw new Error('快照任务身份不一致');
    return job;
  },

  async listReleases(gameId: string): Promise<ReleaseListing> {
    const { data } = await requestEnvelope(gamePath(gameId, '/releases'));
    const value = asRecord(data, 'Release 列表无效');
    return {
      releases: asArray<ReleaseRow>(value.releases, 'Release 列表无效'),
      realms: asArray<ReleaseListing['realms'][number]>(value.realms, 'Realm 列表无效'),
      snapshot_jobs: asArray<SnapshotJob>(value.snapshot_jobs, '快照任务列表无效'),
      activations: asArray<ReleaseListing['activations'][number]>(
        value.activations,
        '激活请求列表无效',
      ),
    };
  },

  async requestActivation(
    gameId: string,
    realmId: string,
    releaseId: string,
    idempotencyKey: string,
    csrfToken: string,
  ): Promise<unknown> {
    return (
      await requestEnvelope(
        gamePath(gameId, `/realms/${encodeURIComponent(realmId)}/activations`),
        {
          method: 'POST',
          body: { release_id: releaseId },
          csrfToken,
          idempotencyKey,
        },
      )
    ).data;
  },
};
