<?php
class Article extends CActiveRecord
{
    public function tableName() { return 'article'; }
    public function relations()
    {
        return array(
            'author' => array(self::BELONGS_TO, 'Account', 'author_id'),
            'category' => array(self::BELONGS_TO, 'Category', 'category_id'),
            'comments' => array(self::HAS_MANY, 'Comment', 'article_id'),
            'tags' => array(self::MANY_MANY, 'Tag', 'article_tag(article_id, tag_id)'),
        );
    }
}
