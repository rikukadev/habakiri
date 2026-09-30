<?php
class User extends CActiveRecord
{
    public function tableName() { return '{{user}}'; }
    public function relations()
    {
        return array(
            'posts' => array(self::HAS_MANY, 'Post', 'author_id'),
        );
    }
}
