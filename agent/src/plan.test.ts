import assert from 'node:assert/strict';
import { test } from 'node:test';

import { InterviewPlan, type Question } from './plan.js';
import { TranscriptRecorder } from './transcript.js';

const q = (id: string, skill: string, difficulty: number, type: Question['type'] = 'technical'): Question => ({
  id,
  skill,
  difficulty,
  type,
  text: `question ${id}`,
  expectedPoints: [],
  followUps: [],
});

const bank: Question[] = [
  q('js1', 'JavaScript', 1),
  q('js2', 'JavaScript', 2),
  q('js3', 'JavaScript', 3),
  q('re1', 'React', 1),
  q('re2', 'React', 2),
  q('re3', 'React', 3),
  q('sc1', 'Web', 2, 'scenario'),
  q('team', 'Kerja tim', 1, 'soft'),
  q('motiv', 'Motivasi', 1, 'soft'),
];

const fixed = () => 0.5; // deterministic shuffle

test('junior plan starts easy, adapts up on strong answers, ends with scenario then soft skills', () => {
  const plan = new InterviewPlan(bank, { level: 'junior', durationMinutes: 15, random: fixed });
  assert.equal(plan.total, 6); // round(15 * 0.4) = 6 -> 4 technical + 2 soft

  const first = plan.next('none');
  assert.ok(!first.done);
  assert.equal(first.number, 1);
  const firstQ = bank.find((x) => x.id === first.questionId)!;
  assert.equal(firstQ.difficulty, 1);

  const asked = [firstQ];
  for (let i = 0; i < 5; i++) {
    const s = plan.next('strong');
    assert.ok(!s.done, `step ${i} should not be done`);
    asked.push(bank.find((x) => x.id === s.questionId)!);
  }
  assert.equal(asked[3]!.type, 'scenario', 'last technical slot is the scenario');
  assert.deepEqual(asked.slice(4).map((x) => x.type), ['soft', 'soft']);
  assert.ok(plan.next('strong').done);
  // A strong answer on a skill raises the next question on that skill.
  const sameSkill = asked.filter((x) => x.skill === firstQ.skill);
  assert.ok(sameSkill.length < 2 || sameSkill[1]!.difficulty > sameSkill[0]!.difficulty);
});

test('two weak answers move off the skill', () => {
  const only = [q('a1', 'Go', 1), q('a2', 'Go', 1), q('a3', 'Go', 2), q('b1', 'SQL', 1), q('b2', 'SQL', 2)];
  const plan = new InterviewPlan(only, { level: 'junior', durationMinutes: 25, random: fixed });
  const seen: string[] = [];
  let step = plan.next('none');
  while (!step.done) {
    seen.push(bank.concat(only).find((x) => x.id === step.questionId)!.skill);
    step = plan.next('weak');
  }
  assert.equal(seen.length, 5, 'all questions eventually used');
  assert.notEqual(seen.at(-1), undefined);
});

test('never repeats a question', () => {
  const plan = new InterviewPlan(bank, { level: 'mid', durationMinutes: 60 });
  const ids = new Set<string>();
  for (let s = plan.next(); !s.done; s = plan.next('partial')) {
    assert.ok(!ids.has(s.questionId));
    ids.add(s.questionId);
  }
  assert.equal(ids.size, bank.length);
});

test('transcript recorder batches, retries and keeps timing', async () => {
  const sent: { speaker: string; text: string; startMs: number; endMs: number }[][] = [];
  let fail = true;
  const rec = new TranscriptRecorder(async (turns) => {
    if (fail) {
      fail = false;
      throw new Error('network');
    }
    sent.push(turns);
  });
  const now = Date.now();
  rec.userStarted(now + 1000);
  rec.userStopped(now + 4000);
  rec.add('candidate', '  halo   saya Budi ', now + 4100, 'js1');
  rec.add('ai', 'Terima kasih.', now + 5000);
  await rec.flush(); // first attempt fails, turns stay queued
  assert.equal(sent.length, 0);
  await rec.flush();
  assert.equal(sent.length, 1);
  const [cand, ai] = sent[0]!;
  assert.equal(cand!.text, 'halo saya Budi');
  assert.ok(cand!.startMs >= 1000 && cand!.endMs >= 4000);
  assert.equal(ai!.speaker, 'ai');
});
