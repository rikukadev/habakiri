<?php
class Post extends CActiveRecord
{
    public function tableName() { return '{{post}}'; }
    public function relations()
    {
        return array(
            // 相手はモジュールの中のモデル(protected/modules/account/models)
            'author' => array(self::BELONGS_TO, 'User', 'author_id'),
            // 自己参照(返信の木)
            'parent' => array(self::BELONGS_TO, 'Post', 'parent_id'),
            'replies' => array(self::HAS_MANY, 'Post', 'parent_id'),
            // 相手のモデルがどこにも無い(読めていない拡張のモデル)
            'tags' => array(self::MANY_MANY, 'Tag', '{{post_tag}}(post_id, tag_id)'),
            'audit' => array(self::HAS_ONE, 'AuditTrail', 'post_id'),
        );
    }

    protected function afterDelete()
    {
        Yii::app()->db->createCommand('DELETE FROM {{user_stat}} WHERE post_id = :id')->execute();
    }
}
