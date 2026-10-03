import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import App from './App';

describe('App', () => {
  it('renders a minimal admin shell without fabricated content', () => {
    render(<App />);

    expect(screen.getByRole('heading', { name: '管理前端', level: 1 })).toBeInTheDocument();
  });
});
