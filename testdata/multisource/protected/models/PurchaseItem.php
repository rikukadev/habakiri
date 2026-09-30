<?php
class PurchaseItem extends CActiveRecord
{
    public function tableName() { return 'purchase_item'; }
    public function relations()
    {
        return array(
            'purchase' => array(self::BELONGS_TO, 'Purchase', 'purchase_id'),
            'product' => array(self::BELONGS_TO, 'Product', 'product_id'),
        );
    }
}
