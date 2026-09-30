<?php
class Setting extends AppActiveRecord
{
    public function tableName() { return 'settings'; }
    public function relations()
    {
        // NOTE: you may need to adjust the relation name and the related
        // class name for the relations automatically generated below.
        return array(
        );
    }
}
