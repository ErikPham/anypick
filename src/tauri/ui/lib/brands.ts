/**
 * Provider / app identity, ported from `src/macos/BrandIcons.swift`.
 *
 * macOS keeps this in one file for a stated reason: *"Chrome uses SF Symbols;
 * only this view loads brand files."* Every provider has a bundled mark, and
 * when one is absent the row wears a provider-specific **semantic** glyph rather
 * than a generic one. This module is that one file for the webview.
 *
 * The Tauri app had neither half. It had ~36 generic `Symbol` paths and a
 * blanket fallback to a square, so twenty-odd supported apps all rendered the
 * same glyph — which is what made the Apps list unreadable as a list of apps.
 */

/** Candidate brand files per provider, in preference order. */
const BRAND_FILES: Record<string, string[]> = {
  // Orange burst first: it is brand-coloured in both appearances, and the white
  // mark is the fallback.
  claude: ['claude-mark-orange.svg', 'claude.svg'],
  anthropic: ['claude-mark-orange.svg', 'claude.svg'],
  claudecode: ['claude-mark-orange.svg', 'claude.svg'],
  codex: ['openai.svg'],
  codexcli: ['openai.svg'],
  openai: ['openai.svg'],
  gemini: ['gemini-icon-2025.svg', 'googlegemini.svg'],
  google: ['gemini-icon-2025.svg', 'googlegemini.svg'],
  googlegemini: ['gemini-icon-2025.svg', 'googlegemini.svg'],
  geminicli: ['gemini-icon-2025.svg', 'googlegemini.svg'],
  antigravity: ['gemini-icon-2025.svg', 'googlegemini.svg'],
  kiro: ['kiro.svg'],
  personalbuilder: ['kiro.svg'],
  workbuddy: ['workbuddy-mark.png'],
  codebuddy: ['workbuddy-mark.png'],
  openrouter: ['openrouter.svg'],
  grok: ['grok.svg'],
  xai: ['grok.svg'],
  opencode: ['opencode.svg'],
  // Zen and Go rows share OpenCode's own mark: they are OpenCode products, not
  // third-party vendors.
  opencodezen: ['opencode.svg'],
  opencodego: ['opencode.svg'],
  commandcode: ['commandcode.svg'],
  cline: ['cline.svg'],
  deepseek: ['deepseek.svg'],
  kimi: ['kimi.svg'],
  moonshot: ['kimi.svg'],
  zai: ['zai.svg'],
  glm: ['zai.svg'],
  zaiglm: ['zai.svg'],
  qwen: ['qwen.svg'],
  dashscope: ['qwen.svg'],
  mistral: ['mistral.svg'],
  mimo: ['mimo.svg'],
  xiaomi: ['mimo.svg'],
  minimax: ['minimax.svg'],
  doubao: ['doubao.svg'],
  bytedance: ['doubao.svg'],
  hunyuan: ['hunyuan.svg'],
  modelscope: ['modelscope.svg'],
  nvidia: ['nvidia.svg'],
  nvidianim: ['nvidia.svg'],
  // Deliberately does not cover `zai`: that is Z.ai's API-key provider, and the
  // ZCode app icon is not the Z.ai logo, so an account row would be wearing the
  // wrong brand.
  zcode: ['zcode-app.png'],
  // DeepSeek's harness CLI: no mark of its own, wears the DeepSeek brand.
  dsh: ['deepseek.svg'],
};

/**
 * `fullColorMarks`. Only these render with their own colours.
 *
 * Every other SVG is a white or black single fill. Shown raw it is invisible on
 * light and mud on dark, so those tint with the label colour instead. This set
 * is kept in step with `assets/icons/` by `contract-drift` in the test suite.
 */
const FULL_COLOR_MARKS = new Set([
  'claude-mark-orange.svg',
  'deepseek.svg',
  'doubao.svg',
  'commandcode.svg',
  'gemini-icon-2025.svg',
  'hunyuan.svg',
  'mimo.svg',
  'minimax.svg',
  'mistral.svg',
  'modelscope.svg',
  'nvidia.svg',
  'qwen.svg',
]);

/** `normalize`. A serialized ref or a namespaced id reduced to its letters. */
export function normalizeBrand(value: string): string {
  const stripped = value.startsWith('provider/') ? value.slice('provider/'.length) : value;
  const provider = stripped.split(':')[0] ?? stripped;
  return provider.toLowerCase().replace(/[^a-z0-9]/g, '');
}

/** The brand file to load, or null when this provider has no bundled mark. */
export function brandFileFor(provider: string): string | null {
  return BRAND_FILES[normalizeBrand(provider)]?.[0] ?? null;
}

/** Whether the mark carries its own colours, or must tint with the label. */
export function rendersOriginal(file: string): boolean {
  return file.endsWith('.png') || FULL_COLOR_MARKS.has(file);
}

/**
 * `fallbackSymbol`. What an unbundled provider wears.
 *
 * The point of the table is that no two rows end up looking alike: "a provider
 * with no mark yet still gets a provider-specific semantic glyph instead of
 * making every provider look like a server."
 */
const FALLBACK_SYMBOL: Record<string, string> = {
  claude: 'brain.head.profile',
  anthropic: 'brain.head.profile',
  claudecode: 'brain.head.profile',
  codex: 'terminal',
  codexcli: 'terminal',
  openai: 'terminal',
  gemini: 'sparkle',
  google: 'sparkle',
  googlegemini: 'sparkle',
  geminicli: 'sparkle',
  antigravity: 'sparkle',
  googleaipro: 'sparkle',
  kiro: 'shippingbox',
  personalbuilder: 'shippingbox',
  openrouter: 'network',
  grok: 'sparkles',
  xai: 'sparkles',
  opencode: 'chevron.left.forwardslash.chevron.right',
  opencodezen: 'chevron.left.forwardslash.chevron.right',
  opencodego: 'chevron.left.forwardslash.chevron.right',
  commandcode: 'terminal',
  cline: 'creditcard',
  deepseek: 'bubble.left.and.bubble.right',
  kimi: 'moon',
  moonshot: 'moon',
  zai: 'z.circle',
  glm: 'z.circle',
  zaiglm: 'z.circle',
  zcode: 'curlybraces',
  qwen: 'character.textbox',
  dashscope: 'character.textbox',
  mistral: 'wind',
  mimo: 'circle.grid.3x3',
  xiaomi: 'circle.grid.3x3',
  minimax: 'waveform.path',
  doubao: 'rectangle.3.group',
  bytedance: 'rectangle.3.group',
  hunyuan: 'circle.hexagongrid',
  modelscope: 'square.stack.3d.up',
  nvidia: 'cpu',
  nvidianim: 'cpu',
  dsh: 'terminal',
  pi: 'terminal',
  omp: 'keyboard',
  proxyhub: 'point.3.connected.trianglepath.dotted',
  hub: 'point.3.connected.trianglepath.dotted',
  cloudflare: 'cloud',
  cloudflareaigateway: 'cloud',
  workbuddy: 'briefcase',
  codebuddy: 'briefcase',
  ark: 'bolt.horizontal.circle',
  volcenginearkbyteplusmodelark: 'bolt.horizontal.circle',
  cerebras: 'waveform.path.ecg',
  fireworks: 'flame',
  groq: 'bolt',
  groqcloud: 'bolt',
  groqcloudnativeresponses: 'bolt',
  siliconflow: 'waveform',
  stepfun: 'figure.walk',
  together: 'person.2',
  togetherai: 'person.2',
  tokenhub: 'key',
  tencentcloudtokenhub: 'key',
};

export function fallbackSymbolFor(provider: string): string {
  return FALLBACK_SYMBOL[normalizeBrand(provider)] ?? 'server.rack';
}

/**
 * `tint`. The semantic colour a monochrome mark or fallback glyph wears.
 *
 * A template mark needs an explicit tint: without one it inherits the row's
 * secondary label colour and every provider collapses into the same grey.
 * Marks that are black-on-light / white-on-dark say exactly that rather than
 * inventing a brand colour — the teal OpenAI tint read as washed out next to
 * the full-colour marks.
 */
export type BrandTint = string | null;

const BRAND_TINT: Record<string, string> = {
  claude: 'rgb(245 102 56)',
  anthropic: 'rgb(245 102 56)',
  claudecode: 'rgb(245 102 56)',
  gemini: 'rgb(140 158 255)',
  google: 'rgb(140 158 255)',
  googlegemini: 'rgb(140 158 255)',
  geminicli: 'rgb(140 158 255)',
  antigravity: 'rgb(140 158 255)',
  googleaipro: 'rgb(140 158 255)',
  deepseek: 'rgb(89 135 255)',
  openrouter: 'rgb(184 117 255)',
};

/** The tint for a provider, or null when the mark is black/white by design. */
export function tintFor(provider: string): BrandTint {
  const key = normalizeBrand(provider);
  return BRAND_TINT[key] ?? null;
}