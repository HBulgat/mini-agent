import { describe, it, expect, vi } from 'vitest';

// We deliberately mock the design-system packages instead of pulling
// them in for real:
//   - antd v5's ESM build uses bare directory imports like
//     `from 'antd/es/version'` that Node's native ESM resolver refuses,
//     and as of Vitest 2.1 there's no inline/transform setting that
//     reliably overrides that resolution path under JSDOM.
//   - The T0.7 test only cares that the build pipeline + React tree are
//     healthy, not that AntD itself renders. Real component-level UI
//     tests come in T6.7 once we have visual fixtures + Storybook.
//
// `vi.mock` calls are hoisted by Vitest above all imports, so the stubs
// take effect for App.tsx's transitive imports too.

// Strip AntD-specific props that aren't valid HTML attributes — passing
// them straight to a <span> trips React's "unknown DOM property" warning
// and clutters the test log.
const ANTD_ONLY_PROPS = new Set([
  'code',
  'mark',
  'keyboard',
  'underline',
  'strong',
  'italic',
  'delete',
  'disabled',
  'ellipsis',
  'level',
  'placement',
  'size',
  'direction',
  'align',
  'justify',
  'gutter',
  'wrap',
  'split',
  'bordered',
  'hoverable',
  'cssVar',
  'token',
  'theme',
]);

function cleanProps<T extends Record<string, unknown>>(props: T): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(props)) {
    if (!ANTD_ONLY_PROPS.has(k)) out[k] = v;
  }
  return out;
}

vi.mock('antd', () => {
  const passthrough =
    (tag: string) =>
    ({ children, ...rest }: { children?: React.ReactNode } & Record<string, unknown>) => {
      const React = require('react');
      return React.createElement(tag, cleanProps(rest), children);
    };
  const Title = ({ children }: { children?: React.ReactNode }) => {
    const React = require('react');
    return React.createElement('h4', null, children);
  };
  return {
    Layout: Object.assign(passthrough('div'), {
      Header: passthrough('header'),
      Content: passthrough('main'),
    }),
    Typography: { Title, Paragraph: passthrough('p'), Text: passthrough('span') },
    Space: passthrough('div'),
    Tag: passthrough('span'),
    Card: passthrough('section'),
  };
});

vi.mock('@ant-design/x', () => ({
  Bubble: ({ content }: { content: string }) => {
    const React = require('react');
    return React.createElement('div', { 'data-testid': 'bubble' }, content);
  },
}));

// Imports that follow run with the mocks in place.
import { render, screen } from '@testing-library/react';
import App from '../App';

describe('<App /> skeleton', () => {
  it('mounts and renders the skeleton header', () => {
    render(<App />);
    expect(screen.getByRole('heading', { name: /mini-agent/i })).toBeInTheDocument();
    // "skeleton" appears in the Tag and in body copy; just assert it's
    // somewhere in the rendered tree without pinning to a single node.
    expect(screen.getAllByText(/skeleton/i).length).toBeGreaterThan(0);
  });

  it('renders both placeholder bubbles via @ant-design/x', () => {
    render(<App />);
    const bubbles = screen.getAllByTestId('bubble');
    expect(bubbles).toHaveLength(2);
    expect(bubbles[0]).toHaveTextContent(/Hello/);
  });
});
