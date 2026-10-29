-- Jobs keep their public text in a source language plus translations, so the
-- job board can follow the visitor's language. Existing jobs are Indonesian.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS source_lang text NOT NULL DEFAULT 'id' CHECK (source_lang IN ('id', 'en'));
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS translations jsonb NOT NULL DEFAULT '{}';
