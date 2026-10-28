import { config } from './config.js';
import type { Question } from './plan.js';

export interface InterviewContext {
  interview: { id: string; status: string; roomName: string; isPractice: boolean };
  job: {
    title: string;
    level: string;
    division: string;
    skills: string[];
    description: string;
    durationMinutes: number;
  };
  candidate: { firstName: string };
  questions: Question[];
  language: string;
}

export interface Turn {
  speaker: 'ai' | 'candidate';
  text: string;
  startMs: number;
  endMs: number;
  questionId?: string;
}

/** Client for the Go API's /internal endpoints, authenticated with the shared secret. */
async function call<T>(method: string, path: string, body?: unknown, attempt = 0): Promise<T> {
  const res = await fetch(`${config.apiUrl}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${config.internalSecret}`,
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(10_000),
  }).catch((err: unknown) => {
    if (attempt < 2) return null;
    throw err;
  });
  if (res === null || (res.status >= 500 && attempt < 2)) {
    await new Promise((r) => setTimeout(r, 500 * (attempt + 1)));
    return call<T>(method, path, body, attempt + 1);
  }
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`API ${method} ${path} failed: ${res.status} ${text.slice(0, 200)}`);
  }
  return (await res.json()) as T;
}

export const api = {
  context: (id: string) => call<InterviewContext>('GET', `/internal/interviews/${id}/context`),
  turns: (id: string, turns: Turn[]) => call<{ saved: number }>('POST', `/internal/interviews/${id}/turns`, { turns }),
  complete: (id: string) => call<{ status: string }>('POST', `/internal/interviews/${id}/complete`, {}),
};
