<?php
class Invoice extends CActiveRecord
{
    public function tableName() { return 'invoice'; }
    public function relations()
    {
        return array(
            'purchase' => array(self::BELONGS_TO, 'Purchase', 'purchase_id'),
            'customer' => array(self::BELONGS_TO, 'Customer', 'customer_id'),
            'issuer' => array(self::BELONGS_TO, 'Account', 'issued_by'),
        );
    }
}
