CREATE TABLE users (
    tg_id BIGINT PRIMARY KEY,
    username VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE servers (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    panel_url VARCHAR(255) NOT NULL,
    api_secret VARCHAR(255) NOT NULL,
    is_active BOOLEAN DEFAULT TRUE
);

CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_tg_id BIGINT REFERENCES users(tg_id),
    server_id INT REFERENCES servers(id),
    client_email VARCHAR(255) NOT NULL,
    vless_uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    expires_at TIMESTAMP NOT NULL,
    is_active BOOLEAN DEFAULT TRUE
);

CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_tg_id BIGINT REFERENCES users(tg_id),
    invoice_id BIGINT NOT NULL,
    amount NUMERIC(12, 2) NOT NULL,
    asset VARCHAR(50) NOT NULL,
    status VARCHAR(50) DEFAULT 'active',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    paid_at TIMESTAMP
);

CREATE INDEX idx_subscriptions_user_tg_id ON subscriptions(user_tg_id);
CREATE INDEX idx_subscriptions_expires_at ON subscriptions(expires_at);
CREATE INDEX idx_payments_user_tg_id ON payments(user_tg_id);
CREATE INDEX idx_payments_status ON payments(status);