<?php
class Purchase extends CActiveRecord
{
    public function tableName() { return 'purchase'; }
    public function relations()
    {
        return array(
            'customer' => array(self::BELONGS_TO, 'Customer', 'customer_id'),
            'creator' => array(self::BELONGS_TO, 'Account', 'created_by'),
            'items' => array(self::HAS_MANY, 'PurchaseItem', 'purchase_id'),
        );
    }
}
