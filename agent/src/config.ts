import 'dotenv/config';

function required(name: string): string {
  const v = process.env[name]?.trim();
  if (!v) throw new Error(`Missing required environment variable ${name}`);
  return v;
}

export const config = {
  apiUrl: (process.env.API_INTERNAL_URL ?? 'http://localhost:8080').replace(/\/$/, ''),
  get internalSecret() {
    return required('INTERNAL_API_SECRET');
  },
  agentName: process.env.LIVEKIT_AGENT_NAME ?? 'ai-screener',
  // Claude Haiku keeps reply latency low for live conversation; scoring uses a larger model in Go.
  llmModel: process.env.AGENT_LLM_MODEL ?? 'claude-haiku-5-5',
  sttModel: process.env.DEEPGRAM_MODEL ?? 'nova-3',
  sttLanguage: process.env.STT_LANGUAGE ?? 'id',
  ttsModel: process.env.ELEVENLABS_MODEL ?? 'eleven_flash_v2_5',
  ttsVoiceId: process.env.ELEVENLABS_VOICE_ID || undefined,
};
