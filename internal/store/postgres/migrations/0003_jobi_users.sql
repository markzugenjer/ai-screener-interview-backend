-- Staff can sign in with JOBI (FDC employee single sign-on). Their account is
-- keyed by JOBI's stable subject id; they have no password in this app.
ALTER TABLE users ADD COLUMN IF NOT EXISTS jobi_sub text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_jobi_sub_key ON users (jobi_sub) WHERE jobi_sub <> '';
