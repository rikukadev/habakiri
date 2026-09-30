<?php
// 名前を外から渡す(任意のテーブルを包む)。静的には決められない
class Wrapper extends AppActiveRecord
{
    private $_table;

    public function __construct($table)
    {
        $this->_table = $table;
        parent::__construct();
    }

    public function tableName()
    {
        return $this->_table;
    }
}
