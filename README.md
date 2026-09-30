# habakiri

**モノリス DB の分割案を機械的に出す CLI。**
**DB(MySQL / Postgres)または ActiveRecord のコード(Rails / Yii1)から分析し、
大域的な分割(数個の大きなまとまり)から個別の切断点(橋)までを一度に出す。**

> 名は天羽々斬(あめのはばきり)から。スサノオが八岐大蛇を斬った剣 —
> 「羽々」は大蛇の古語で、絡み合った大蛇を斬るためのもの。斬った尾の中からは
> 草薙剣が出てきた。絡んだモノリスを斬れば、中から新しいサービスが生まれる。

分析は読み取り専用。`--emit-contract` はファイル生成のみで DB に書かない。実地検証と設計の背景は
[ブログ記事](https://rikuka.dev/blog/habakiri-magento2-bridge-cut/)に。

## クイックスタート

```console
$ go install github.com/rikukadev/habakiri@latest

$ habakiri --dsn "user:pass@tcp(127.0.0.1:3306)/mydb"     # MySQL
$ habakiri --dsn "postgres://user:pass@host:5432/mydb"    # Postgres(current_schema() が対象)
$ habakiri --rails /path/to/railsapp                      # Rails app/models(DB 不要)
$ habakiri --yii1  /path/to/yii1app                       # Yii1 relations()(DB 不要)
$ habakiri --dsn "..." --yii1 /path/to/yii1app            # 併用: DB の FK と宣言を 1 つのグラフに
```

| 主なフラグ | 意味 |
|---|---|
| `--graph physical\|logical\|combined` | グラフの見方(下の用語)。既定は入力に応じる: DB だけ → physical、静的ソースだけ → logical、併用 → combined |
| `--compare-graphs` | Physical / Logical / Combined を同じ条件で解析して比べる(Coverage / Edge Diff / Community Diff)。DB と静的ソースの併用が前提 |
| `--show-evidence` | 各 FK の出自(証拠の位置・DB が強制しているか・NULL 許容・重みの理由)を text / JSON / HTML に出す |
| `--dump-schema FILE` / `--schema-json FILE` | DB スキャン結果(テーブル名と FK 定義のみ)の書き出し / 読み込み。DB に繋げない環境へスキーマだけ持ち出して解析する |
| `--hub N` | hub 判定の次数閾値(既定 max(6, 15%)。融合したら下げる) |
| `--services N` | 分割案のグループ数の希望(既定はモジュラリティ最大) |
| `--json` / `--mermaid` | 機械可読出力 |
| `--svg` / `--svg-cut` / `--svg-partition` | E-R 図(切る前 / 切った後 / 分割案) |
| `--cut-level 1..3` | 切った後の図の深さ(既定 1) |
| `--html` | 全図 + 全表の自己完結 1 ファイル(CDN/JS 依存なし) |
| `--cooc FILE` | 同一 tx 書き込み共起のログ(MySQL general log / Postgres log_statement=all / 1 行 1 tx の中立形式を自動判別) |
| `--baseline FILE` | 過去の `--json` と比較し、結合の逆行(新規 FK ペア・hub 契約増・跨ぎ FK 増)で exit 3 — CI に置く計器。**比較は静的エッジのみ**(共起はサンプリング依存で揺れるため対象外) |
| `--cooc-weight=false` | 共起を分割グラフに算入しない(レポートのみ。CI の静的モード) |
| `--patterns FILE` | 橋の「切断後に書くもの」語彙の差し替え(JSON) |
| `--emit-contract FILE` | 橋から FK DROP マイグレーションのスケルトン生成(前提ゲートをコメント同梱) |
| `--churn DIR` / `--criticality FILE` | 切り出し 1 本目候補のランキング(橋 昇順 × 変更頻度 降順 × 事故コスト 昇順。静的ソースと併用) |

DSN は環境変数 `HABAKIRI_DSN` でも渡せる。

## 用語 — どの証拠から組んだグラフか

FK が部分的にしか張られていない DB では、DB だけ読むと未整備の領域が「結合なし」に見える。
証拠の出どころを分けて持ち、見方を選べるようにしてある。

| 用語 | 定義 |
|---|---|
| **Physical** | DB スキーマから得た物理 FK のみ。DB が制約を強制している |
| **Logical** | ORM の宣言(Yii1 `relations()` / Rails associations)から得た関係のみ。物理 FK の存在を意味しない |
| **Combined** | Physical + Logical の統合。同じ関係(子テーブル・子の列・親テーブルが一致)は FK 1 本に証拠が 2 件付くだけで、重みは加算しない。属性は DB の値を優先 |
| **Observed** | `--cooc` の実行時観測。FK とは別の証拠として扱う |

「DB が強制しているか」と「NULL を許すか」は別の軸。宣言からは NULL 許容が分からないことがあり
(Yii1 の `relations()` は必須性を書けない)、その場合は NULL可 と同じ箱に入れず **unknown** として持つ。
unknown の重み 1 は「弱いと判明した」ではなく情報不足時の暫定値で、`--show-evidence` では
重みの理由が `unknown_provisional` と出る。

## 何が出るか

| 出力 | 意味 |
|---|---|
| **分割案(メイン)** | 大物数個へのグルーピング + hub 所有権の提案 + 粒度の階段(`--services N`) |
| 橋 | 分割案の境界を実行するときの FK 作業リスト(切断レベル付き) |
| hub 契約 | ユニット × hub の FK 本数・方向・NOT NULL。橋ゼロの大物同士の分離コストはここに出る |
| shared kernel(hub) | 高次数ノード。分割せず共有 or 複製 — 人間が決める |
| CASCADE 集約 | ライフサイクル一体。切らない塊 |
| 島 / 孤立テーブル | hub 経由のみ / FK なし。低コストで動かせる |
| 宣言外の疑い(静的ソース) | callback/メソッドの他モデル言及を[強]/[弱]で注記(偽エッジは作らない) |
| 実測共起(--cooc) | 同一 tx で一緒に書かれた対。FK なしの対 = 宣言に現れない結合の実測。橋に張れば切断レベル +1。分割への算入は **NPMI ≥ 0.3** のみ(生カウントはトラフィックの写し)、**共起 hub**(動的次数 ≥ 閾値)接続の対は不参加 — hub を先に外す規律を動的エッジにも適用 |

### 比較モード(`--compare-graphs`)で出るもの

FK が部分的にしか張られていない DB では、DB の FK グラフの分割は業務の塊ではなく
「FK を張った範囲」を写す。同じ入力を 3 つの見方で解析して、そのずれを測る。

| 出力 | 意味 |
|---|---|
| Observation Coverage | 入力の充足度: DB のテーブル数 / 物理 FK を持つテーブル数 / ORM が解析したテーブル数 / 宣言の数 / 重複・Logical Only・Physical Only の数。分割案のグループごとに物理 FK の保有率を出し、全体より大きく低いグループには「入力が薄い」旗を立てる |
| Edge Diff | 関係 × {Physical, Logical, Observed} の存在表。Logical Only(ORM にあるが DB が強制していない)/ Physical Only(DB にあるが ORM に無い)/ Observed Only(共起だけ)/ Both。hub に接する関係は別枠。「無い」と「見ていない」は分けて持つ |
| Community Diff | 見方の対ごとの ARI(Adjusted Rand Index、共通の非 hub 頂点の上で)、所属が変わったテーブル、片方でだけコミュニティを跨ぐ辺 |

比較では hub 集合・CASCADE 縮約・頂点集合を combined から 1 回だけ決め、3 つの見方すべてに同じものを当てる
(グラフごとに決め直すと、分割の差が入力の差なのか前処理の差なのか分からなくなるため)。
差分は**乖離の候補**であって、設計上の問題の断定ではない。

## 仕組み

```
scan → 束ね → hub 除外 → CASCADE 縮約 → 橋検出 → ブロック → hub 契約 → 分割案
```

順序が本質: hub を先に外さないと、hub への掃除 CASCADE が推移閉包で繋がり
数百テーブルが融合する(Magento 実測)。

FK の重み = 切断後に必要な API 契約の強さ:

| 重み | DB 上の意味 | 切断後の契約 | 切断レベル |
|---|---|---|---|
| NULLABLE (1) | 消えても良い参照 | 結果整合・非同期で足りる | L1(既定で切る) |
| NOT NULL (2) | 存在依存 | 存在保証 API or マスタ複製 | L2 |
| CASCADE (3) | ライフサイクル共有 | 分散トランザクション | 切らない |

宣言外の疑い[強]が同じ対に張る橋は 1 レベル加算(FK が示すより高くつく)。

静的ソースの重み写像: `dependent: :destroy` → CASCADE / 必須 `belongs_to` → NOT NULL /
`optional:` → NULLABLE / Yii1 全般 → unknown(暫定で 1)。数値は DB 由来と同じだが、
重みの理由は `logical_*_declared` / `unknown_provisional` になり、DB で確認した値と区別できる。

### アルゴリズム(すべて決定的)

| 段 | 手法 |
|---|---|
| CASCADE 縮約 | Union–Find(代表は辞書順) |
| 橋検出 | Tarjan(DFS、O(V+E)) |
| ブロック | 橋除去後の連結成分 = 2-辺連結成分 |
| hub 契約の辺化 | 二部グラフ射影(重み = FK 本数 / hub 次数) |
| 分割案 | Girvan–Newman(Brandes 辺媒介中心性)+ 重み付きモジュラリティ最大化。橋検出の一般化。グループごとに結束の内訳(FK/疑い/共起)を出し、**共起のみで束ねられたグループは ⚠ 要レビュー**。--services N 選択時は Q 最大との差 = 粒度の制約コストを表示 |
| 小コミュニティ吸着 | 3 テーブル未満を最強結合先へ編入(resolution limit 対策) |
| 粒度の階段 | デンドログラム各段を保持、`--services N` で選択 |

乱数・時刻・map 順不使用。同点は辞書順、浮動小数の加算順も固定 —
同じ入力からはバイト単位で同じ出力(SVG 含む)。

## 図(--svg 系)

- [D2](https://d2lang.com)(ELK)を**ライブラリ埋め込み** — 外部コマンド不要の 1 バイナリ
- `sql_table` shape の E-R 図。**線種 = 種類**(実線 FK / ✂ 橋 / 破線紫 = 宣言外の疑い)、
  **色 = 重み**(グレー / 青 / 朱)
- `--svg-cut` は既定レベル 1 のみ切る(全部切った図は上限の可視化であって実務の姿ではない)
- `--svg-partition` はグループ = コンテナ。群間の ✂ と集約 hub 契約線 = サービス間 API 面
- `--d2` でスクリプトも出せる(手編集 → `d2` で再描画)

## 使い方のコツ

- **ツールが測るのは切断コスト。切る価値(チーム・変更頻度・スケール)は図の外** —
  1 テーブルのマイクロサービスは作らない。分割案は束ね方の候補で、選ぶのは人間
- FK 切断だけでは何も分離されない。**アプリ層の置換(observer・読み path の API 化)とセット**
- DB がスキーマの正とは限らない。ORM / マイグレーション層(declarative schema、schema.rb、
  Prisma)が DROP を巻き戻すので、**切断の実施は宣言側で**

## 既知の限界

- 共起は「無いことの証明」に使えない(観測期間の罠 — 月次バッチは 1 週間のログに現れない)。
  狙いは時間的局所性ではなく同一 tx の原子性。時間窓ベースの緩い共起は中立形式で持ち込める
- **物理 FK が少ない領域を「結合が弱い」と読まない**。FK が無いのは観測が薄いだけのことがある —
  `--compare-graphs` の Coverage と「入力が薄い」旗を見る
- 比較モードの Logical は combined 由来の hub・CASCADE 縮約を受ける。DB 側に CASCADE が増えると、
  宣言が同じでも比較モードの Logical の頂点は変わりうる。DB に依らない Logical は単独の `--graph logical`
- 共起(Observed)は 4 つ目のグラフにはしていない。共起だけのグラフを FK のパイプラインに通すには
  偽の FK が要るため。共起は 3 つの見方への算入有無を揃えるのと、Edge Diff の列として扱う
- 併用時のテーブル名の突合は、完全一致・大文字小文字違い・接頭辞の推定(DB 側に接頭辞付きの名前が
  揃っているときだけ)まで。突合できなかった宣言は宣言側の名前のまま残り、注に出る
- 宣言だけの関係は、DB が読めていても NULL 許容を列定義から補完しない(unknown のまま)
- 静的ソースのインフレクタは簡易実装(外れたら `self.table_name` / `tableName()` で勝つ)
- 橋 0 本の密結合スキーマは「最薄の継ぎ目」フォールバックのみ

## 検証

`go test ./...`(グラフ演算・静的ソース・分割の純関数テスト + ゴールデン回帰)+ CI の実 Postgres E2E。
ゴールデンは v0.5.0 の実バイナリの出力(text / JSON / SVG)で、単独ソースの出力をバイト単位で固定している。

合成スキーマ A/B(`testdata/multisource/`): 同じ Yii1 の宣言に対して、A = 物理 FK がほぼ無い DB、
B = 注文まわりにだけ FK を張った DB。列もテーブルも同じで、違うのは FK 制約の有無だけ。

| 見方 | A と B の分割の一致(ARI) | 読み |
|---|---|---|
| Physical | 0.00 | DB だけ読むと、FK を張った範囲がそのまま「分割」になる |
| Logical | 1.00 | 宣言は同じなので同じ分割 |
| Combined | 0.84 | 差は B にだけある「ORM に宣言の無い物理 FK」の 1 テーブル |

これを `go test` に固定してある(過去の整備状況が結果を歪める現象の回帰テスト)。
実地検証: Magento 2(295→2.4.9 358 テーブル、実切断 + HTTP 分離)/ Mastodon(146 モデル、DB 接続ゼロ)。
