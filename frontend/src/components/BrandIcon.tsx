export type BrandName = 'Claude Code' | 'Codex' | 'DSH' | 'ACP' | 'MCP' | 'Magpie';

const sources: Record<Exclude<BrandName, 'Magpie'>, string> = {
  'Claude Code': '/logos/claude.svg',
  Codex: '/logos/codex.svg',
  DSH: '/logos/deepseek.png',
  ACP: '/logos/acp.png',
  MCP: '/logos/mcp.svg',
};

// Official assets: ACP registry (Claude/Codex), deepseek.com, ACP and MCP official sites.
// Magpie has no published official mark, so it uses a plain text wordmark instead.
export function BrandIcon({ name, size = 18 }: { name: BrandName; size?: number }) {
  if (name === 'Magpie') return <span className="magpie-wordmark" style={{ fontSize: Math.max(8, size * 0.42) }}>Magpie</span>;
  const monochrome = name === 'Claude Code' || name === 'Codex' || name === 'ACP';
  return <img className={`brand-icon ${monochrome ? 'brand-icon-monochrome' : ''}`} src={sources[name]} width={size} height={size} alt="" aria-hidden="true" draggable={false} />;
}
