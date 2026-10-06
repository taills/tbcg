import { validateItemDefinition } from './content/item-schema';
import type { ItemDefinition } from './content/item-schema';

export type AdminRole = 'Reader' | 'Author' | 'Reviewer' | 'Publisher';

type Envelope<T> = {
  success: boolean;
  data: T;
  error: ApiErrorBody | null;
  meta: Record<string, unknown>;
};

export interface ApiErrorBody {
  code: string;
  message: string;
  fields: readonly ApiFieldError[];
}

export interface ApiFieldError {
  pointer: string;
  code: string;
  message: string;
}

export interface AdminUser {
  admin_id: string;
  username: string;
}

export interface GameRole {
  game_id: string;
  roles: readonly AdminRole[];
}

export interface AdminSession {
  authenticated: boolean;
  csrf_token: string;
  user: AdminUser | null;
  game_roles: readonly GameRole[];
}

export interface Draft {
  draft_id: string;
  game_id: string;
  content_id: string;
  draft_version: string;
  definition: ItemDefinition;
  updated_at: string;
}

export interface Revision {
  game_id: string;
  content_id: string;
  revision: number;
  body_hash: string;
  definition: ItemDefinition;
  submitted_at: string;
}

export interface Approval {
  game_id: string;
  content_id: string;
  revision: number;
  body_hash: string;
  decision: 'approved' | 'rejected';
  reviewer_id: string;
  comment?: string;
  reviewed_at: string;
}

export interface CreateDraftRequest {
  content_id: string;
  expected_draft_version: '0';
  definition: ItemDefinition;
}

export interface UpdateDraftRequest {
  expected_draft_version: string;
  definition: ItemDefinition;
}

export interface ValidationResult {
  valid: boolean;
  errors: readonly ApiFieldError[];
}

export interface Page<T> {
  items: readonly T[];
  total: number;
  limit: number;
  offset: number;
}

export type BuildStatus = 'queued' | 'running' | 'retry_wait' | 'built' | 'failed';

export interface BuildCandidate {
  game_id: string;
  content_id: string;
  revision: number;
  body_hash: string;
  submitted_at: string;
}

export interface BuildJob {
  job_id: string;
  game_id: string;
  content_id: string;
  revision: number;
  body_hash: string;
  release_id: string;
  release_version: number;
  status: BuildStatus;
  created_at: string;
  updated_at: string;
  public_artifact_sha256: string | null;
  error_code: string | null;
}

export interface StartBuildRequest {
  content_id: string;
  revision: number;
  body_hash: string;
}

export class AdminApiError extends Error {
  public readonly status: number;
  public readonly code: string;
  public readonly data: unknown;
  public readonly fields: readonly ApiFieldError[];

  public constructor(status: number, body: ApiErrorBody, data: unknown) {
    super(body.message);
    this.name = 'AdminApiError';
    this.status = status;
    this.code = body.code;
    this.data = data;
    this.fields = body.fields;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function toApiError(value: unknown, status: number): ApiErrorBody {
  if (isRecord(value)) {
    const fields = Array.isArray(value.fields)
      ? value.fields.filter(
          (field): field is ApiFieldError =>
            isRecord(field) &&
            typeof field.pointer === 'string' &&
            typeof field.code === 'string' &&
            typeof field.message === 'string',
        )
      : [];
    if (typeof value.code === 'string' && typeof value.message === 'string') {
      return { code: value.code, message: value.message, fields };
    }
  }
  return { code: 'http_error', message: `请求失败（${status}）`, fields: [] };
}

function parseAdminSession(value: unknown): AdminSession {
  if (
    !isRecord(value) ||
    typeof value.authenticated !== 'boolean' ||
    typeof value.csrf_token !== 'string' ||
    !Array.isArray(value.game_roles)
  ) {
    throw new Error('管理服务返回了无效会话');
  }
  let user: AdminUser | null = null;
  if (value.user !== null) {
    if (
      !isRecord(value.user) ||
      typeof value.user.admin_id !== 'string' ||
      typeof value.user.username !== 'string'
    ) {
      throw new Error('管理服务返回了无效管理员身份');
    }
    user = { admin_id: value.user.admin_id, username: value.user.username };
  }
  const gameRoles = value.game_roles.map((entry): GameRole => {
    if (!isRecord(entry) || typeof entry.game_id !== 'string' || !Array.isArray(entry.roles)) {
      throw new Error('管理服务返回了无效 Game 权限');
    }
    const roles = entry.roles.map((role) => {
      if (role !== 'Reader' && role !== 'Author' && role !== 'Reviewer' && role !== 'Publisher') {
        throw new Error('管理服务返回了未知角色');
      }
      return role;
    });
    return { game_id: entry.game_id, roles };
  });
  if (value.authenticated !== (user !== null)) {
    throw new Error('管理服务会话身份状态不一致');
  }
  return {
    authenticated: value.authenticated,
    csrf_token: value.csrf_token,
    user,
    game_roles: gameRoles,
  };
}

function parseDefinition(value: unknown): ItemDefinition {
  const result = validateItemDefinition(value);
  if (!result.valid || result.data === undefined) {
    throw new Error('管理服务返回的物品 Definition 不符合共享 Schema');
  }
  return result.data;
}

function parseDraft(value: unknown): Draft {
  if (
    !isRecord(value) ||
    typeof value.draft_id !== 'string' ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    typeof value.draft_version !== 'string' ||
    !/^(0|[1-9][0-9]{0,18})$/.test(value.draft_version) ||
    BigInt(value.draft_version) > 9223372036854775807n ||
    typeof value.updated_at !== 'string'
  ) {
    throw new Error('管理服务返回了无效草稿或草稿版本');
  }
  const definition = parseDefinition(value.definition);
  if (definition.game_id !== value.game_id || definition.content_id !== value.content_id) {
    throw new Error('管理服务返回了 Game 或内容 ID 不匹配的草稿');
  }
  return {
    draft_id: value.draft_id,
    game_id: value.game_id,
    content_id: value.content_id,
    draft_version: value.draft_version,
    definition,
    updated_at: value.updated_at,
  };
}

function parseRevision(value: unknown): Revision {
  if (
    !isRecord(value) ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    typeof value.revision !== 'number' ||
    !Number.isInteger(value.revision) ||
    value.revision < 1 ||
    value.revision > 2147483647 ||
    typeof value.body_hash !== 'string' ||
    !/^[a-f0-9]{64}$/.test(value.body_hash) ||
    typeof value.submitted_at !== 'string'
  ) {
    throw new Error('管理服务返回了无效不可变 revision');
  }
  const definition = parseDefinition(value.definition);
  if (
    definition.game_id !== value.game_id ||
    definition.content_id !== value.content_id ||
    definition.revision !== value.revision
  ) {
    throw new Error('管理服务返回了身份不一致的不可变 revision');
  }
  return {
    game_id: value.game_id,
    content_id: value.content_id,
    revision: value.revision,
    body_hash: value.body_hash,
    definition,
    submitted_at: value.submitted_at,
  };
}

function parseApproval(value: unknown): Approval {
  if (
    !isRecord(value) ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    typeof value.revision !== 'number' ||
    !Number.isInteger(value.revision) ||
    value.revision < 1 ||
    value.revision > 2147483647 ||
    typeof value.body_hash !== 'string' ||
    !/^[a-f0-9]{64}$/.test(value.body_hash) ||
    (value.decision !== 'approved' && value.decision !== 'rejected') ||
    typeof value.reviewer_id !== 'string' ||
    typeof value.reviewed_at !== 'string'
  ) {
    throw new Error('管理服务返回了无效审核记录');
  }
  return {
    game_id: value.game_id,
    content_id: value.content_id,
    revision: value.revision,
    body_hash: value.body_hash,
    decision: value.decision,
    reviewer_id: value.reviewer_id,
    ...(typeof value.comment === 'string' ? { comment: value.comment } : {}),
    reviewed_at: value.reviewed_at,
  };
}

function parseValidationResult(value: unknown): ValidationResult {
  if (!isRecord(value) || typeof value.valid !== 'boolean' || !Array.isArray(value.errors)) {
    throw new Error('管理服务返回了无效校验报告');
  }
  const errors = value.errors.map((issue): ApiFieldError => {
    if (
      !isRecord(issue) ||
      typeof issue.pointer !== 'string' ||
      typeof issue.code !== 'string' ||
      typeof issue.message !== 'string'
    ) {
      throw new Error('管理服务校验报告包含无效字段错误');
    }
    return { pointer: issue.pointer, code: issue.code, message: issue.message };
  });
  return { valid: value.valid, errors };
}

const sha256Pattern = /^[a-f0-9]{64}$/;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const idempotencyKeyPattern = /^[A-Za-z0-9._~-]{16,128}$/;
const buildStatuses: readonly BuildStatus[] = [
  'queued',
  'running',
  'retry_wait',
  'built',
  'failed',
];
function isBuildStatus(value: unknown): value is BuildStatus {
  return typeof value === 'string' && (buildStatuses as readonly string[]).includes(value);
}

const buildJobKeys = new Set([
  'job_id',
  'game_id',
  'content_id',
  'revision',
  'body_hash',
  'release_id',
  'release_version',
  'status',
  'created_at',
  'updated_at',
  'public_artifact_sha256',
  'error_code',
]);

function isRevisionNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 2147483647;
}

function parseBuildCandidate(value: unknown): BuildCandidate {
  if (
    !isRecord(value) ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    !isRevisionNumber(value.revision) ||
    typeof value.body_hash !== 'string' ||
    !sha256Pattern.test(value.body_hash) ||
    typeof value.submitted_at !== 'string' ||
    Object.keys(value).length !== 5
  ) {
    throw new Error('管理服务返回了无效构建候选');
  }
  return {
    game_id: value.game_id,
    content_id: value.content_id,
    revision: value.revision,
    body_hash: value.body_hash,
    submitted_at: value.submitted_at,
  };
}

function parseBuildJob(value: unknown): BuildJob {
  if (
    !isRecord(value) ||
    Object.keys(value).some((key) => !buildJobKeys.has(key)) ||
    typeof value.job_id !== 'string' ||
    !uuidPattern.test(value.job_id) ||
    typeof value.game_id !== 'string' ||
    typeof value.content_id !== 'string' ||
    !isRevisionNumber(value.revision) ||
    typeof value.body_hash !== 'string' ||
    !sha256Pattern.test(value.body_hash) ||
    typeof value.release_id !== 'string' ||
    value.release_id.length === 0 ||
    !isRevisionNumber(value.release_version) ||
    !isBuildStatus(value.status) ||
    typeof value.created_at !== 'string' ||
    typeof value.updated_at !== 'string' ||
    !(
      value.public_artifact_sha256 === null ||
      (typeof value.public_artifact_sha256 === 'string' &&
        sha256Pattern.test(value.public_artifact_sha256))
    ) ||
    !(
      value.error_code === null ||
      (typeof value.error_code === 'string' && value.error_code.length <= 64)
    )
  ) {
    throw new Error('管理服务返回了无效构建任务');
  }
  return {
    job_id: value.job_id,
    game_id: value.game_id,
    content_id: value.content_id,
    revision: value.revision,
    body_hash: value.body_hash,
    release_id: value.release_id,
    release_version: value.release_version,
    status: value.status,
    created_at: value.created_at,
    updated_at: value.updated_at,
    public_artifact_sha256: value.public_artifact_sha256,
    error_code: value.error_code,
  };
}

export async function requestEnvelope(
  path: string,
  options: {
    method?: 'GET' | 'POST' | 'PUT';
    body?: unknown;
    csrfToken?: string;
    idempotencyKey?: string;
  } = {},
): Promise<{ data: unknown; meta: Record<string, unknown> }> {
  const method = options.method ?? 'GET';
  const headers = new Headers({ Accept: 'application/json' });
  if (options.body !== undefined) {
    headers.set('Content-Type', 'application/json');
  }
  if (method !== 'GET') {
    if (options.csrfToken === undefined || options.csrfToken.length === 0) {
      throw new Error('此操作需要当前会话的 CSRF token');
    }
    headers.set('X-CSRF-Token', options.csrfToken);
  }
  if (options.idempotencyKey !== undefined) {
    headers.set('Idempotency-Key', options.idempotencyKey);
  }

  let response: Response;
  try {
    response = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      ...(options.body === undefined ? {} : { body: JSON.stringify(options.body) }),
    });
  } catch {
    throw new Error('无法连接管理服务，请检查网络后重试');
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new Error('管理服务返回了无法读取的响应');
  }
  if (!isRecord(payload) || typeof payload.success !== 'boolean' || !('data' in payload)) {
    throw new Error('管理服务响应格式无效');
  }
  if (!response.ok || !payload.success) {
    const error = toApiError(payload.error, response.status);
    throw new AdminApiError(response.status, error, payload.data);
  }
  return { data: payload.data, meta: isRecord(payload.meta) ? payload.meta : {} };
}

async function request(
  path: string,
  options: {
    method?: 'GET' | 'POST' | 'PUT';
    body?: unknown;
    csrfToken?: string;
    idempotencyKey?: string;
  } = {},
): Promise<unknown> {
  return (await requestEnvelope(path, options)).data;
}

function itemDraftPath(gameId: string, draftId: string): string {
  return `/api/v1/games/${encodeURIComponent(gameId)}/item-drafts/${encodeURIComponent(draftId)}`;
}

export const adminApi = {
  async getSession(): Promise<AdminSession> {
    return parseAdminSession(await request('/api/v1/session'));
  },

  async login(username: string, password: string, csrfToken: string): Promise<AdminSession> {
    return parseAdminSession(
      await request('/api/v1/login', {
        method: 'POST',
        body: { username, password },
        csrfToken,
      }),
    );
  },

  async logout(csrfToken: string): Promise<Record<string, never>> {
    const value = await request('/api/v1/logout', { method: 'POST', csrfToken });
    if (!isRecord(value)) {
      throw new Error('管理服务返回了无效退出响应');
    }
    return {};
  },

  async createItemDraft(
    gameId: string,
    input: CreateDraftRequest,
    csrfToken: string,
  ): Promise<Draft> {
    const draft = parseDraft(
      await request(`/api/v1/games/${encodeURIComponent(gameId)}/item-drafts`, {
        method: 'POST',
        body: input,
        csrfToken,
      }),
    );
    if (draft.game_id !== gameId || draft.content_id !== input.content_id) {
      throw new Error('管理服务返回了超出创建请求范围的草稿');
    }
    return draft;
  },

  async getItemDraft(gameId: string, draftId: string): Promise<Draft> {
    const draft = parseDraft(await request(itemDraftPath(gameId, draftId)));
    if (draft.game_id !== gameId || draft.draft_id !== draftId) {
      throw new Error('管理服务返回了超出读取请求范围的草稿');
    }
    return draft;
  },

  async listItemDrafts(
    gameId: string,
    contentId: string,
    limit = 50,
    offset = 0,
  ): Promise<Page<Draft>> {
    if (
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 50 ||
      !Number.isInteger(offset) ||
      offset < 0
    ) {
      throw new Error('草稿列表分页参数无效');
    }
    const query = new URLSearchParams({
      content_id: contentId,
      limit: String(limit),
      offset: String(offset),
    });
    const response = await requestEnvelope(
      `/api/v1/games/${encodeURIComponent(gameId)}/item-drafts?${query}`,
    );
    const { total, limit: pageLimit, offset: pageOffset } = response.meta;
    if (
      !Array.isArray(response.data) ||
      typeof total !== 'number' ||
      !Number.isSafeInteger(total) ||
      total < 0 ||
      typeof pageLimit !== 'number' ||
      pageLimit !== limit ||
      typeof pageOffset !== 'number' ||
      pageOffset !== offset
    ) {
      throw new Error('管理服务返回了无效草稿分页');
    }
    const items = response.data.map(parseDraft);
    if (
      items.length > limit ||
      items.some((draft) => draft.game_id !== gameId || draft.content_id !== contentId)
    ) {
      throw new Error('管理服务返回了跨 Game 或内容 ID 的草稿');
    }
    return { items, total, limit: pageLimit, offset: pageOffset };
  },

  async updateItemDraft(
    gameId: string,
    draftId: string,
    input: UpdateDraftRequest,
    csrfToken: string,
  ): Promise<Draft> {
    const draft = parseDraft(
      await request(itemDraftPath(gameId, draftId), { method: 'PUT', body: input, csrfToken }),
    );
    if (draft.game_id !== gameId || draft.draft_id !== draftId) {
      throw new Error('管理服务返回了超出更新请求范围的草稿');
    }
    return draft;
  },

  async validateItemDraft(
    gameId: string,
    draftId: string,
    csrfToken: string,
  ): Promise<ValidationResult> {
    return parseValidationResult(
      await request(`${itemDraftPath(gameId, draftId)}/validate`, {
        method: 'POST',
        csrfToken,
      }),
    );
  },

  async submitItemDraft(
    gameId: string,
    draftId: string,
    version: string,
    csrfToken: string,
  ): Promise<Revision> {
    const revision = parseRevision(
      await request(`${itemDraftPath(gameId, draftId)}/submit`, {
        method: 'POST',
        body: { expected_draft_version: version },
        csrfToken,
      }),
    );
    if (revision.game_id !== gameId) {
      throw new Error('管理服务返回了超出提交请求范围的 revision');
    }
    return revision;
  },

  async listReviewQueue(gameId: string, limit = 50, offset = 0): Promise<Page<Revision>> {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
    const response = await requestEnvelope(
      `/api/v1/games/${encodeURIComponent(gameId)}/review-queue?${query}`,
    );
    const { total, limit: pageLimit, offset: pageOffset } = response.meta;
    if (
      !Array.isArray(response.data) ||
      typeof total !== 'number' ||
      !Number.isInteger(total) ||
      total < 0 ||
      typeof pageLimit !== 'number' ||
      pageLimit !== limit ||
      typeof pageOffset !== 'number' ||
      pageOffset !== offset
    ) {
      throw new Error('管理服务返回了无效审核队列分页');
    }
    const items = response.data.map(parseRevision);
    if (items.some((revision) => revision.game_id !== gameId)) {
      throw new Error('管理服务返回了跨 Game 的审核队列');
    }
    return { items, total, limit: pageLimit, offset: pageOffset };
  },

  async reviewRevision(
    gameId: string,
    input: {
      content_id: string;
      revision: number;
      body_hash: string;
      decision: 'approved' | 'rejected';
      comment?: string;
    },
    csrfToken: string,
  ): Promise<Approval> {
    const approval = parseApproval(
      await request(`/api/v1/games/${encodeURIComponent(gameId)}/revision-approvals`, {
        method: 'POST',
        body: input,
        csrfToken,
      }),
    );
    if (
      approval.game_id !== gameId ||
      approval.content_id !== input.content_id ||
      approval.revision !== input.revision ||
      approval.body_hash !== input.body_hash ||
      approval.decision !== input.decision
    ) {
      throw new Error('管理服务返回了超出审批请求范围的记录');
    }
    return approval;
  },
  async listBuildCandidates(gameId: string, limit = 50, offset = 0): Promise<Page<BuildCandidate>> {
    const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
    const response = await requestEnvelope(
      `/api/v1/games/${encodeURIComponent(gameId)}/build-candidates?${query}`,
    );
    const { total, limit: pageLimit, offset: pageOffset } = response.meta;
    if (
      !Array.isArray(response.data) ||
      typeof total !== 'number' ||
      !Number.isSafeInteger(total) ||
      total < 0 ||
      pageLimit !== limit ||
      pageOffset !== offset
    ) {
      throw new Error('管理服务返回了无效构建候选分页');
    }
    const items = response.data.map(parseBuildCandidate);
    if (items.length > limit || items.some((candidate) => candidate.game_id !== gameId)) {
      throw new Error('管理服务返回了跨 Game 的构建候选');
    }
    return { items, total, limit, offset };
  },

  async startBuild(
    gameId: string,
    input: StartBuildRequest,
    idempotencyKey: string,
    csrfToken: string,
  ): Promise<BuildJob> {
    if (!idempotencyKeyPattern.test(idempotencyKey)) {
      throw new Error('Idempotency-Key 必须是 16–128 个安全字符');
    }
    const job = parseBuildJob(
      await request(`/api/v1/games/${encodeURIComponent(gameId)}/builds`, {
        method: 'POST',
        body: input,
        csrfToken,
        idempotencyKey,
      }),
    );
    if (
      job.game_id !== gameId ||
      job.content_id !== input.content_id ||
      job.revision !== input.revision ||
      job.body_hash !== input.body_hash
    ) {
      throw new Error('管理服务返回了超出构建请求范围的任务');
    }
    return job;
  },

  /** Reads a job and requires every immutable identity field to match the job being tracked. */
  async getBuildJob(gameId: string, expected: BuildJob): Promise<BuildJob> {
    const job = parseBuildJob(
      await request(
        `/api/v1/games/${encodeURIComponent(gameId)}/builds/${encodeURIComponent(expected.job_id)}`,
      ),
    );
    if (
      job.game_id !== gameId ||
      job.job_id !== expected.job_id ||
      job.content_id !== expected.content_id ||
      job.revision !== expected.revision ||
      job.body_hash !== expected.body_hash ||
      job.release_id !== expected.release_id ||
      job.release_version !== expected.release_version
    ) {
      throw new Error('管理服务返回了超出读取范围的构建任务');
    }
    return job;
  },
};
