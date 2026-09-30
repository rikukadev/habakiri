<?php
class Attachment extends CActiveRecord
{
    public function tableName() { return 'attachment'; }
    public function relations()
    {
        return array(
            'article' => array(self::BELONGS_TO, 'Article', 'article_id'),
            'uploader' => array(self::BELONGS_TO, 'Account', 'uploaded_by'),
        );
    }
}
