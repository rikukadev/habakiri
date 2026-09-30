<?php
class Product extends CActiveRecord
{
    public function tableName() { return 'product'; }
    public function relations()
    {
        return array(
        );
    }
}
