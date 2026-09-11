-- Native 3x-ui subscriptions require a stable panel client ID, a SubID and URL.
-- Legacy VLESS entries cannot be converted into 3x-ui SubIDs. They are retained
-- for audit but deactivated, so clients must purchase/renew once to receive a URL.
ALTER TABLE subscriptions RENAME COLUMN vless_uuid TO panel_client_id;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS sub_id VARCHAR(255);
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS subscription_url TEXT;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;

UPDATE subscriptions
SET is_active = FALSE
WHERE sub_id IS NULL OR subscription_url IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_sub_id ON subscriptions(sub_id) WHERE sub_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_client_email ON subscriptions(client_email);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user_active ON subscriptions(user_tg_id, is_active, expires_at);
