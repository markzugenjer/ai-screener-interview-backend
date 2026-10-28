import type { Turn } from './api.js';

type Sender = (turns: Turn[]) => Promise<unknown>;

/**
 * Collects conversation turns with timing and sends them to the API in small
 * batches, so a crash loses at most a few seconds of transcript.
 */
export class TranscriptRecorder {
  private queue: Turn[] = [];
  private sending: Promise<void> = Promise.resolve();
  private timer?: NodeJS.Timeout;
  private readonly t0 = Date.now();

  // Speaking windows from user/agent state changes, used for pace and pause metrics.
  private userSpeech: { start?: number; end?: number } = {};
  private agentSpeech: { start?: number; end?: number } = {};

  constructor(
    private readonly send: Sender,
    private readonly onError: (err: unknown) => void = () => {},
  ) {}

  rel(epochMs = Date.now()): number {
    return Math.max(0, Math.round(epochMs - this.t0));
  }

  userStarted(at: number) {
    this.userSpeech = { start: this.rel(at) };
  }
  userStopped(at: number) {
    this.userSpeech.end = this.rel(at);
  }
  agentStarted(at: number) {
    this.agentSpeech = { start: this.rel(at) };
  }
  agentStopped(at: number) {
    this.agentSpeech.end = this.rel(at);
  }

  add(speaker: Turn['speaker'], text: string, createdAt: number, questionId?: string) {
    const clean = text.replace(/\s+/g, ' ').trim();
    if (!clean) return;
    const window = speaker === 'candidate' ? this.userSpeech : this.agentSpeech;
    const fallback = this.rel(createdAt);
    const startMs = window.start ?? fallback;
    const endMs = window.end && window.end >= startMs ? window.end : Math.max(startMs, fallback);
    this.queue.push({ speaker, text: clean, startMs, endMs, ...(questionId ? { questionId } : {}) });
    if (speaker === 'candidate') this.userSpeech = {};
    else this.agentSpeech = {};
    this.schedule();
  }

  private schedule() {
    if (this.queue.length >= 10) {
      void this.flush();
      return;
    }
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.flush(), 1500);
  }

  /** Sends everything queued so far. Safe to call repeatedly. */
  flush(): Promise<void> {
    clearTimeout(this.timer);
    this.sending = this.sending.then(async () => {
      while (this.queue.length > 0) {
        const batch = this.queue.splice(0, 50);
        try {
          await this.send(batch);
        } catch (err) {
          this.queue.unshift(...batch);
          this.onError(err);
          return;
        }
      }
    });
    return this.sending;
  }
}
