# Popular providers

Bundled AnyPick plugin for official compatible endpoints. It supports separate
accounts for pay-as-you-go API keys and developer subscription/coding-plan keys.

| Provider | API | Coding subscription |
| --- | --- | --- |
| Kimi | Open Platform | Kimi Code membership |
| GLM | BigModel / Z.AI | GLM Coding Plan |
| MiniMax | Pay-as-you-go | Token Plan |
| MiMo | Pay-as-you-go | Token Plan (China, Singapore, Europe) |
| DeepSeek | Platform API | — |
| Qwen | Model Studio | Coding Plan / Token Plan |
| Ark | Volcengine / BytePlus ModelArk | Coding Plan (China, global) |
| TokenHub | Tencent pay-as-you-go | Coding Plan |
| OpenRouter | Multi-model API | — |
| GroqCloud | Fast inference API | — |
| Mistral | La Plateforme API | — |
| Together | Inference API | — |
| Fireworks | Serverless API | — |
| Cerebras | Inference API | — |
| SiliconFlow | Inference API | — |
| NVIDIA NIM | Hosted NIM API | — |

Subscription credentials are intended only for interactive coding tools where
the provider permits that use. The proxy preserves the real downstream
`User-Agent`; it does not impersonate an approved client or import browser
cookies. API and subscription keys are separate account kinds because vendor
keys and billing endpoints are not interchangeable.

OpenRouter, GroqCloud, and Fireworks declare native Responses support, so Codex
requests retain the vendor's complete Responses surface. Other OpenAI-compatible
backends use AnyPick's Chat Completions translation. Native Anthropic Messages
is used only when the selected credential profile documents it.
