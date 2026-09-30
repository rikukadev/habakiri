-- Postgres スキャナの E2E フィクスチャ。
-- 重み 3 種(CASCADE / NOT NULL / NULL可)・複合 FK・孤立テーブル・橋を最小構成で踏む。
DROP TABLE IF EXISTS pair_refs, pairs, invoices, shipments, order_items, orders, customers, regions, logs CASCADE;

CREATE TABLE regions (id serial PRIMARY KEY);
CREATE TABLE customers (
  id serial PRIMARY KEY,
  region_id int REFERENCES regions(id)                        -- NULL可 / NO ACTION → 重み1
);
CREATE TABLE orders (
  id serial PRIMARY KEY,
  customer_id int NOT NULL REFERENCES customers(id)           -- NOT NULL → 重み2
);
CREATE TABLE order_items (
  id serial PRIMARY KEY,
  order_id int NOT NULL REFERENCES orders(id) ON DELETE CASCADE  -- CASCADE → 縮約
);
CREATE TABLE shipments (
  id serial PRIMARY KEY,
  order_id int REFERENCES orders(id) ON DELETE SET NULL,
  customer_id int NOT NULL REFERENCES customers(id)            -- orders–customers–shipments で環になる
);
CREATE TABLE invoices (
  id serial PRIMARY KEY,
  order_id int REFERENCES orders(id) ON DELETE SET NULL        -- 葉 → 橋になる
);
CREATE TABLE logs (id serial PRIMARY KEY);                      -- 孤立

-- 複合 FK(片方の列が NULL可 → AllNotNull=false)
CREATE TABLE pairs (a int, b int, PRIMARY KEY (a, b));
CREATE TABLE pair_refs (
  a int NOT NULL,
  b int,
  FOREIGN KEY (a, b) REFERENCES pairs (a, b)
);
