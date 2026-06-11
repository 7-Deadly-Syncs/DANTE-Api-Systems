-- +goose Up
ALTER TABLE transactions
    ALTER COLUMN merchant_id DROP NOT NULL;

-- +goose Down
ALTER TABLE transactions
    ALTER COLUMN merchant_id SET NOT NULL;
