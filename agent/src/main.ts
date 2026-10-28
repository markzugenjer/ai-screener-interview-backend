/**
 * AI Screener voice agent.
 *
 * LiveKit dispatches this agent into a room when a candidate joins with a token
 * from the Go API. The agent loads the interview plan from the API, runs the
 * conversation (Deepgram STT -> Claude -> ElevenLabs TTS), streams the
 * transcript back to the API, and marks the interview complete at the end.
 */
import { type JobContext, type JobProcess, ServerOptions, cli, defineAgent, llm, log, voice } from '@livekit/agents';
import * as anthropic from '@livekit/agents-plugin-anthropic';
import * as deepgram from '@livekit/agents-plugin-deepgram';
import * as elevenlabs from '@livekit/agents-plugin-elevenlabs';
import * as silero from '@livekit/agents-plugin-silero';
import { fileURLToPath } from 'node:url';
import { z } from 'zod';

import { api } from './api.js';
import { config } from './config.js';
import { InterviewPlan, type Rating } from './plan.js';
import { OPENING_INSTRUCTION, SILENCE_INSTRUCTION, WRAP_UP_INSTRUCTION, interviewerInstructions } from './prompts.js';
import { TranscriptRecorder } from './transcript.js';

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/;

function interviewIdFrom(ctx: JobContext): string {
  try {
    const meta = JSON.parse(ctx.job.metadata || '{}') as { interviewId?: string };
    if (meta.interviewId && UUID.test(meta.interviewId)) return meta.interviewId;
  } catch {
    // fall through to the room name
  }
  const m = UUID.exec(ctx.room.name ?? '');
  if (!m) throw new Error(`cannot find interview id in job metadata or room name "${ctx.room.name}"`);
  return m[0];
}

export default defineAgent({
  prewarm: async (proc: JobProcess) => {
    proc.userData.vad = await silero.VAD.load();
  },

  entry: async (ctx: JobContext) => {
    const logger = log().child({ room: ctx.room.name });
    await ctx.connect();
    const interviewId = interviewIdFrom(ctx);
    const info = await api.context(interviewId);
    if (info.interview.status !== 'pending' && info.interview.status !== 'in_progress') {
      logger.warn({ interviewId, status: info.interview.status }, 'interview already finished, leaving');
      ctx.shutdown('interview already finished');
      return;
    }

    const plan = new InterviewPlan(info.questions, { level: info.job.level, durationMinutes: info.job.durationMinutes });
    const transcript = new TranscriptRecorder(
      (turns) => api.turns(interviewId, turns),
      (err) => logger.error({ err }, 'failed to save transcript; will retry'),
    );

    let finished = false;
    const finish = async (reason: string) => {
      if (finished) return;
      finished = true;
      logger.info({ interviewId, reason }, 'finishing interview');
      await transcript.flush();
      await api.complete(interviewId).catch((err: unknown) => logger.error({ err }, 'complete call failed'));
    };
    // Runs when the candidate leaves, the room closes, or the job is stopped.
    ctx.addShutdownCallback(() => finish('shutdown'));

    const agent = new voice.Agent({
      instructions: interviewerInstructions(info, plan.total),
      tools: {
        next_question: llm.tool({
          description:
            'Get the next interview question. Call this after the introduction and after each answer. ' +
            'Rate the previous answer in previous_rating (none before the first question).',
          parameters: z.object({
            previous_rating: z.enum(['strong', 'partial', 'weak', 'none']).describe('Quality of the previous answer'),
          }),
          execute: async ({ previous_rating }) => {
            const step = plan.next(previous_rating as Rating);
            if (step.done) {
              return { done: true, instruction: 'All questions are done. Close the interview politely, then call end_interview.' };
            }
            return step;
          },
        }),
        end_interview: llm.tool({
          description: 'End the interview after you have said goodbye to the candidate.',
          parameters: z.object({ reason: z.string().describe('Short reason, e.g. "all questions done"') }),
          execute: async ({ reason }) => {
            // Give the goodbye a moment to finish playing before closing the room.
            setTimeout(() => {
              void finish(reason).then(() => ctx.shutdown('interview finished'));
            }, 5000);
            return { ok: true, instruction: 'The interview has ended. Do not say anything else.' };
          },
        }),
      },
    });

    const session = new voice.AgentSession({
      vad: ctx.proc.userData.vad as Awaited<ReturnType<typeof silero.VAD.load>>,
      stt: new deepgram.STT({ model: config.sttModel, language: config.sttLanguage, smartFormat: true, punctuate: true }),
      llm: new anthropic.LLM({ model: config.llmModel, temperature: 0.5 }),
      tts: new elevenlabs.TTS({ model: config.ttsModel, voiceId: config.ttsVoiceId, language: 'id' }),
      userAwayTimeout: 15,
      turnHandling: {
        // turnDetection is left unset so LiveKit provisions its audio end-of-turn detector,
        // which works from the voice itself rather than the transcript language.
        // Candidates often pause to think: wait a little longer before replying.
        endpointing: { minDelay: 800, maxDelay: 5000 },
      },
    });

    session.on(voice.AgentSessionEventTypes.UserStateChanged, (ev) => {
      if (ev.newState === 'speaking') transcript.userStarted(ev.createdAt);
      if (ev.oldState === 'speaking') transcript.userStopped(ev.createdAt);
      if (ev.newState === 'away' && !finished) session.generateReply({ instructions: SILENCE_INSTRUCTION });
    });
    session.on(voice.AgentSessionEventTypes.AgentStateChanged, (ev) => {
      if (ev.newState === 'speaking') transcript.agentStarted(ev.createdAt);
      if (ev.oldState === 'speaking') transcript.agentStopped(ev.createdAt);
    });
    session.on(voice.AgentSessionEventTypes.ConversationItemAdded, (ev) => {
      const item = ev.item;
      if (item.type !== 'message' || (item.role !== 'user' && item.role !== 'assistant')) return;
      const text = item.textContent ?? '';
      transcript.add(item.role === 'user' ? 'candidate' : 'ai', text, item.createdAt, plan.currentQuestionId);
    });
    session.on(voice.AgentSessionEventTypes.Error, (ev) => logger.error({ err: ev.error }, 'session error'));

    await session.start({ agent, room: ctx.room });
    session.generateReply({ instructions: OPENING_INSTRUCTION });

    // Time limits: ask to wrap up one minute before the end, hard stop three minutes after.
    const minutes = info.job.durationMinutes;
    const wrapUp = setTimeout(() => {
      if (!finished) session.generateReply({ instructions: WRAP_UP_INSTRUCTION });
    }, Math.max(1, minutes - 1) * 60_000);
    const hardStop = setTimeout(() => {
      void finish('time limit').then(() => ctx.shutdown('time limit'));
    }, (minutes + 3) * 60_000);
    ctx.addShutdownCallback(async () => {
      clearTimeout(wrapUp);
      clearTimeout(hardStop);
    });
  },
});

cli.runApp(new ServerOptions({ agent: fileURLToPath(import.meta.url), agentName: config.agentName }));
