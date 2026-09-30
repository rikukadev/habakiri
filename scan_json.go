// scan_json.go: スキャン結果(テーブルと FK)の JSON 入出力。
//
// 本番 DB に解析機から繋げない環境では、踏み台で --dump-schema した JSON だけを
// 持ち出して --schema-json で解析する。中身は ScanResult そのもの(テーブル名と
// FK 定義だけで、行データは一切含まない)。
// テストでは「実 DB をスキャンした結果」を DB 無しで再生する入口にもなる。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// DumpSchemaJSON は ScanResult を書き出す。
func DumpSchemaJSON(path string, sc *ScanResult) error {
	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("dump-schema: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("dump-schema: %w", err)
	}
	return nil
}

// LoadSchemaJSON は --dump-schema の出力を読む。
func LoadSchemaJSON(path string) (*ScanResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("schema-json: %w", err)
	}
	var sc ScanResult
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("schema-json: %w", err)
	}
	if sc.Schema == "" {
		return nil, fmt.Errorf("schema-json: %s に schema が無い(--dump-schema の出力を渡す)", path)
	}
	sort.Strings(sc.Tables)
	return &sc, nil
}
