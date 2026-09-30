<?php
class Answer extends AppActiveRecord
{
    public function tableName() { return 'answers'; }
    public function relations()
    {
        return [
            'response' => [self::BELONGS_TO, 'Response', 'response_id'],
            'contact' => [self::BELONGS_TO, 'Contact', 'contact_id'],
        ];
    }
}
