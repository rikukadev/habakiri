<?php
// 名前を関連の相手の getter から読む($this->form->timingsTable → Form::getTimingsTable())
class Timing extends AppActiveRecord
{
    public function tableName()
    {
        return $this->form->timingsTable;
    }
    public function relations()
    {
        return array(
            'form' => array(self::BELONGS_TO, 'Form', 'form_id'),
        );
    }
}
