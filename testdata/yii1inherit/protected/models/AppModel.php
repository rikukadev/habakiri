<?php
// 関連をメタデータから実行時に組み立てる基底
abstract class AppModel extends AppActiveRecord
{
    public function relations()
    {
        $relations = array();
        foreach (Field::linkFields($this->tableName()) as $field) {
            $relations[$field->name . 'Model'] = array(self::BELONGS_TO, $field->linkType, $field->name);
        }
        // ループの外に書かれた宣言は読める
        $relations['setting'] = array(self::HAS_ONE, 'Setting', 'owner_id');
        return $relations;
    }
}
