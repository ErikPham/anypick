/**
 * Multi-model gateways and inference clouds with documented OpenAI-compatible
 * credentials. `nativeResponses` is enabled only when the vendor documents the
 * endpoint; otherwise AnyPick translates Responses through Chat Completions.
 */
export const inferenceCloudProviders = [
  {
    id: 'openrouter',
    name: 'OpenRouter',
    description: 'OpenRouter multi-model API key with native Messages and Responses APIs',
    defaultProxyPort: 4147,
    models: ['openrouter/auto'],
    credentials: [
      {
        kind: 'api-key',
        label: 'OpenRouter API key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://openrouter.ai/api/v1',
            nativeResponses: true,
            anthropicBase: 'https://openrouter.ai/api',
            anthropicAuth: 'bearer',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['insufficient.*credits', 'payment_required', 'token_limit_exceeded'],
  },
  {
    id: 'groq-cloud',
    name: 'GroqCloud',
    shortName: 'Groq',
    description: 'GroqCloud fast inference API with native OpenAI Responses support',
    defaultProxyPort: 4148,
    models: ['openai/gpt-oss-120b', 'openai/gpt-oss-20b', 'qwen/qwen3.6-27b'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Groq API key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://api.groq.com/openai/v1',
            nativeResponses: true,
          },
        ],
      },
    ],
    quotaErrorPatterns: ['rate_limit_exceeded', 'quota.*exceeded'],
  },
  {
    id: 'mistral',
    name: 'Mistral AI',
    description: 'Mistral La Plateforme API keys for agentic and coding models',
    defaultProxyPort: 4149,
    models: ['mistral-medium-latest', 'mistral-small-latest', 'mistral-large-latest'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Mistral API key',
        profiles: [{ id: 'global', openaiBase: 'https://api.mistral.ai/v1' }],
      },
    ],
    quotaErrorPatterns: ['insufficient.*balance', 'quota.*exceeded'],
  },
  {
    id: 'together-ai',
    name: 'Together AI',
    shortName: 'Together',
    description: 'Together AI serverless and dedicated inference API keys',
    defaultProxyPort: 4150,
    models: ['MiniMaxAI/MiniMax-M3', 'Qwen/Qwen3.5-9B', 'openai/gpt-oss-120b'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Together API key',
        profiles: [{ id: 'global', openaiBase: 'https://api.together.ai/v1' }],
      },
    ],
    quotaErrorPatterns: ['insufficient.*credits', 'quota.*exceeded'],
  },
  {
    id: 'fireworks',
    name: 'Fireworks AI',
    description: 'Fireworks serverless API with native OpenAI Responses and Anthropic Messages',
    defaultProxyPort: 4151,
    models: ['accounts/fireworks/models/kimi-k2p5', 'accounts/fireworks/models/deepseek-v3p1'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Fireworks API key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://api.fireworks.ai/inference/v1',
            nativeResponses: true,
            anthropicBase: 'https://api.fireworks.ai/inference',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['insufficient.*credits', 'quota.*exceeded'],
  },
  {
    id: 'cerebras',
    name: 'Cerebras Inference',
    shortName: 'Cerebras',
    description: 'Cerebras high-speed OpenAI-compatible inference API',
    defaultProxyPort: 4152,
    models: ['gpt-oss-120b', 'zai-glm-4.7'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Cerebras API key',
        profiles: [{ id: 'global', openaiBase: 'https://api.cerebras.ai/v1' }],
      },
    ],
    quotaErrorPatterns: ['quota.*exceeded', 'rate_limit_exceeded'],
  },
  {
    id: 'siliconflow',
    name: 'SiliconFlow',
    description: 'SiliconFlow multi-model OpenAI-compatible inference API',
    defaultProxyPort: 4153,
    models: ['deepseek-ai/DeepSeek-V4-Pro', 'zai-org/GLM-5.1', 'Qwen/Qwen3-Coder-30B-A3B-Instruct'],
    credentials: [
      {
        kind: 'api-key',
        label: 'SiliconFlow API key',
        profiles: [{ id: 'global', openaiBase: 'https://api.siliconflow.com/v1' }],
      },
    ],
    quotaErrorPatterns: ['余额不足', 'insufficient.*balance', 'quota.*exceeded'],
  },
  {
    id: 'nvidia-nim',
    name: 'NVIDIA NIM',
    shortName: 'NVIDIA',
    description: 'NVIDIA hosted NIM OpenAI-compatible inference API',
    defaultProxyPort: 4154,
    models: ['z-ai/glm5.1', 'qwen/qwen3.5-122b-a10b'],
    credentials: [
      {
        kind: 'api-key',
        label: 'NVIDIA API key',
        profiles: [{ id: 'hosted', openaiBase: 'https://integrate.api.nvidia.com/v1' }],
      },
    ],
    quotaErrorPatterns: ['payment required', 'quota.*exceeded', 'rate.*limit'],
  },
];
