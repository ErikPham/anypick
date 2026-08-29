import { codingPlanProviders } from './providers/coding-plans.mjs';
import { inferenceCloudProviders } from './providers/inference-clouds.mjs';

/**
 * @typedef {object} PluginContext
 * @property {(provider: object) => void} registerCompatibleProvider
 */

const modelVendorProviders = [
  {
    id: 'kimi',
    name: 'Kimi / Moonshot AI',
    shortName: 'Kimi',
    description: 'Kimi Open Platform API keys and Kimi Code membership keys',
    defaultProxyPort: 4141,
    models: ['k3', 'k3-256k', 'kimi-for-coding', 'kimi-for-coding-highspeed', 'kimi-k3'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Open Platform API key',
        profiles: [
          { id: 'global', openaiBase: 'https://api.moonshot.ai/v1' },
          { id: 'china', openaiBase: 'https://api.moonshot.cn/v1' },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'Kimi Code membership key',
        profiles: [
          {
            id: 'kimi-code',
            openaiBase: 'https://api.kimi.com/coding/v1',
            anthropicBase: 'https://api.kimi.com/coding',
          },
        ],
      },
    ],
    quotaErrorPatterns: [
      'usage limit',
      'membership benefits',
      'subscription.*access',
      'engine is currently overloaded',
    ],
  },
  {
    id: 'glm',
    name: 'Zhipu GLM / Z.AI',
    shortName: 'GLM',
    description: 'Zhipu/Z.AI API keys and GLM Coding Plan keys',
    defaultProxyPort: 4142,
    models: ['glm-5.2', 'glm-5', 'glm-4.7'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Platform API key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://api.z.ai/api/paas/v4',
            anthropicBase: 'https://api.z.ai/api/anthropic',
            anthropicAuth: 'bearer',
          },
          {
            id: 'china',
            openaiBase: 'https://open.bigmodel.cn/api/paas/v4',
            anthropicBase: 'https://open.bigmodel.cn/api/anthropic',
            anthropicAuth: 'bearer',
          },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'GLM Coding Plan key',
        profiles: [
          {
            id: 'global-coding',
            openaiBase: 'https://api.z.ai/api/coding/paas/v4',
            anthropicBase: 'https://api.z.ai/api/anthropic',
            anthropicAuth: 'bearer',
          },
          {
            id: 'china-coding',
            openaiBase: 'https://open.bigmodel.cn/api/coding/paas/v4',
            anthropicBase: 'https://open.bigmodel.cn/api/anthropic',
            anthropicAuth: 'bearer',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['余额不足', '套餐.*额度', 'quota.*exceeded', 'usage.*limit'],
  },
  {
    id: 'minimax',
    name: 'MiniMax',
    description: 'MiniMax pay-as-you-go API and Token Plan keys',
    defaultProxyPort: 4143,
    models: ['MiniMax-M2.7', 'MiniMax-M2.7-highspeed', 'MiniMax-M2.5'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Pay-as-you-go API key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://api.minimax.io/v1',
            anthropicBase: 'https://api.minimax.io/anthropic',
          },
          {
            id: 'china',
            openaiBase: 'https://api.minimaxi.com/v1',
            anthropicBase: 'https://api.minimaxi.com/anthropic',
          },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'MiniMax Token Plan key',
        profiles: [
          {
            id: 'global',
            openaiBase: 'https://api.minimax.io/v1',
            anthropicBase: 'https://api.minimax.io/anthropic',
          },
          {
            id: 'china',
            openaiBase: 'https://api.minimaxi.com/v1',
            anthropicBase: 'https://api.minimaxi.com/anthropic',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['token plan.*quota', 'credits.*exhausted', 'insufficient.*balance'],
  },
  {
    id: 'mimo',
    name: 'Xiaomi MiMo',
    shortName: 'MiMo',
    description: 'Xiaomi MiMo API and Token Plan keys',
    defaultProxyPort: 4144,
    models: ['mimo-v2.5-pro'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Pay-as-you-go API key',
        profiles: [
          {
            id: 'platform',
            openaiBase: 'https://api.xiaomimimo.com/v1',
            anthropicBase: 'https://api.xiaomimimo.com/anthropic',
            anthropicAuth: 'bearer',
          },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'MiMo Token Plan key',
        profiles: [
          {
            id: 'china',
            openaiBase: 'https://token-plan-cn.xiaomimimo.com/v1',
            anthropicBase: 'https://token-plan-cn.xiaomimimo.com/anthropic',
            anthropicAuth: 'bearer',
          },
          {
            id: 'singapore',
            openaiBase: 'https://token-plan-sgp.xiaomimimo.com/v1',
            anthropicBase: 'https://token-plan-sgp.xiaomimimo.com/anthropic',
            anthropicAuth: 'bearer',
          },
          {
            id: 'europe',
            openaiBase: 'https://token-plan-ams.xiaomimimo.com/v1',
            anthropicBase: 'https://token-plan-ams.xiaomimimo.com/anthropic',
            anthropicAuth: 'bearer',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['token plan.*quota', 'usage.*limit', 'subscription.*expired'],
  },
  {
    id: 'deepseek',
    name: 'DeepSeek',
    description: 'DeepSeek OpenAI- and Anthropic-compatible API keys',
    defaultProxyPort: 4145,
    models: ['deepseek-v4-pro', 'deepseek-v4-flash'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Platform API key',
        profiles: [
          {
            id: 'platform',
            openaiBase: 'https://api.deepseek.com',
            anthropicBase: 'https://api.deepseek.com/anthropic',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['insufficient.*balance', 'quota.*exceeded'],
  },
  {
    id: 'qwen',
    name: 'Alibaba Cloud Model Studio / Qwen',
    shortName: 'Qwen',
    description: 'Qwen pay-as-you-go API, Coding Plan, and Token Plan keys',
    defaultProxyPort: 4146,
    models: ['qwen3.7-plus', 'qwen3.6-plus', 'qwen3-coder-next', 'qwen3-coder-plus'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Model Studio API key',
        profiles: [
          {
            id: 'china',
            openaiBase: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
            anthropicBase: 'https://dashscope.aliyuncs.com/apps/anthropic',
          },
          {
            id: 'singapore',
            openaiBase: 'https://dashscope-intl.aliyuncs.com/compatible-mode/v1',
            anthropicBase: 'https://dashscope-intl.aliyuncs.com/apps/anthropic',
          },
          {
            id: 'us',
            openaiBase: 'https://dashscope-us.aliyuncs.com/compatible-mode/v1',
            anthropicBase: 'https://dashscope-us.aliyuncs.com/apps/anthropic',
          },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'Model Studio coding subscription key',
        profiles: [
          {
            id: 'coding-china',
            openaiBase: 'https://coding.dashscope.aliyuncs.com/v1',
            anthropicBase: 'https://coding.dashscope.aliyuncs.com/apps/anthropic',
          },
          {
            id: 'coding-global',
            openaiBase: 'https://coding-intl.dashscope.aliyuncs.com/v1',
            anthropicBase: 'https://coding-intl.dashscope.aliyuncs.com/apps/anthropic',
          },
          {
            id: 'token-plan-china',
            openaiBase: 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
            anthropicBase: 'https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['quota.*exceeded', 'subscription.*expired', '余额不足', '套餐.*额度'],
  },
];

export default {
  /** @param {PluginContext} context */
  activate(context) {
    for (const provider of [
      ...modelVendorProviders,
      ...codingPlanProviders,
      ...inferenceCloudProviders,
    ]) {
      context.registerCompatibleProvider(provider);
    }
  },
};
