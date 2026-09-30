<?php
class Timing extends AppActiveRecord
{
    public function tableName()
    {
        return $this->survey->timingsTableName;
    }
}
