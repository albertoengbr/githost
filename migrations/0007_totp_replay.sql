-- 0007_totp_replay: one-time enforcement for TOTP second factors.
--
-- A TOTP code stays valid across a ±1 step window. Without a stored high
-- water mark the same code can be replayed within that window. The
-- application claims each time step atomically before accepting a code;
-- this column holds the highest step already consumed by the user.

ALTER TABLE users ADD COLUMN last_totp_step INTEGER NOT NULL DEFAULT 0;
