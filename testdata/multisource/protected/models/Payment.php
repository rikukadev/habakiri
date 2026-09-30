<?php
class Payment extends CActiveRecord
{
    public function tableName() { return 'payment'; }
    public function relations()
    {
        return array(
            'purchase' => array(self::BELONGS_TO, 'Purchase', 'purchase_id'),
        );
    }
}
