<?php
class Comment extends CActiveRecord
{
    public function tableName() { return 'comment'; }
    public function relations()
    {
        return array(
            'article' => array(self::BELONGS_TO, 'Article', 'article_id'),
            'account' => array(self::BELONGS_TO, 'Account', 'account_id'),
        );
    }
}
