/** Coding-plan products whose keys and billing endpoints differ from pay-go. */
export const codingPlanProviders = [
  {
    id: 'ark',
    name: 'Volcengine Ark / BytePlus ModelArk',
    shortName: 'Ark',
    description: 'Ark pay-as-you-go APIs and separate Volcengine/BytePlus Coding Plan keys',
    defaultProxyPort: 4155,
    models: ['ark-code-latest', 'doubao-seed-2.0-code'],
    credentials: [
      {
        kind: 'api-key',
        label: 'Ark platform API key',
        profiles: [
          {
            id: 'china',
            label: 'Volcengine China',
            openaiBase: 'https://ark.cn-beijing.volces.com/api/v3',
          },
          {
            id: 'global',
            label: 'BytePlus Singapore',
            openaiBase: 'https://ark.ap-southeast.bytepluses.com/api/v3',
          },
        ],
      },
      {
        kind: 'subscription-key',
        label: 'Ark Coding Plan key',
        profiles: [
          {
            id: 'china-coding',
            label: 'Volcengine Coding Plan',
            openaiBase: 'https://ark.cn-beijing.volces.com/api/coding/v3',
            anthropicBase: 'https://ark.cn-beijing.volces.com/api/coding',
            anthropicAuth: 'bearer',
          },
          {
            id: 'global-coding',
            label: 'BytePlus Coding Plan',
            openaiBase: 'https://ark.ap-southeast.bytepluses.com/api/coding/v3',
            anthropicBase: 'https://ark.ap-southeast.bytepluses.com/api/coding',
            anthropicAuth: 'bearer',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['coding plan.*quota', '套餐.*额度', '余额不足', 'quota.*exceeded'],
  },
  {
    id: 'tokenhub',
    name: 'Tencent Cloud TokenHub',
    shortName: 'TokenHub',
    description: 'Tencent TokenHub pay-go API and separate Coding/Token Plan keys',
    defaultProxyPort: 4156,
    models: ['glm-5', 'kimi-k2.5', 'minimax-m2.5'],
    credentials: [
      {
        kind: 'api-key',
        label: 'TokenHub API key',
        profiles: [{ id: 'paygo', openaiBase: 'https://tokenhub.tencentmaas.com/v1' }],
      },
      {
        kind: 'subscription-key',
        label: 'TokenHub Coding Plan key',
        profiles: [
          {
            id: 'coding-plan',
            openaiBase: 'https://api.lkeap.cloud.tencent.com/plan/v3',
            anthropicBase: 'https://api.lkeap.cloud.tencent.com/plan/anthropic',
            anthropicAuth: 'bearer',
          },
        ],
      },
    ],
    quotaErrorPatterns: ['coding plan.*quota', 'token plan.*quota', '套餐.*额度', '余额不足'],
  },
];
