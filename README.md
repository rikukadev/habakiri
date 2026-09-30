# habakiri

**モノリス DB の分割案を機械的に出す CLI。**
**DB(MySQL / Postgres)または ActiveRecord のコード(Rails / Yii1)から分析し、
大域的な分割(数個の大きなまとまり)から個別の切断点(橋)までを一度に出す。**

> 名は天羽々斬(あめのはばきり)から。スサノオが八岐大蛇を斬った剣 —
> 「羽々」は大蛇の古語で、絡み合った大蛇を斬るためのもの。斬った尾の中からは
> 草薙剣が出てきた。絡んだモノリスを斬れば、中から新しいサービスが生まれる。

読み取り専用。実地検証と設計の背景は
[ブログ記事](https://rikuka.dev/blog/habakiri-magento2-bridge-cut/)に。

## クイックスタート

```console
$ go install github.com/rikukadev/habakiri@latest

$ habakiri --dsn "user:pass@tcp(127.0.0.1:3306)/mydb"     # MySQL
$ habakiri --dsn "postgres://user:pass@host:5432/mydb"    # Postgres(current_schema() が対象)
$ habakiri --rails /path/to/railsapp                      # Rails app/models(DB 不要)
$ habakiri --yii1  /path/to/yii1app                       # Yii1 relations()(DB 不要)
```

| 主なフラグ | 意味 |
|---|---|
| `--hub N` | hub 判定の次数閾値(既定 max(6, 15%)。融合したら下げる) |
| `--services N` | 分割案のグループ数の希望(既定はモジュラリティ最大) |
| `--json` / `--mermaid` | 機械可読出力 |
| `--svg` / `--svg-cut` / `--svg-partition` | E-R 図(切る前 / 切った後 / 分割案) |
| `--cut-level 1..3` | 切った後の図の深さ(既定 1) |
| `--html` | 全図 + 全表の自己完結 1 ファイル(CDN/JS 依存なし) |

DSN は環境変数 `HABAKIRI_DSN` でも渡せる。

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
`optional:` と Yii1 全般 → NULLABLE。

### アルゴリズム(すべて決定的)

| 段 | 手法 |
|---|---|
| CASCADE 縮約 | Union–Find(代表は辞書順) |
| 橋検出 | Tarjan(DFS、O(V+E)) |
| ブロック | 橋除去後の連結成分 = 2-辺連結成分 |
| hub 契約の辺化 | 二部グラフ射影(重み = FK 本数 / hub 次数) |
| 分割案 | Girvan–Newman(Brandes 辺媒介中心性)+ 重み付きモジュラリティ最大化。橋検出の一般化 |
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

- 生 SQL・サービス層の暗黙結合は見えない。クエリログ由来の共起で補うのは今後の拡張
- 静的ソースのインフレクタは簡易実装(外れたら `self.table_name` / `tableName()` で勝つ)
- 橋 0 本の密結合スキーマは「最薄の継ぎ目」フォールバックのみ

## 検証

`go test ./...`(グラフ演算・静的ソース・分割の純関数テスト)+ CI の実 Postgres E2E。
実地検証: Magento 2(295→2.4.9 358 テーブル、実切断 + HTTP 分離)/ Mastodon(146 モデル、DB 接続ゼロ)。
