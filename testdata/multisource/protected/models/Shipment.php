<?php
class Shipment extends CActiveRecord
{
    public function tableName() { return 'shipment'; }
    public function relations()
    {
        return array(
            'purchase' => array(self::BELONGS_TO, 'Purchase', 'purchase_id'),
        );
    }
}
