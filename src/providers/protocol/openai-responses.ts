import { randomUUID } from 'node:crypto';
import type { ServerResponse } from 'node:http';
import type { OpenAIChatRequest, OpenAIChatResponse, OpenAIMessage, OpenAITool } from './anthropic';
import { sanitizeJsonSchema } from './json-schema';

export interface OpenAIResponsesRequest {
  model?: string;
  instructions?: string;
  input?: string | Array<Record<string, unknown>>;
  max_output_tokens?: number;
  temperature?: number;
  top_p?: number;
  stream?: boolean;
  tools?: Array<Record<string, unknown>>;
  tool_choice?: unknown;
  reasoning?: { effort?: string; summary?: string; [key: string]: unknown };
  [key: string]: unknown;
}

export interface OpenAIResponsesResponse {
  id: string;
  object: 'response';
  created_at: number;
  status: 'completed' | 'incomplete' | 'failed';
  error: null | { message: string; type: string; code?: string };
  incomplete_details: null | { reason: string };
  model: string;
  output: Array<Record<string, unknown>>;
  parallel_tool_calls: boolean;
  usage: {
    input_tokens: number;
    input_tokens_details: { cached_tokens: number };
    output_tokens: number;
    output_tokens_details: { reasoning_tokens: number };
    total_tokens: number;
  };
}

function id(prefix: string): string {
  return `${prefix}_${randomUUID().replaceAll('-', '').slice(0, 24)}`;
}

function flattenText(value: unknown): string {
  if (typeof value === 'string') {
    return value;
  }
  if (!Array.isArray(value)) {
    return '';
  }
  return value
    .map((part) =>
      part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string'
        ? String((part as { text: string }).text)
        : '',
    )
    .join('');
}

function responseContentToChat(value: unknown): OpenAIMessage['content'] {
  if (typeof value === 'string') {
    return value;
  }
  if (!Array.isArray(value)) {
    return '';
  }
  return value.flatMap((part): Array<{ type: string; text?: string; [key: string]: unknown }> => {
    if (!part || typeof part !== 'object') {
      return [];
    }
    const item = part as Record<string, unknown>;
    if (
      (item.type === 'input_text' || item.type === 'output_text' || item.type === 'text') &&
      typeof item.text === 'string'
    ) {
      return [{ type: 'text', text: item.text }];
    }
    if (item.type === 'input_image') {
      const url = typeof item.image_url === 'string' ? item.image_url : item.url;
      return typeof url === 'string' ? [{ type: 'image_url', image_url: { url } }] : [];
    }
    return [];
  });
}

/** Convert the Responses request used by Codex into OpenAI Chat Completions. */
export function openAIResponsesToChat(
  req: OpenAIResponsesRequest,
  resolvedModel?: string,
): OpenAIChatRequest {
  const messages: OpenAIMessage[] = [];
  if (req.instructions) {
    messages.push({ role: 'system', content: req.instructions });
  }

  let pendingReasoning = '';
  const attachReasoning = (message: OpenAIMessage): OpenAIMessage => {
    if (pendingReasoning) {
      message.reasoning_content = pendingReasoning;
    }
    pendingReasoning = '';
    return message;
  };

  if (typeof req.input === 'string') {
    messages.push({ role: 'user', content: req.input });
  } else {
    for (const item of req.input ?? []) {
      const type = typeof item.type === 'string' ? item.type : undefined;
      if (type === 'reasoning') {
        pendingReasoning = flattenText(item.summary ?? item.content);
        continue;
      }
      if (type === 'function_call') {
        messages.push(
          attachReasoning({
            role: 'assistant',
            content: null,
            tool_calls: [
              {
                id:
                  typeof item.call_id === 'string'
                    ? item.call_id
                    : typeof item.id === 'string'
                      ? item.id
                      : id('call'),
                type: 'function',
                function: {
                  name: typeof item.name === 'string' ? item.name : 'tool',
                  arguments: typeof item.arguments === 'string' ? item.arguments : '{}',
                },
              },
            ],
          }),
        );
        continue;
      }
      if (type === 'function_call_output') {
        if (pendingReasoning) {
          messages.push(attachReasoning({ role: 'assistant', content: null }));
        }
        messages.push({
          role: 'tool',
          tool_call_id:
            typeof item.call_id === 'string'
              ? item.call_id
              : typeof item.id === 'string'
                ? item.id
                : id('call'),
          content: flattenText(item.output),
        });
        continue;
      }
      const rawRole = typeof item.role === 'string' ? item.role : 'user';
      const role: OpenAIMessage['role'] =
        rawRole === 'assistant'
          ? 'assistant'
          : rawRole === 'system' || rawRole === 'developer'
            ? 'system'
            : 'user';
      const message: OpenAIMessage = { role, content: responseContentToChat(item.content) };
      messages.push(role === 'assistant' ? attachReasoning(message) : message);
    }
  }
  if (pendingReasoning) {
    messages.push(attachReasoning({ role: 'assistant', content: null }));
  }
  if (messages.every((message) => message.role === 'system')) {
    messages.push({ role: 'user', content: '' });
  }

  const tools = (req.tools ?? []).flatMap((tool): OpenAITool[] => {
    if (tool.type !== 'function' || typeof tool.name !== 'string') {
      return [];
    }
    return [
      {
        type: 'function',
        function: {
          name: tool.name,
          ...(typeof tool.description === 'string' ? { description: tool.description } : {}),
          parameters: sanitizeJsonSchema(
            tool.parameters &&
              typeof tool.parameters === 'object' &&
              !Array.isArray(tool.parameters)
              ? tool.parameters
              : { type: 'object', properties: {} },
          ),
        },
      },
    ];
  });
  return {
    model: resolvedModel ?? req.model ?? '',
    messages,
    stream: false,
    ...(req.max_output_tokens != null ? { max_tokens: req.max_output_tokens } : {}),
    ...(req.temperature != null ? { temperature: req.temperature } : {}),
    ...(req.top_p != null ? { top_p: req.top_p } : {}),
    ...(req.reasoning ? { reasoning: req.reasoning } : {}),
    ...(tools.length ? { tools } : {}),
    ...(req.tool_choice != null ? { tool_choice: req.tool_choice } : {}),
  };
}

/** Convert one complete Chat Completions result into an OpenAI Responses object. */
export function openAIChatToResponses(
  result: OpenAIChatResponse,
  fallbackModel: string,
): OpenAIResponsesResponse {
  const responseId = result.id?.startsWith('resp_') ? result.id : id('resp');
  const choice = result.choices?.[0];
  const message = choice?.message;
  const output: Array<Record<string, unknown>> = [];
  const reasoning = message?.reasoning_content ?? message?.reasoning;
  if (reasoning) {
    output.push({
      id: id('rs'),
      type: 'reasoning',
      status: 'completed',
      summary: [{ type: 'summary_text', text: reasoning }],
    });
  }
  if (message?.content != null) {
    output.push({
      id: id('msg'),
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{ type: 'output_text', text: message.content, annotations: [], logprobs: [] }],
    });
  }
  for (const call of message?.tool_calls ?? []) {
    output.push({
      id: id('fc'),
      type: 'function_call',
      status: 'completed',
      call_id: call.id,
      name: call.function.name,
      arguments: call.function.arguments,
    });
  }
  if (!output.length) {
    output.push({
      id: id('msg'),
      type: 'message',
      status: 'completed',
      role: 'assistant',
      content: [{ type: 'output_text', text: '', annotations: [], logprobs: [] }],
    });
  }
  const inputTokens = result.usage?.prompt_tokens ?? 0;
  const outputTokens = result.usage?.completion_tokens ?? 0;
  const incomplete = choice?.finish_reason === 'length';
  return {
    id: responseId,
    object: 'response',
    created_at: Math.floor(Date.now() / 1000),
    status: incomplete ? 'incomplete' : 'completed',
    error: null,
    incomplete_details: incomplete ? { reason: 'max_output_tokens' } : null,
    model: result.model ?? fallbackModel,
    output,
    parallel_tool_calls: true,
    usage: {
      input_tokens: inputTokens,
      input_tokens_details: { cached_tokens: 0 },
      output_tokens: outputTokens,
      output_tokens_details: { reasoning_tokens: 0 },
      total_tokens: result.usage?.total_tokens ?? inputTokens + outputTokens,
    },
  };
}

function writeSse(res: ServerResponse, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
}

/** Emit the complete Responses SSE lifecycle Codex expects. */
export function writeOpenAIResponsesStream(
  res: ServerResponse,
  response: OpenAIResponsesResponse,
): void {
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'cache-control': 'no-cache, no-transform',
    connection: 'keep-alive',
  });
  let sequence = 0;
  const emit = (type: string, payload: Record<string, unknown>) =>
    writeSse(res, type, { type, sequence_number: sequence++, ...payload });
  const inProgress = { ...response, status: 'in_progress', output: [], usage: null };
  emit('response.created', { response: inProgress });
  emit('response.in_progress', { response: inProgress });

  response.output.forEach((item, outputIndex) => {
    const type = item.type;
    const added: Record<string, unknown> = { ...item, status: 'in_progress' };
    if (type === 'reasoning') {
      added.summary = [];
    }
    if (type === 'message') {
      added.content = [];
    }
    if (type === 'function_call') {
      added.arguments = '';
    }
    emit('response.output_item.added', { output_index: outputIndex, item: added });

    if (type === 'reasoning') {
      const summary = Array.isArray(item.summary) ? item.summary : [];
      summary.forEach((part, summaryIndex) => {
        const text =
          part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string'
            ? String((part as { text: string }).text)
            : '';
        emit('response.reasoning_summary_part.added', {
          item_id: item.id,
          output_index: outputIndex,
          summary_index: summaryIndex,
          part: { type: 'summary_text', text: '' },
        });
        if (text) {
          emit('response.reasoning_summary_text.delta', {
            item_id: item.id,
            output_index: outputIndex,
            summary_index: summaryIndex,
            delta: text,
          });
        }
        emit('response.reasoning_summary_text.done', {
          item_id: item.id,
          output_index: outputIndex,
          summary_index: summaryIndex,
          text,
        });
        emit('response.reasoning_summary_part.done', {
          item_id: item.id,
          output_index: outputIndex,
          summary_index: summaryIndex,
          part,
        });
      });
    } else if (type === 'message') {
      const content = Array.isArray(item.content) ? item.content : [];
      content.forEach((part, contentIndex) => {
        const text =
          part && typeof part === 'object' && typeof (part as { text?: unknown }).text === 'string'
            ? String((part as { text: string }).text)
            : '';
        emit('response.content_part.added', {
          item_id: item.id,
          output_index: outputIndex,
          content_index: contentIndex,
          part: { type: 'output_text', text: '', annotations: [], logprobs: [] },
        });
        if (text) {
          emit('response.output_text.delta', {
            item_id: item.id,
            output_index: outputIndex,
            content_index: contentIndex,
            delta: text,
            logprobs: [],
          });
        }
        emit('response.output_text.done', {
          item_id: item.id,
          output_index: outputIndex,
          content_index: contentIndex,
          text,
          logprobs: [],
        });
      });
    } else if (type === 'function_call') {
      const args = typeof item.arguments === 'string' ? item.arguments : '';
      if (args) {
        emit('response.function_call_arguments.delta', {
          item_id: item.id,
          output_index: outputIndex,
          delta: args,
        });
      }
      emit('response.function_call_arguments.done', {
        item_id: item.id,
        output_index: outputIndex,
        arguments: args,
      });
    }
    emit('response.output_item.done', { output_index: outputIndex, item });
  });
  emit('response.completed', { response });
  res.end();
}
