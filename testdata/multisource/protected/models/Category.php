<?php
class Category extends CActiveRecord
{
    public function tableName() { return 'category'; }
    public function relations()
    {
        return array(
        );
    }
}
