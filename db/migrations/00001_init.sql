-- +goose Up

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    phone_number TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE merchants (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    qris_code TEXT UNIQUE NOT NULL,
    category TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE accounts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id),
    account_number TEXT UNIQUE NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE transactions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id),
    merchant_id UUID NOT NULL REFERENCES merchants(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    amount BIGINT NOT NULL CHECK (amount > 0),
    status TEXT NOT NULL CHECK (
        status IN ('INITIATED', 'PROCESSING', 'SUCCESS', 'FAILED', 'EXPIRED')
    ),
    idempotency_key TEXT NOT NULL UNIQUE,
    legacy_reference_id TEXT,
    failure_reason TEXT,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE transaction_events (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transaction_id UUID NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    message TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE legacy_call_logs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    endpoint TEXT NOT NULL,
    method TEXT NOT NULL,
    status_code INT,
    success BOOLEAN NOT NULL DEFAULT FALSE,
    latency_ms INT NOT NULL,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE cache_metrics (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    cache_key TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('HIT', 'MISS', 'SET', 'DELETE')),
    source TEXT NOT NULL DEFAULT 'redis',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE benchmark_runs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('DIRECT_LEGACY', 'DANTE')),
    virtual_users INT,
    network_profile TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE benchmark_results (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    benchmark_run_id UUID NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
    total_requests INT NOT NULL DEFAULT 0,
    successful_requests INT NOT NULL DEFAULT 0,
    failed_requests INT NOT NULL DEFAULT 0,
    p50_latency_ms INT,
    p95_latency_ms INT,
    p99_latency_ms INT,
    throughput_rps NUMERIC(10, 2),
    error_rate NUMERIC(5, 2),
    cache_hit_rate NUMERIC(5, 2),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_transactions_user_id ON transactions(user_id);
CREATE INDEX idx_transactions_merchant_id ON transactions(merchant_id);
CREATE INDEX idx_transactions_status ON transactions(status);
CREATE INDEX idx_transactions_created_at ON transactions(created_at);
CREATE INDEX idx_transaction_events_transaction_id ON transaction_events(transaction_id);
CREATE INDEX idx_legacy_call_logs_transaction_id ON legacy_call_logs(transaction_id);
CREATE INDEX idx_cache_metrics_cache_key ON cache_metrics(cache_key);
CREATE INDEX idx_benchmark_results_run_id ON benchmark_results(benchmark_run_id);

-- +goose Down

DROP TABLE IF EXISTS benchmark_results;
DROP TABLE IF EXISTS benchmark_runs;
DROP TABLE IF EXISTS cache_metrics;
DROP TABLE IF EXISTS legacy_call_logs;
DROP TABLE IF EXISTS transaction_events;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS accounts;
DROP TABLE IF EXISTS merchants;
DROP TABLE IF EXISTS users;

DROP EXTENSION IF EXISTS "uuid-ossp";
