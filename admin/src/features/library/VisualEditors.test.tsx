import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { DialogTreeEditor, DungeonStageEditor, QuestStageEditor } from './VisualEditors';

describe('visual editors', () => {
  it('edits NPC dialog text and option labels in place', () => {
    const onChange = vi.fn();
    const value = {
      server: {
        start_node: 'root',
        nodes: [
          {
            node_id: 'root',
            text: '你好',
            options: [{ option_id: 'bye', text: '再见', action: { kind: 'close' } }],
          },
        ],
      },
    };
    render(<DialogTreeEditor value={value} onChange={onChange} disabled={false} />);
    fireEvent.change(screen.getByLabelText('节点 root 文本'), { target: { value: '欢迎' } });
    expect(onChange.mock.calls[0]?.[0].server.nodes[0].text).toBe('欢迎');
    fireEvent.change(screen.getByLabelText('选项 bye'), { target: { value: '告辞' } });
    expect(onChange.mock.calls[1]?.[0].server.nodes[0].options[0].text).toBe('告辞');
  });

  it('edits quest stage descriptions and counts with the same definition object', () => {
    const onChange = vi.fn();
    const value = {
      public: { stage_descriptions: ['去练武区'] },
      server: {
        stages: [{ objective: { kind: 'enter_zone', count: 1, progress_scope: 'personal' } }],
      },
    };
    render(<QuestStageEditor value={value} onChange={onChange} disabled={false} />);
    fireEvent.change(screen.getByLabelText('阶段 1 描述'), { target: { value: '前往练武区' } });
    expect(onChange.mock.calls[0]?.[0].public.stage_descriptions[0]).toBe('前往练武区');
    fireEvent.change(screen.getByLabelText('阶段 1 数量'), { target: { value: '3' } });
    expect(onChange.mock.calls[1]?.[0].server.stages[0].objective.count).toBe(3);
  });

  it('renames dungeon stages without changing encounter references', () => {
    const onChange = vi.fn();
    const value = {
      public: { stage_names: ['守卫', '狼王'] },
      server: {
        stages: [{ encounter_id: 'demo:encounter/a' }, { encounter_id: 'demo:encounter/b' }],
      },
    };
    render(<DungeonStageEditor value={value} onChange={onChange} disabled={false} />);
    expect(screen.getByText('demo:encounter/b')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('副本阶段 2 名称'), { target: { value: '巢穴' } });
    expect(onChange.mock.calls[0]?.[0].public.stage_names).toEqual(['守卫', '巢穴']);
    expect(onChange.mock.calls[0]?.[0].server).toBe(value.server);
  });
});
