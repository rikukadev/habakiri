<?php
class Customer extends CActiveRecord
{
    public function tableName() { return 'customer'; }
    public function relations()
    {
        return array(
            'account' => array(self::BELONGS_TO, 'Account', 'account_id'),
        );
    }
}
