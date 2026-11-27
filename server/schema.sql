CREATE TABLE IF NOT EXISTS rooms (
 id text PRIMARY KEY, invite text UNIQUE NOT NULL, state jsonb NOT NULL DEFAULT '{"asset":"","position":0,"rate":1,"playing":false,"version":0,"at":0}'
);
CREATE TABLE IF NOT EXISTS members (
 token text PRIMARY KEY, room text NOT NULL REFERENCES rooms(id), nickname text NOT NULL
);
CREATE TABLE IF NOT EXISTS assets (
 id text PRIMARY KEY, room text NOT NULL REFERENCES rooms(id), owner_token text NOT NULL REFERENCES members(token),
 name text NOT NULL, bytes bigint NOT NULL CHECK(bytes>0), ready boolean NOT NULL DEFAULT false,
 store text NOT NULL, object_key text NOT NULL, upload_id text NOT NULL, sha256 text
);
CREATE TABLE IF NOT EXISTS locations (
 asset text NOT NULL REFERENCES assets(id), store text NOT NULL, object_key text NOT NULL, bytes bigint NOT NULL, sha256 text, PRIMARY KEY(asset,store)
);
ALTER TABLE assets ADD COLUMN IF NOT EXISTS original_id text REFERENCES assets(id);
ALTER TABLE assets ADD COLUMN IF NOT EXISTS playable_id text REFERENCES assets(id);
CREATE TABLE IF NOT EXISTS transcodes (
 asset text PRIMARY KEY REFERENCES assets(id), status text NOT NULL DEFAULT 'queued',
 task_id text, reserved_minutes integer NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(), detail text NOT NULL DEFAULT ''
);

ALTER TABLE rooms ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT '观影房间';
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS listed boolean NOT NULL DEFAULT false;
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS owner_token text;
ALTER TABLE members ADD COLUMN IF NOT EXISTS public_id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE members ADD COLUMN IF NOT EXISTS permissions jsonb NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX IF NOT EXISTS members_public_id ON members(public_id);
CREATE INDEX IF NOT EXISTS members_room ON members(room);
CREATE INDEX IF NOT EXISTS rooms_listed ON rooms(invite) WHERE listed;

CREATE TABLE IF NOT EXISTS accounts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), handle text UNIQUE, password_hash bytea, display_name text NOT NULL
);
CREATE TABLE IF NOT EXISTS account_sessions (
 token text PRIMARY KEY, account_id uuid NOT NULL REFERENCES accounts(id), expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS social_identities (
 provider text NOT NULL, app_id text NOT NULL, subject text NOT NULL, account_id uuid NOT NULL REFERENCES accounts(id),
 PRIMARY KEY(provider,app_id,subject), UNIQUE(account_id,provider,app_id)
);
CREATE TABLE IF NOT EXISTS oauth_flows (
 state text PRIMARY KEY, provider text NOT NULL, challenge text NOT NULL, session_key text REFERENCES account_sessions(token) ON DELETE CASCADE, expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_limits (bucket text PRIMARY KEY, hits int NOT NULL, until_at timestamptz NOT NULL);
ALTER TABLE members ADD COLUMN IF NOT EXISTS account_id uuid REFERENCES accounts(id);
CREATE UNIQUE INDEX IF NOT EXISTS member_account_room ON members(room,account_id) WHERE account_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS account_memberships ON members(account_id) WHERE account_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS room_sessions (
 token text PRIMARY KEY, member_key text NOT NULL REFERENCES members(token) ON DELETE CASCADE,
 session_key text NOT NULL REFERENCES account_sessions(token) ON DELETE CASCADE, UNIQUE(member_key,session_key)
);
ALTER TABLE rooms ADD COLUMN IF NOT EXISTS last_used timestamptz NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS room_activity ON rooms(last_used);
CREATE TABLE IF NOT EXISTS media_deletions (
 store text NOT NULL, object_key text NOT NULL, upload_id text NOT NULL DEFAULT '', next_try timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(store,object_key,upload_id)
);
