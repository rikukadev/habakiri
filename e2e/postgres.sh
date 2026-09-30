#!/usr/bin/env bash
# Postgres スキャナの E2E。実 Postgres にフィクスチャを流し、--json の中身を検証する。
#
# 使い方: PSQL="psql ..." ./e2e/postgres.sh "postgres://user:pass@host:port/db" ./habakiri
# PSQL を指定しなければ psql コマンドに DSN を渡す(CI はこちら)。
set -euo pipefail

DSN="${1:?DSN を渡す}"
BIN="${2:?habakiri バイナリのパスを渡す}"
PSQL="${PSQL:-psql "$DSN"}"

$PSQL -v ON_ERROR_STOP=1 -q < "$(dirname "$0")/../testdata/postgres-fixture.sql"

out=$("$BIN" --dsn "$DSN" --json)

fail() { echo "NG: $1" >&2; echo "$out" | head -60 >&2; exit 1; }

# FK は 7 本
n=$(echo "$out" | jq '.fk_count')
[ "$n" = 7 ] || fail "fk_count が 7 でない: $n"

# 孤立テーブル
echo "$out" | jq -e '.isolated_tables | index("logs")' > /dev/null || fail "孤立テーブル logs が出ていない"

# CASCADE 縮約: order_items と orders が同じライフサイクル群に入る
echo "$out" | jq -e '.cascade_groups | [keys[], .[][]] | (index("order_items") and index("orders"))' > /dev/null \
  || fail "CASCADE 縮約(orders + order_items)が効いていない"

# 橋: invoices は葉なので橋。delete_rule の写像(n → SET NULL)もここで確認できる
echo "$out" | jq -e '.bridges[] | select(.a=="invoices" or .b=="invoices")
  | .fks[0] | select(.delete_rule=="SET NULL")' > /dev/null \
  || fail "invoices の橋 / SET NULL の写像が壊れている"

# 複合 FK: pair_refs の橋で 2 列に畳まれ、片方 NULL可なので all_not_null=false
echo "$out" | jq -e '.bridges[] | select(.a=="pair_refs" or .b=="pair_refs")
  | .fks[0] | select((.child_columns|length)==2 and .all_not_null==false)' > /dev/null \
  || fail "複合 FK の畳み込み/NULL 判定が壊れている"

echo "OK: postgres e2e"
