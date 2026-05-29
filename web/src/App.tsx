import { Layout, Typography, Space, Tag, Card } from 'antd';
import { Bubble } from '@ant-design/x';

const { Header, Content } = Layout;
const { Title, Paragraph, Text } = Typography;

/**
 * App is the T0.7 skeleton: a minimal AntD layout that proves the build
 * system, AntD, and `@ant-design/x` are all wired up correctly.
 *
 * Real shell + routes + chat surface land in Iter-5 / Iter-6:
 *   - T5.5  routes/pages, react-router setup
 *   - T5.6  Markdown / syntax highlight / diff rendering
 *   - T6.*  tool-call viz, file tree, todo list, trace, settings
 *
 * Until then, rendering this component is the success criterion for
 * `pnpm dev` / `pnpm build`.
 */
export default function App() {
  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header style={{ background: '#fff', borderBottom: '1px solid #f0f0f0' }}>
        <Space size="middle" align="center" style={{ height: '100%' }}>
          <Title level={4} style={{ margin: 0 }}>
            mini-agent
          </Title>
          <Tag color="blue">skeleton</Tag>
          <Text type="secondary">T0.7 · web UI scaffold</Text>
        </Space>
      </Header>

      <Content style={{ padding: 24, maxWidth: 960, margin: '0 auto', width: '100%' }}>
        <Card>
          <Paragraph>
            This is the placeholder shell. The real chat surface, tool
            visualizations, file tree, and trace viewer will land in
            Iter-5 / Iter-6.
          </Paragraph>
          <Paragraph>
            For now, rendering the <Text code>@ant-design/x</Text> Bubble
            below verifies that the design-system bundle resolves and the
            React tree is healthy:
          </Paragraph>

          <Space direction="vertical" size={12} style={{ width: '100%', marginTop: 16 }}>
            <Bubble
              placement="start"
              content="Hello — mini-agent web UI skeleton is live."
            />
            <Bubble
              placement="end"
              content="Backend wiring (gin REST + SSE) lands in T5.1–T5.3."
            />
          </Space>
        </Card>
      </Content>
    </Layout>
  );
}
