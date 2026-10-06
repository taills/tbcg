import type { ReactElement } from 'react';

type Json = Record<string, unknown>;

interface EditorProps {
  value: Json;
  onChange: (next: Json) => void;
  disabled: boolean;
}

const input =
  'w-full rounded border border-slate-300 px-2 py-1 text-sm focus:outline-none focus:ring-2 focus:ring-teal-700';

function section(value: Json, key: 'public' | 'server'): Json {
  const raw = value[key];
  return typeof raw === 'object' && raw !== null && !Array.isArray(raw) ? (raw as Json) : {};
}

function withSection(value: Json, key: 'public' | 'server', next: Json): Json {
  return { ...value, [key]: next };
}

/** NPC 对话树可视化编辑：与表单共享同一定义对象，保存后仍走 Schema 校验与版本流程。 */
export function DialogTreeEditor({ value, onChange, disabled }: EditorProps): ReactElement {
  const server = section(value, 'server');
  const nodes = Array.isArray(server.nodes) ? (server.nodes as Json[]) : [];
  const update = (index: number, node: Json): void => {
    const nextNodes = nodes.map((current, i) => (i === index ? node : current));
    onChange(withSection(value, 'server', { ...server, nodes: nextNodes }));
  };
  return (
    <div className="space-y-3" aria-label="对话树">
      <p className="text-xs text-slate-500">起始节点：{String(server.start_node ?? '')}</p>
      {nodes.map((node, index) => {
        const options = Array.isArray(node.options) ? (node.options as Json[]) : [];
        return (
          <div className="rounded-md border border-slate-200 p-3" key={String(node.node_id)}>
            <p className="text-xs font-semibold text-teal-800">节点 {String(node.node_id)}</p>
            <textarea
              aria-label={`节点 ${String(node.node_id)} 文本`}
              className={input}
              disabled={disabled}
              onChange={(event) => update(index, { ...node, text: event.currentTarget.value })}
              value={String(node.text ?? '')}
            />
            <ul className="mt-2 space-y-1">
              {options.map((option, optionIndex) => {
                const action = (option.action ?? {}) as Json;
                return (
                  <li className="flex items-center gap-2 text-sm" key={String(option.option_id)}>
                    <input
                      aria-label={`选项 ${String(option.option_id)}`}
                      className={input}
                      disabled={disabled}
                      onChange={(event) => {
                        const nextOptions = options.map((current, i) =>
                          i === optionIndex
                            ? { ...current, text: event.currentTarget.value }
                            : current,
                        );
                        update(index, { ...node, options: nextOptions });
                      }}
                      value={String(option.text ?? '')}
                    />
                    <span className="shrink-0 rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-700">
                      {String(action.kind ?? '')}
                      {action.next_node !== undefined ? ` → ${String(action.next_node)}` : ''}
                      {action.target_id !== undefined ? ` · ${String(action.target_id)}` : ''}
                    </span>
                  </li>
                );
              })}
            </ul>
          </div>
        );
      })}
    </div>
  );
}

/** 任务阶段编辑：阶段描述（公开）与目标（服务端）并排展示，数量保持一致。 */
export function QuestStageEditor({ value, onChange, disabled }: EditorProps): ReactElement {
  const publicPart = section(value, 'public');
  const server = section(value, 'server');
  const descriptions = Array.isArray(publicPart.stage_descriptions)
    ? (publicPart.stage_descriptions as string[])
    : [];
  const stages = Array.isArray(server.stages) ? (server.stages as Json[]) : [];
  return (
    <ol className="space-y-2" aria-label="任务阶段">
      {stages.map((stage, index) => {
        const objective = (stage.objective ?? {}) as Json;
        return (
          <li className="rounded-md border border-slate-200 p-3" key={index}>
            <p className="text-xs font-semibold text-teal-800">
              阶段 {index + 1}：{String(objective.kind ?? '')} ×{String(objective.count ?? '')}（
              {String(objective.progress_scope ?? '')}）
            </p>
            <input
              aria-label={`阶段 ${index + 1} 描述`}
              className={input}
              disabled={disabled}
              onChange={(event) => {
                const next = descriptions.map((text, i) =>
                  i === index ? event.currentTarget.value : text,
                );
                onChange(withSection(value, 'public', { ...publicPart, stage_descriptions: next }));
              }}
              value={descriptions[index] ?? ''}
            />
            <input
              aria-label={`阶段 ${index + 1} 数量`}
              className={`${input} mt-1 w-24`}
              disabled={disabled}
              min={1}
              onChange={(event) => {
                const count = Number.parseInt(event.currentTarget.value, 10);
                const next = stages.map((current, i) =>
                  i === index
                    ? {
                        ...current,
                        objective: { ...objective, count: Number.isFinite(count) ? count : 1 },
                      }
                    : current,
                );
                onChange(withSection(value, 'server', { ...server, stages: next }));
              }}
              type="number"
              value={Number(objective.count ?? 1)}
            />
          </li>
        );
      })}
    </ol>
  );
}

/** 副本阶段图：阶段名称（公开）→ 遭遇引用（服务端），只读展示顺序并允许改名。 */
export function DungeonStageEditor({ value, onChange, disabled }: EditorProps): ReactElement {
  const publicPart = section(value, 'public');
  const server = section(value, 'server');
  const names = Array.isArray(publicPart.stage_names) ? (publicPart.stage_names as string[]) : [];
  const stages = Array.isArray(server.stages) ? (server.stages as Json[]) : [];
  return (
    <div className="flex flex-wrap items-center gap-2" aria-label="副本阶段图">
      {stages.map((stage, index) => (
        <div className="flex items-center gap-2" key={index}>
          <div className="rounded-md border border-teal-300 bg-teal-50 p-2">
            <input
              aria-label={`副本阶段 ${index + 1} 名称`}
              className={input}
              disabled={disabled}
              onChange={(event) => {
                const next = names.map((text, i) =>
                  i === index ? event.currentTarget.value : text,
                );
                onChange(withSection(value, 'public', { ...publicPart, stage_names: next }));
              }}
              value={names[index] ?? ''}
            />
            <p className="mt-1 text-xs text-slate-600">{String(stage.encounter_id ?? '')}</p>
          </div>
          {index < stages.length - 1 && <span aria-hidden="true">→</span>}
        </div>
      ))}
    </div>
  );
}
