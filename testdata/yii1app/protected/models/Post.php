<?php
class Post extends CActiveRecord
{
    public function tableName() { return '{{post}}'; }
    public function relations()
    {
        return array(
            'author' => array(self::BELONGS_TO, 'User', 'author_id'),
            'comments' => array(self::HAS_MANY, 'Comment', 'post_id'),
            'categories' => array(self::MANY_MANY, 'Category', '{{post_category}}(post_id, category_id)'),
        );
    }
    public function beforeDelete()
    {
        foreach (Attachment::model()->findAllByAttributes(array('post_id' => $this->id)) as $a) {
            $a->delete();
        }
        return parent::beforeDelete();
    }
}
