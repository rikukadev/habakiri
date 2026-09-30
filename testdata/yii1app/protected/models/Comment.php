<?php
class Comment extends CActiveRecord
{
    public function tableName() { return '{{comment}}'; }
    public function relations()
    {
        return array(
            'post' => array(self::BELONGS_TO, 'Post', 'post_id'),
        );
    }
}
