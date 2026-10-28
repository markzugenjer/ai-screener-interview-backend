/**
 * Adaptive interview plan.
 *
 * - Technical questions start at difficulty 1 for junior jobs (2 for mid/senior).
 * - A strong answer moves that skill one level up; a weak answer keeps it, and two
 *   weak answers in a row on one skill moves on so the candidate isn't stuck.
 * - At least one scenario question is asked when the bank has one.
 * - Soft-skill questions (teamwork, motivation) come last.
 * - Questions are shuffled within the same skill and difficulty, so candidates
 *   cannot share an exact script.
 */
export type Rating = 'strong' | 'partial' | 'weak' | 'none';

export interface Question {
  id: string;
  skill: string;
  difficulty: number;
  type: 'technical' | 'scenario' | 'soft';
  text: string;
  expectedPoints: string[];
  followUps: string[];
}

export interface NextQuestion {
  done: false;
  questionId: string;
  question: string;
  skill: string;
  type: Question['type'];
  followUps: string[];
  number: number;
  total: number;
}

export type PlanStep = NextQuestion | { done: true; total: number };

export class InterviewPlan {
  private readonly pool: Question[];
  private readonly soft: Question[];
  private readonly asked = new Set<string>();
  private readonly level = new Map<string, number>();
  private readonly weakStreak = new Map<string, number>();
  private readonly skippedSkills = new Set<string>();
  private readonly techTarget: number;
  private readonly softTarget: number;
  private techAsked = 0;
  private softAsked = 0;
  private scenarioAsked = false;
  private current?: Question;
  private skillCursor = 0;

  constructor(
    questions: Question[],
    private readonly opts: { level: string; durationMinutes: number; random?: () => number },
  ) {
    const rnd = opts.random ?? Math.random;
    const shuffled = [...questions].sort(() => rnd() - 0.5);
    this.pool = shuffled.filter((q) => q.type !== 'soft');
    this.soft = shuffled.filter((q) => q.type === 'soft');
    // Roughly one question per 2.5 minutes, between 4 and 12 in total.
    const total = Math.min(12, Math.max(4, Math.round(opts.durationMinutes * 0.4)));
    this.softTarget = Math.min(2, this.soft.length);
    this.techTarget = Math.min(total - this.softTarget, this.pool.length);
  }

  get total(): number {
    return this.techTarget + this.softTarget;
  }

  get currentQuestionId(): string | undefined {
    return this.current?.id;
  }

  /** Returns the next question, adapting to how the previous answer was rated. */
  next(previous: Rating = 'none'): PlanStep {
    this.applyRating(previous);
    const q = this.pickTechnical() ?? this.pickSoft();
    if (!q) {
      this.current = undefined;
      return { done: true, total: this.asked.size };
    }
    this.asked.add(q.id);
    this.current = q;
    return {
      done: false,
      questionId: q.id,
      question: q.text,
      skill: q.skill,
      type: q.type,
      followUps: q.followUps,
      number: this.asked.size,
      total: this.total,
    };
  }

  private applyRating(r: Rating): void {
    const q = this.current;
    if (!q || q.type === 'soft' || r === 'none') return;
    const lvl = this.levelFor(q.skill);
    if (r === 'strong') {
      this.level.set(q.skill, Math.min(3, lvl + 1));
      this.weakStreak.set(q.skill, 0);
    } else if (r === 'weak') {
      const streak = (this.weakStreak.get(q.skill) ?? 0) + 1;
      this.weakStreak.set(q.skill, streak);
      if (streak >= 2) this.skippedSkills.add(q.skill);
    } else {
      this.weakStreak.set(q.skill, 0);
    }
  }

  private levelFor(skill: string): number {
    if (!this.level.has(skill)) this.level.set(skill, this.opts.level === 'junior' ? 1 : 2);
    return this.level.get(skill)!;
  }

  private remaining(filter: (q: Question) => boolean): Question[] {
    return this.pool.filter((q) => !this.asked.has(q.id) && filter(q));
  }

  private pickTechnical(): Question | undefined {
    if (this.techAsked >= this.techTarget) return undefined;

    // Reserve the last technical slot for a scenario question.
    const needScenario = !this.scenarioAsked && this.techAsked === this.techTarget - 1;
    if (needScenario) {
      const scenario = this.closest(this.remaining((q) => q.type === 'scenario'));
      if (scenario) return this.take(scenario);
    }

    const skills = [...new Set(this.remaining((q) => q.type === 'technical').map((q) => q.skill))];
    const active = skills.filter((s) => !this.skippedSkills.has(s));
    const candidates = active.length > 0 ? active : skills;
    if (candidates.length === 0) {
      const any = this.closest(this.remaining(() => true));
      return any ? this.take(any) : undefined;
    }
    const skill = candidates[this.skillCursor % candidates.length]!;
    this.skillCursor++;
    const q = this.closest(this.remaining((x) => x.type === 'technical' && x.skill === skill), this.levelFor(skill));
    return q ? this.take(q) : undefined;
  }

  private pickSoft(): Question | undefined {
    if (this.softAsked >= this.softTarget) return undefined;
    const q = this.soft.find((x) => !this.asked.has(x.id));
    if (q) this.softAsked++;
    return q;
  }

  private take(q: Question): Question {
    this.techAsked++;
    if (q.type === 'scenario') this.scenarioAsked = true;
    return q;
  }

  /** The question whose difficulty is closest to the target level (prefers easier on ties). */
  private closest(list: Question[], target = 2): Question | undefined {
    let best: Question | undefined;
    for (const q of list) {
      if (!best) {
        best = q;
        continue;
      }
      const d = Math.abs(q.difficulty - target) - Math.abs(best.difficulty - target);
      if (d < 0 || (d === 0 && q.difficulty < best.difficulty)) best = q;
    }
    return best;
  }
}
