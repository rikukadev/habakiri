<?php
class Account extends CActiveRecord
{
    public function tableName() { return 'account'; }
    public function relations()
    {
        return array(
        );
    }
}
