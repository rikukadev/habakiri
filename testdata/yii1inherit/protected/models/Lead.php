<?php
// parent::relations() に足す
class Lead extends Contact
{
    public function tableName() { return 'leads'; }
    public function relations()
    {
        return array_merge(parent::relations(), array(
            'owner' => array(self::BELONGS_TO, 'Contact', 'owner_id'),
        ));
    }
}
