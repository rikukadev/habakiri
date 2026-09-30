-- 合成スキーマ B: A の注文まわりにだけ FK を張った DB(一部だけ整備が進んだ状態)。
-- 記事まわりは A と同じく FK なし。ORM に宣言の無い物理 FK が 2 本ある:
--   payment.customer_id → customer … 両端にモデルがある(Physical Only)
--   audit_log.account_id → account … audit_log にはモデルが無い。静的解析が
--     そのテーブルを見ていないので「宣言が無い」とは言えない(判定不能)
--
-- CASCADE は意図的に入れていない: 比較モードの縮約は combined から決まるので、
-- B にだけ CASCADE があると「宣言は同じなのに Logical の頂点が変わる」ことになり、
-- 受け入れ条件(Logical は A / B で同一)の前提が崩れる。
DROP SCHEMA IF EXISTS ms_b CASCADE;
CREATE SCHEMA ms_b;
SET search_path = ms_b;

-- 記事まわり(どちらのスキーマでも FK なし)
CREATE TABLE account  (id serial PRIMARY KEY);
CREATE TABLE category (id serial PRIMARY KEY);
CREATE TABLE tag      (id serial PRIMARY KEY);
CREATE TABLE article (
  id serial PRIMARY KEY,
  author_id   int NOT NULL,
  category_id int
);
CREATE TABLE comment (
  id serial PRIMARY KEY,
  article_id int NOT NULL,
  account_id int NOT NULL
);
CREATE TABLE article_tag (
  article_id int NOT NULL,
  tag_id     int NOT NULL
);
CREATE TABLE attachment (
  id serial PRIMARY KEY,
  article_id  int NOT NULL,
  uploaded_by int
);

-- 注文まわり
CREATE TABLE product (id serial PRIMARY KEY);
CREATE TABLE customer (
  id serial PRIMARY KEY,
  account_id int NOT NULL REFERENCES account(id)
);
CREATE TABLE purchase (
  id serial PRIMARY KEY,
  customer_id int NOT NULL REFERENCES customer(id),
  created_by  int
);
CREATE TABLE purchase_item (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL REFERENCES purchase(id),
  product_id  int NOT NULL REFERENCES product(id)
);
CREATE TABLE payment (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL REFERENCES purchase(id),
  customer_id int REFERENCES customer(id)                  -- モデルは両端にあるが、ORM に宣言が無い
);
CREATE TABLE shipment (
  id serial PRIMARY KEY,
  purchase_id int REFERENCES purchase(id)
);
CREATE TABLE invoice (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL REFERENCES purchase(id),
  customer_id int NOT NULL REFERENCES customer(id),
  issued_by   int
);

-- モデルの無いテーブル
CREATE TABLE audit_log (
  id serial PRIMARY KEY,
  account_id int REFERENCES account(id)
);
