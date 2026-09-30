-- 合成スキーマ A: 物理 FK がほぼ無い DB(MyISAM 時代に作られたテーブル群を想定)。
-- FK は purchase_item → purchase の 1 本だけ。
--
-- A と B は列もテーブルも同じで、違うのは FK 制約の有無だけ。ORM の宣言
-- (protected/models/)は共通。「DB の FK 整備状況の差が分割結果を歪める」ことを
-- 回帰テストとして固定するためのフィクスチャ(compare_ab_test.go)。
-- Postgres 方言(MyISAM そのものは再現できないので「FK を張っていない」で代用)。
DROP SCHEMA IF EXISTS ms_a CASCADE;
CREATE SCHEMA ms_a;
SET search_path = ms_a;

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
  account_id int NOT NULL
);
CREATE TABLE purchase (
  id serial PRIMARY KEY,
  customer_id int NOT NULL,
  created_by  int
);
CREATE TABLE purchase_item (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL REFERENCES purchase(id),
  product_id  int NOT NULL
);
CREATE TABLE payment (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL
);
CREATE TABLE shipment (
  id serial PRIMARY KEY,
  purchase_id int
);
CREATE TABLE invoice (
  id serial PRIMARY KEY,
  purchase_id int NOT NULL,
  customer_id int NOT NULL,
  issued_by   int
);

-- モデルの無いテーブル
CREATE TABLE audit_log (
  id serial PRIMARY KEY,
  account_id int
);
