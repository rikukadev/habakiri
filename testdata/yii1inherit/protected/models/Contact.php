<?php
class Contact extends AppModel
{
    public function tableName() { return 'x2_contacts'; }
    public function relations()
    {
        return array(
            'account' => array(self::BELONGS_TO, 'Account', 'account_id'),
        );
    }
}
