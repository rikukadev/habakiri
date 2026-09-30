# habakiri

**FK グラフから「切れる場所」を機械的に出す CLI。**

名は天羽々斬(あめのはばきり)から。スサノオが八岐大蛇を斬った剣 —
「羽々」は大蛇の古語で、絡み合った大蛇を斬るためのもの。斬った尾の中からは
草薙剣が出てきた。絡んだモノリスを斬れば、中から新しいサービスが生まれる。

モノリスのデータ切り出しで「どこから手を付けるか」を、スキーマだけを材料に提案する。

読み取り専用。MySQL は information_schema、Postgres は pg_catalog しか見ない。
DSN のスキーム(`postgres://`)で自動判別する。

DB が無くても使える: `--rails` / `--yii1` は ActiveRecord の関連宣言を静的に読む。
FK を張らない文化圏で「宣言された関係」を回収するための入口(後述)。

## インストール

```console
$ go install github.com/rikukadev/habakiri@latest
```

## 使い方

```console
$ habakiri --dsn "user:pass@tcp(127.0.0.1:3306)/mydb" [--hub N] [--json] [--mermaid out.mmd]   # MySQL
$ habakiri --dsn "postgres://user:pass@127.0.0.1:5432/mydb"                                     # Postgres
```

Postgres の対象は `current_schema()`(通常 public)。別スキーマは DSN の
`?search_path=...` で切り替える。

```console
$ habakiri --rails /path/to/railsapp   # app/models の belongs_to / has_many を読む(DB 不要)
$ habakiri --yii1  /path/to/yii1app    # protected/models の relations() を読む(DB 不要)
```

静的ソースの重みの写像:

| 宣言 | 対応 | 重み |
|---|---|---|
| Rails `dependent: :destroy` / `:delete_all` | CASCADE | 3 |
| Rails `belongs_to`(5+ は必須が既定) | NOT NULL | 2 |
| Rails `belongs_to ..., optional: true` / Yii1 全般 | NULLABLE | 1 |

Yii1 の `relations()` には必須性もカスケードも宣言できないため重みは一律 1。
実務のカスケードは `beforeDelete()` の手書き削除に現れるので、callback を持つ
モデルの宣言外の他モデル言及は**注記**として出す(偽エッジは作らない)。
`MANY_MANY` は `'join(col1, col2)'` 形式から中間テーブルの FK 2 本を合成する。

DSN は環境変数 `HABAKIRI_DSN` でも渡せる(パスワードをシェル履歴に残さないため)。

## 何をしているか

```
scan → 束ね → CASCADE 縮約 → hub 除外 → 橋検出(Tarjan) → 橋ブロック木
```

順序が本質。生のグラフには users のような hub が全テーブルと繋がっていて
橋がほぼ存在しない。ライフサイクル一体(CASCADE)を潰し、hub を shared kernel
として別枠に外した**あと**の位相にこそ、意味のある橋が現れる。

FK は本数ではなく属性で重み付けする:

```
ON DELETE CASCADE (3)  >  NOT NULL (2)  >  NULLABLE (1)
ライフサイクル共有        存在依存         弱い参照
```

CASCADE は Vernon の集約基準(トランザクション整合性とライフサイクルの共有)
そのものなので、切断候補から機械的に除外してよい。

## 図の機械生成(--svg / --html)

```console
$ habakiri --dsn ... --svg out.svg    # 決定的レイアウトの SVG(同じ入力 → 同じバイト列)
$ habakiri --dsn ... --html out.html  # SVG + 切断計画をまとめた自己完結 HTML(CDN/JS 依存なし)
```

- レイアウトは橋ブロック木の放射ツリー(force ではない)。決定的なのでテスト・diff できる
- **線種 = つながりの種類**: 実線 = FK、✂ 付き太線 = 橋、点線(紫)= 宣言外の疑い(静的ソースの callback/メソッド言及)
- **色 = 重み**: グレー(NULL可)/ 青(NOT NULL)/ 朱(CASCADE 級)
- hub 経由のみで繋がる「島」はグリッド帯で別掲(DB スキャンでは sales 系のような大物がここに出る)

## 出力は 3 方向

| 枠 | 意味 |
|---|---|
| スキーマ跨ぎ FK / 孤立テーブル | 今日でも動かせる(最優先 / ゼロコスト) |
| 橋 | 1 本切るだけで塊が分離する。FK の作業リストと難易度付き |
| ブロック(2-辺連結成分) | 内部は密。これ以上の分割は設計判断(段階 2) |
| shared kernel(hub) | 分割せず共有 or 複製。人間が決める |

## FK 切断はアプリ層の分離とセット(Magento 2.4.9 実験より)

FK を切っただけの段階では、アプリの可視挙動は何も変わらない — 切断は
「分離」ではなく**分離の許可証**で、結合はアプリ層に残っている。実際に
Magento 2.4.9 で橋(downloadable↔sales、FK 2 本)を切って別サービス化した
実験では、DB の橋の細さがそのままアプリ境界の細さに対応した
(FK 2 本 → HTTP 置換 5 箇所・API 6 本)。

FK の重みは「切断後に必要な API 契約の強さ」の予測として裏返る:

| 重み | DB 上の意味 | 切断後の HTTP 契約 |
|---|---|---|
| NULLABLE (1) | 消えても良い参照 | 結果整合・非同期で足りる |
| NOT NULL (2) | 存在依存 | 存在保証 API = 同期呼び出し or マスタ複製 |
| CASCADE (3) | ライフサイクル共有 | 切ると分散トランザクション。切らない |

注意: DB がスキーマの正とは限らない。ORM・マイグレーション層
(Magento の declarative schema、Rails schema.rb、Prisma schema 等)が
DROP した FK を巻き戻すことがあるので、切断の実施は**スキーマ定義側**で行う。

## 既知の限界

- **FK が無いことは無関係の証明ではない。** 関連宣言は `--rails` / `--yii1` で
  読めるようになったが、生 SQL・サービス層の暗黙結合はまだ見えない。
  クエリログ由来の共起で重みを補正するのは今後の拡張(口は Analyze の重みに開けてある)
- 静的ソースのインフレクタは簡易実装。テーブル名が外れるモデルには
  `self.table_name` / `tableName()` を書けば勝つ
- hub 閾値の既定(max(6, ノード数の 15%))は経験則。小さいスキーマでは
  `--hub` で明示するほうがよい
- 橋が 0 本の密結合スキーマでは「最薄の継ぎ目」(重み最小エッジ)への
  フォールバックのみ。min-cut / edge betweenness は未実装

## 検証

- `go test ./...` — グラフ演算(縮約・橋・平行エッジ・hub)の純関数テスト
- 実 DB での E2E は sashiki のブランチに向けると安全
  (baseline を clone して本番相当スキーマで読み取れる)
