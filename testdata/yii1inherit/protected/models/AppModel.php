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
        return $relations;
    }
}
