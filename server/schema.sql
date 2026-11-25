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
