<?php
class Tag extends CActiveRecord
{
    public function tableName() { return 'tag'; }
    public function relations()
    {
        return array(
        );
    }
}
