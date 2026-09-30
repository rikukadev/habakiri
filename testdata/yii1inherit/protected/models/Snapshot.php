<?php
// 名前を型注釈のあるプロパティの getter から読む。族の名前は ID の後ろにも文字列がある
class Snapshot extends AppActiveRecord
{
    /** @var Form $source */
    protected $source;

    public function tableName()
    {
        return $this->source->snapshotTable;
    }
}
