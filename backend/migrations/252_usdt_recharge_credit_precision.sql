-- USDT conversion and the 1% gift keep eight decimal places in the credit ledger.
-- Existing two-decimal values remain unchanged; payment fiat amounts remain two-decimal.
SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE payment_orders
  ALTER COLUMN amount TYPE numeric(26,8),
  ALTER COLUMN bonus_amount TYPE numeric(26,8),
  ALTER COLUMN refund_amount TYPE numeric(26,8);
