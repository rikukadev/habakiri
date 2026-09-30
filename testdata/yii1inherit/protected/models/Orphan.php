<?php
// 親が読んだ範囲に無く、名前からも ActiveRecord とは分からない
class Orphan extends ExtensionBaseRecord
{
    public function tableName() { return 'orphans'; }
}
