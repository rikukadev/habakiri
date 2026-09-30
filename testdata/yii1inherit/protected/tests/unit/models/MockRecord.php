<?php
// テストのモック。アプリのモデルではない
class MockRecord extends CActiveRecord
{
    public function tableName() { return 'mocks'; }
}
