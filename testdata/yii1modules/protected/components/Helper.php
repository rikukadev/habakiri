<?php
// models ディレクトリの外。モデルとして読まない
class Helper extends CActiveRecord
{
    public function tableName() { return '{{helper}}'; }
}
