import { describe, expect, it } from 'vitest';
import { pipeOpenAIStreamToAnthropic } from '../src/providers/protocol/anthropic-stream';

describe('OpenAI to Anthropic stream backpressure', () => {
  it('awaits an asynchronous downstream writer before emitting the next event', async () => {
    const encoder = new TextEncoder();
    const upstream = new Response(
      new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(
            encoder.encode(
              `data: ${JSON.stringify({ choices: [{ delta: { content: 'hello' } }] })}\n\n` +
                `data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] })}\n\n`,
            ),
          );
          controller.close();
        },
      }),
      { headers: { 'content-type': 'text/event-stream' } },
    );
    const chunks: string[] = [];
    let releaseFirst!: () => void;
    const firstBlocked = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    let firstWrite!: () => void;
    const firstWritten = new Promise<void>((resolve) => {
      firstWrite = resolve;
    });

    const piping = pipeOpenAIStreamToAnthropic(
      upstream,
      async (chunk) => {
        chunks.push(chunk);
        if (chunks.length === 1) {
          firstWrite();
          await firstBlocked;
        }
      },
      'test-model',
    );

    await firstWritten;
    await Promise.resolve();
    expect(chunks).toHaveLength(1);
    releaseFirst();
    await piping;
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.join('')).toContain('message_stop');
  });
});
