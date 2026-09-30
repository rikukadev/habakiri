<?php
// 2 段継承のモデル(Account → AppModel → AppActiveRecord → CActiveRecord)
class Account extends AppModel
{
    public function tableName() { return 'accounts'; }
}
